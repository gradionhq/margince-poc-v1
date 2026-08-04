// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"go/ast"
	"go/token"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// dispatcherLiteralFloor guards against a vacuous pass. Twenty-four dispatcher
// types exist today; the floor sits at eighteen so retiring a few passes does
// not drag the gate along, while a walk that matched nothing — a rename of the
// args types, a parse that silently found no files — still trips it.
const dispatcherLiteralFloor = 18

// periodicForArgs answers every composite literal this package's own sources
// hand to periodicFor, keyed by node so the second walk can ask of a literal it
// meets "is this the one periodicFor was given" rather than compare positions.
func periodicForArgs(files []*ast.File) map[*ast.CompositeLit]struct{} {
	scheduled := map[*ast.CompositeLit]struct{}{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != "periodicFor" {
				return true
			}
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.CompositeLit); ok {
					scheduled[lit] = struct{}{}
				}
			}
			return true
		})
	}
	return scheduled
}

// TestNoScheduledDispatcherIsEnqueuedByHand — a dispatcher's args type is the
// whole fleet in one value. Constructing one anywhere but the schedule enqueues
// a pass over EVERY workspace in the installation, so a caller that meant "run
// this for the tenant in front of me" runs it for all of them, and N tenants
// hitting the same trigger run the fleet N times. The intent reads local at the
// call site and the effect is fleet-wide, which is why this is a gate rather
// than a review note: nothing about the enqueue looks wrong.
//
// The one sanctioned exception is DERIVED, not listed: a dispatcher whose
// declared cadence is on_demand has no clock, so a call site is the only thing
// that can ever place it — embed_reindex, which a human's confirm enqueues.
// Give a kind a cadence in api/jobs.yaml and its hand-enqueue sites fail here
// on the next run.
//
// What a caller wants instead is almost always the CHILD kind for the one
// workspace it knows about, enqueued through oneOffChildOpts so it carries the
// declared queue and attempt cap without being counted as a fleet pass.
func TestNoScheduledDispatcherIsEnqueuedByHand(t *testing.T) {
	byType := kindByGoType()
	fset, files := parseComposeSources(t)
	scheduled := periodicForArgs(files)

	seen := 0
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			name, ok := lit.Type.(*ast.Ident)
			if !ok {
				return true
			}
			kind, declared := byType[name.Name]
			if !declared {
				return true
			}
			spec, ok := jobs.SpecFor(kind)
			if !ok || spec.Role != jobs.Dispatcher {
				return true
			}
			seen++
			if _, viaSchedule := scheduled[lit]; viaSchedule {
				return true
			}
			if spec.Cadence.OnDemand {
				return true
			}
			t.Errorf("%s: %s (%s) is constructed outside periodicFor. It is a dispatcher with a "+
				"declared cadence, so enqueueing it fans one job out per workspace across the whole "+
				"installation — enqueue the child kind for the workspace this site actually knows about",
				position(fset, lit.Pos()), name.Name, kind)
			return true
		})
	}
	if seen < dispatcherLiteralFloor {
		t.Fatalf("only %d dispatcher args literals were found, under the floor of %d — this gate is no longer reading the package",
			seen, dispatcherLiteralFloor)
	}
}

// position renders a node's file and line for a failure message.
func position(fset *token.FileSet, pos token.Pos) string {
	return fset.Position(pos).String()
}
