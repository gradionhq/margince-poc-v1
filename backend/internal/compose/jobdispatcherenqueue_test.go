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
//
// What this gate does NOT see, stated so nobody reads more into a green run
// than it earns: it walks this package's own top-level product sources, so a
// dispatcher value built in a subpackage, in a cmd, or through a helper that
// returns river.JobArgs is outside it. Those are not idiom here — every
// dispatcher in the tree is a bare args struct constructed in this package —
// and the enqueue door itself is held separately, by the generated closed
// union that refuses an undeclared args type and by forbidigo's ban on a
// direct river.AddWorker.
const dispatcherLiteralFloor = 18

// periodicForArgs answers every composite literal this package's own sources
// hand to periodicFor, keyed by node so the second walk can ask of a literal it
// meets "is this the one periodicFor was given" rather than compare positions.
func periodicForArgs(files []*ast.File) map[ast.Node]struct{} {
	scheduled := map[ast.Node]struct{}{}
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
				scheduled[arg] = struct{}{}
			}
			return true
		})
	}
	return scheduled
}

// dispatcherValueSites answers every place a node in this package's sources
// produces a value of a named type, paired with that type's identifier.
//
// THREE spellings, not one. A composite literal is what the tree writes today,
// but `var a CaptureDigestArgs` and `new(CaptureDigestArgs)` produce the same
// value and reach the same Insert, so a gate that saw only the literal would be
// green about the two ways around it. The node returned is the one a caller
// compares against the periodicFor arguments — for the var form that is the
// declaration itself, which periodicFor never receives, so a scheduled
// dispatcher declared that way is always reported.
func dispatcherValueSites(files []*ast.File) map[ast.Node]*ast.Ident {
	sites := map[ast.Node]*ast.Ident{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				if name, ok := node.Type.(*ast.Ident); ok {
					sites[node] = name
				}
			case *ast.ValueSpec:
				if name, ok := node.Type.(*ast.Ident); ok {
					sites[node] = name
				}
			case *ast.CallExpr:
				fn, ok := node.Fun.(*ast.Ident)
				if !ok || fn.Name != "new" || len(node.Args) != 1 {
					return true
				}
				if name, ok := node.Args[0].(*ast.Ident); ok {
					sites[node] = name
				}
			}
			return true
		})
	}
	return sites
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
//
// All three ways of producing the value are walked — a composite literal, a
// var declaration, and new(T) — because they reach the same Insert and a gate
// that saw only the first would be green about the other two.
func TestNoScheduledDispatcherIsEnqueuedByHand(t *testing.T) {
	byType := kindByGoType()
	fset, files := parseComposeSources(t)
	scheduled := periodicForArgs(files)

	seen := 0
	for node, name := range dispatcherValueSites(files) {
		kind, declared := byType[name.Name]
		if !declared {
			continue
		}
		spec, ok := jobs.SpecFor(kind)
		if !ok || spec.Role != jobs.Dispatcher {
			continue
		}
		seen++
		if _, viaSchedule := scheduled[node]; viaSchedule {
			continue
		}
		if spec.Cadence.OnDemand {
			continue
		}
		t.Errorf("%s: %s (%s) is constructed outside periodicFor. It is a dispatcher with a "+
			"declared cadence, so enqueueing it fans one job out per workspace across the whole "+
			"installation — enqueue the child kind for the workspace this site actually knows about",
			position(fset, node.Pos()), name.Name, kind)
	}
	if seen < dispatcherLiteralFloor {
		t.Fatalf("only %d dispatcher args value(s) were found, under the floor of %d — this gate is no longer reading the package",
			seen, dispatcherLiteralFloor)
	}
}

// position renders a node's file and line for a failure message.
func position(fset *token.FileSet, pos token.Pos) string {
	return fset.Position(pos).String()
}
