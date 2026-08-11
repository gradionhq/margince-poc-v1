// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The jobs gate family, asked about an extension kind.
//
// Every gate in this package was written when "declared" meant "named in
// api/jobs.yaml", and each has an arm that reports a kind it does not recognise:
// the census's totality check, the registry's args-type pairing, and the
// wiring's derived-timeout join. A composed kind reaches all three — it is
// registered into the same jobRegistry and answered by the same jobs.SpecFor —
// so if any of them still reads the core table alone, an installation with a
// unit enabled fails its own build gate for having composed exactly what it was
// asked to compose. That is the failure this file exists to prevent, and it can
// only be seen with a composed set actually registered, which no other test in
// this package does.
//
// It is one file rather than an arm added to each gate's own tests because the
// claim is about the FAMILY: what must hold is that every gate agrees, and
// splitting it up would let a new gate join the family with nobody noticing it
// was never asked this question.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// gateProbeDecl is one well-formed extension job declaration. The queue is the
// installation's existing default pool, which is what an extension job rides:
// composing a dedicated River queue would mean composing the worker pool it
// bounds, and the job composer refuses an undeclared queue for that reason.
func gateProbeDecl() extension.JobDeclaration {
	return extension.JobDeclaration{
		Unit: "gateprobe", Job: "sweep", Queue: "default",
		Cadence: time.Hour, DispatcherTimeout: 30 * time.Second, Timeout: 2 * time.Minute,
		MaxAttempts: 3, Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
	}
}

// composeProbeJob registers one served extension job into this process's
// composed set and restores the empty set afterwards. The set is process-wide (a
// boot binding), so a test that left it behind would compose a phantom job into
// every later test in this package — including the census tests, which would
// then measure a fleet nobody deployed.
func composeProbeJob(t *testing.T, d extension.JobDeclaration) {
	t.Helper()
	err := RegisterExtensions(
		[]extension.Extension{{
			Name:    extension.Name(d.Unit),
			Version: "0.1.0",
			Jobs:    []extension.Job{{Name: d.Job, Handle: func(context.Context, extension.Runtime) error { return nil }}},
		}},
		nil,
		[]extension.JobDeclaration{d},
	)
	if err != nil {
		t.Fatalf("RegisterExtensions: %v", err)
	}
	t.Cleanup(func() {
		setComposedJobs(nil)
		setComposedTools(nil)
		setComposedVerbs(nil)
		if err := jobs.RegisterComposed(nil); err != nil {
			t.Errorf("restoring the composed job table: %v", err)
		}
	})
}

// TestTheJobsGateFamilyAdmitsARegisteredExtensionKind is the whole family at
// once, over a composed set: the census must report no finding, which means
// every one of its arms recognised both composed kinds as declared AND as
// wired. A gate reading the core table alone shows up here as a finding naming
// the ext_ kind, which is also why the assertion quotes what it found rather
// than only that it found something.
func TestTheJobsGateFamilyAdmitsARegisteredExtensionKind(t *testing.T) {
	d := gateProbeDecl()
	composeProbeJob(t, d)

	// Precondition, not decoration: if the declaration table did not gain the
	// kinds, every arm below would agree about nothing and the census would pass
	// for the wrong reason.
	for _, kind := range []string{d.DispatcherKind(), d.ChildKind()} {
		if _, declared := jobs.SpecFor(kind); !declared {
			t.Fatalf("%s is not in the declaration table after RegisterExtensions — the composed set never registered", kind)
		}
	}

	census, err := NewJobCensus()
	if err != nil {
		t.Fatalf("NewJobCensus: %v", err)
	}
	if err := census.Validate(); err != nil {
		t.Fatalf("the census refuses a composed installation:\n%v", err)
	}
	if _, wired := census.wired[d.DispatcherKind()]; !wired {
		t.Errorf("the census wired no worker for %s — it would then have nothing to hold the declaration to, and passing means nothing", d.DispatcherKind())
	}
	if _, wired := census.wired[d.ChildKind()]; !wired {
		t.Errorf("the census wired no worker for %s", d.ChildKind())
	}
}

// TestTheRegistrySPairingCheckAdmitsAComposedKind isolates the one arm whose
// finding would be actively misleading. misfiledKinds joins a registration's
// args type to Spec.GoType, and every composed kind shares one pair of args
// types by construction (the kind lives in the args VALUE) — so a pairing rule
// that assumed one type per kind would report an extension's kinds as misfiled
// and send the reader to api/jobs.yaml, which does not name them and never will.
func TestTheRegistrySPairingCheckAdmitsAComposedKind(t *testing.T) {
	d := gateProbeDecl()
	composeProbeJob(t, d)

	reg, _ := wireJobs(nil, discardLogger(), censusJobConfig())
	if err := reg.everyKindIsRegisteredWithItsDeclaredType(); err != nil {
		t.Fatalf("the boot-time pairing check refuses a composed kind: %v", err)
	}
	if findings := misfiledKinds(reg.wired); len(findings) != 0 {
		t.Errorf("misfiledKinds reports %v over a composed set", findings)
	}
	if err := jobs.MustBeTotal(reg.kinds); err != nil {
		t.Errorf("MustBeTotal refuses a boot whose composed kinds were declared through RegisterComposed: %v", err)
	}
}

// TestAnUnregisteredExtensionKindIsStillARefusal is the falsification of the two
// tests above. They would both pass if the gates had simply stopped looking at
// ext_ kinds, which is the tempting way to make them green and would put an
// extension job back on River's silent one-minute default. The namespace is not
// a waiver: a kind nothing declared is refused whether or not it is an
// extension's.
func TestAnUnregisteredExtensionKindIsStillARefusal(t *testing.T) {
	undeclared := "ext_gateprobe_never_declared_ws"
	if _, declared := jobs.SpecFor(undeclared); declared {
		t.Fatalf("%s is declared in a process that composed nothing", undeclared)
	}
	err := jobs.MustBeTotal([]string{undeclared})
	if err == nil {
		t.Fatal("MustBeTotal accepted an undeclared ext_ kind — the namespace is not an exemption from the declaration")
	}
	if !strings.Contains(err.Error(), undeclared) {
		t.Errorf("the refusal does not name %s: %v", undeclared, err)
	}
}

// TestAComposedDispatcherPublishesTheQueueItsRowsActuallyLandOn pins the half of
// the composed declaration that no other test can reach, because it only differs
// from the truth when a unit picks a pool other than the default.
//
// The dispatcher's insert options come from periodicForComposed's
// sweepInsertOpts(), which names no queue — so the row lands on River's default
// whatever the fragment says. The unit's declared queue belongs to the CHILD,
// which is the row that does the tenant's work and the pool an operator sizes.
// A dispatcher Spec republishing the unit's queue would put a label on
// margince_job_declared_info naming a pool its rows never reach, which is
// precisely the declared-versus-actual drift the whole contract exists to end.
func TestAComposedDispatcherPublishesTheQueueItsRowsActuallyLandOn(t *testing.T) {
	d := gateProbeDecl()
	// A real declared pool that is NOT the default: with Queue "default" every
	// assertion below would hold under either reading and prove nothing.
	d.Queue = "deep_read"
	if _, built := jobs.DeclaredQueues()[d.Queue]; !built {
		t.Fatalf("%s is not a queue this installation builds — pick another non-default pool", d.Queue)
	}
	composeProbeJob(t, d)

	dispatcher, ok := jobs.SpecFor(d.DispatcherKind())
	if !ok {
		t.Fatalf("%s is not declared", d.DispatcherKind())
	}
	if dispatcher.Queue != river.QueueDefault {
		t.Errorf("the dispatcher declares queue %q, but sweepInsertOpts names no queue so its rows land on %q",
			dispatcher.Queue, river.QueueDefault)
	}
	if dispatcher.OptsOwner != jobs.OptsCaller {
		t.Errorf("the dispatcher declares opts_owner %v; the periodic tick supplies its insert options, as it does for every core dispatcher", dispatcher.OptsOwner)
	}
	// The options periodicForComposed hands River, read from the same function it
	// calls: a *river.PeriodicJob keeps its constructor unexported, so the
	// alternative is inserting a row and reading the queue back — a database test
	// for a claim that is decided entirely at the insert site.
	if opts := sweepInsertOpts(); opts.Queue != "" && opts.Queue != river.QueueDefault {
		t.Errorf("the tick inserts on queue %q while the declaration publishes %q", opts.Queue, dispatcher.Queue)
	}

	child, ok := jobs.SpecFor(d.ChildKind())
	if !ok {
		t.Fatalf("%s is not declared", d.ChildKind())
	}
	if child.Queue != d.Queue {
		t.Errorf("the child declares queue %q, want the unit's %q — the child is the row the declared pool exists to bound", child.Queue, d.Queue)
	}
}
