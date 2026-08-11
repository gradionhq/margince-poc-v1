// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The boot-time half of the extension job seam, provable without a database:
// which declarations join to which behavior, which are refused, and what the
// composed set contributes to the declaration table.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

func jobDecl() extension.JobDeclaration {
	return extension.JobDeclaration{
		Unit: "demo", Job: "refresh", Queue: "default",
		Cadence: 6 * time.Hour, DispatcherTimeout: time.Minute, Timeout: 5 * time.Minute,
		MaxAttempts: 3, Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
	}
}

func noopTick(context.Context, extension.Runtime) error { return nil }

// unitWithJob is one composed unit declaring one job. Every case uses the same
// unit name because what varies between them is the JOB half — two units are
// spelled by calling it twice.
func unitWithJob(job string, handle extension.JobHandler) extension.Extension {
	return extension.Extension{Name: "demo", Version: "0.1.0", Jobs: []extension.Job{{Name: job, Handle: handle}}}
}

func TestBuildExtensionJobsJoinsBehaviorToItsDeclaration(t *testing.T) {
	d := jobDecl()
	built, err := buildExtensionJobs([]extension.Extension{unitWithJob("refresh", noopTick)}, []extension.JobDeclaration{d})
	if err != nil {
		t.Fatalf("buildExtensionJobs: %v", err)
	}
	if len(built) != 1 || built[0].decl.DispatcherKind() != "ext_demo_refresh" || built[0].handle == nil {
		t.Fatalf("built = %+v, want one served job", built)
	}
}

// TestAHandlerLessJobIsInert: a declared job with no Go behavior is a
// contract-only request — the manifest records it and the seam registers
// nothing, which is the same shape a handler-less tool takes. Registering a
// dispatcher for it would tick a fan-out whose children nothing works.
func TestAHandlerLessJobIsInert(t *testing.T) {
	built, err := buildExtensionJobs([]extension.Extension{unitWithJob("refresh", nil)}, []extension.JobDeclaration{jobDecl()})
	if err != nil {
		t.Fatalf("buildExtensionJobs: %v", err)
	}
	if len(built) != 0 {
		t.Fatalf("a handler-less job was served: %+v", built)
	}
}

func TestBuildExtensionJobsRefusesTheShapesTheSeamCannotRun(t *testing.T) {
	confirmFirst := jobDecl()
	confirmFirst.Tier = extension.TierConfirmationRequired
	outbound := jobDecl()
	outbound.RequestedScope = extension.ScopeSend
	invalid := jobDecl()
	invalid.Cadence = 0
	dup := jobDecl()

	for _, tc := range []struct {
		name  string
		exts  []extension.Extension
		decls []extension.JobDeclaration
		want  string
	}{
		{
			name:  "behavior no kind declares",
			exts:  []extension.Extension{unitWithJob("rebuild", noopTick)},
			decls: []extension.JobDeclaration{jobDecl()},
			want:  "no kind in its api/jobs.yaml fragment declares it",
		},
		{
			name:  "a confirm-first tier",
			exts:  []extension.Extension{unitWithJob("refresh", noopTick)},
			decls: []extension.JobDeclaration{confirmFirst},
			want:  "a job has no caller",
		},
		{
			name:  "an outbound scope",
			exts:  []extension.Extension{unitWithJob("refresh", noopTick)},
			decls: []extension.JobDeclaration{outbound},
			want:  "autonomous outbound authority on a timer",
		},
		{
			name:  "an invalid declaration",
			exts:  []extension.Extension{unitWithJob("refresh", noopTick)},
			decls: []extension.JobDeclaration{invalid},
			want:  "declares cadence",
		},
		{
			name:  "one job declared twice",
			exts:  []extension.Extension{unitWithJob("refresh", noopTick)},
			decls: []extension.JobDeclaration{jobDecl(), dup},
			want:  "declares job \"refresh\" twice",
		},
		{
			name:  "two units under one kind",
			exts:  []extension.Extension{unitWithJob("refresh", noopTick), unitWithJob("refresh", noopTick)},
			decls: []extension.JobDeclaration{jobDecl()},
			want:  "both run a job under kind",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildExtensionJobs(tc.exts, tc.decls)
			if err == nil {
				t.Fatalf("buildExtensionJobs accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("buildExtensionJobs(%s) = %v, want a message mentioning %q", tc.name, err, tc.want)
			}
		})
	}
}

// TestComposedJobSpecsDeclareBothKindsAndTheEdgeBetweenThem: the fan-out
// helpers read the declaration and nothing else, so a child whose opts_owner or
// whose dispatcher edge were missing would panic at the first insert rather
// than land on the queue and attempt cap the contract states.
func TestComposedJobSpecsDeclareBothKindsAndTheEdgeBetweenThem(t *testing.T) {
	d := jobDecl()
	specs := composedJobSpecs([]composedJob{{decl: d, handle: noopTick}})
	if len(specs) != 2 {
		t.Fatalf("got %d specs, want a dispatcher and a child", len(specs))
	}
	dispatcher, child := specs[0], specs[1]
	if dispatcher.Kind != d.DispatcherKind() || dispatcher.Role != jobs.Dispatcher {
		t.Fatalf("dispatcher spec = %+v", dispatcher)
	}
	if dispatcher.FanOutTo != d.ChildKind() || dispatcher.FanOutUnit != jobs.FanOutWorkspace {
		t.Fatalf("the dispatcher declares no workspace fan-out edge to its child: %+v", dispatcher)
	}
	if dispatcher.Cadence.Fixed != d.Cadence || dispatcher.Timeout.Duration(0) != d.DispatcherTimeout {
		t.Fatalf("dispatcher mechanics = %+v", dispatcher)
	}
	if child.Kind != d.ChildKind() || child.Role != jobs.Workspace || child.OptsOwner != jobs.OptsFanOut {
		t.Fatalf("child spec = %+v", child)
	}
	if child.MaxAttempts != d.MaxAttempts || child.Timeout.Duration(0) != d.Timeout || child.Queue != d.Queue {
		t.Fatalf("child mechanics = %+v", child)
	}
}

// TestAVanillaProcessComposesNoJobs is the property that keeps the composed
// lane a superset of the vanilla one rather than a second program: with no
// units, the seam registers no kinds, places no ticks and declares nothing.
func TestAVanillaProcessComposesNoJobs(t *testing.T) {
	built, err := buildExtensionJobs(nil, nil)
	if err != nil {
		t.Fatalf("buildExtensionJobs: %v", err)
	}
	if len(built) != 0 || len(composedJobSpecs(built)) != 0 {
		t.Fatal("a vanilla set composed a job")
	}
	if periodic := addExtensionJobs(newJobRegistry(), nil, nil); periodic != nil {
		t.Fatalf("a vanilla set placed %d tick(s)", len(periodic))
	}
}
