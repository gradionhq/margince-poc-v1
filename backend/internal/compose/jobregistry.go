// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// How the runner hands a worker to River: through the declaration, never
// around it. A worker arrives here as jobs.WorkOnly, so whatever option
// methods it happens to carry are unreachable and api/jobs.yaml is what
// answers River's questions about it.
//
// This is the shared body, not the door. Every registration is written as the
// generated addDeclaredWorker (or its with-timeout form), whose type parameter
// is constrained to the declared set, so a kind api/jobs.yaml has never heard
// of cannot be named at a call site at all.
//
// Two ways past that constraint remain, and each has its own gate, because
// neither is something the compiler can refuse:
//
//   - Going to River directly. All three of its registration spellings —
//     AddWorker, AddWorkerArgs, AddWorkerSafely — take an unconstrained type
//     parameter and skip jobs.Govern besides, so a worker registered that way
//     answers River's option methods for itself again. forbidigo bans all
//     three outside this file.
//   - Calling addGovernedWorker below, which is constrained only to
//     river.JobArgs. That is what fixtures do, and it is deliberate; the kind
//     it records is what jobs.MustBeTotal refuses to boot on.
//
// So the compiler holds the sanctioned path, forbidigo holds the way around
// it, and MustBeTotal holds what is left — including a hand-edited generated
// file, whose union the compiler would believe.

import (
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// jobRegistry is what the runner registers into: River's own worker bundle,
// plus the kind of every worker put in it.
//
// The kind list is carried alongside because River keeps its own registry
// map unexported — there is no way to ask a *river.Workers what it holds —
// and jobs.MustBeTotal needs the set THIS process intends to work in order to
// name a kind the contract has never heard of.
type jobRegistry struct {
	workers *river.Workers
	kinds   []string
	// wired is what this build put in, keyed by kind. MustBeTotal needs only
	// the kind list above; the census needs the two things a kind string
	// cannot carry — the args value its type parameter named, whose fields the
	// declaration has to match, and the worker behind it, whose type name is
	// the only join between a declared fault posture and the receiver the
	// fault gate reads off the source.
	wired map[string]wiredWorker
}

// wiredWorker is one registration as the census reads it back.
type wiredWorker struct {
	args   river.JobArgs
	worker any
	// operatorSupplied records that the registration passed a wall clock
	// rather than leaving it to the file. Only addDeclaredWorkerWithTimeout
	// sets it, and only a {operator: …} policy reads what it passes, so the
	// two sets have to be the same one.
	operatorSupplied bool
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{workers: river.NewWorkers(), wired: map[string]wiredWorker{}}
}

// markOperatorSupplied records that this kind's wall clock was computed at its
// registration. It is called AFTER the registration that recorded the kind, so
// there is always an entry to mark.
func (r *jobRegistry) markOperatorSupplied(kind string) {
	entry, registered := r.wired[kind]
	if !registered {
		panic("compose: marking " + kind + " operator-supplied before it was registered")
	}
	entry.operatorSupplied = true
	r.wired[kind] = entry
}

// addGovernedWorker registers one worker under its DECLARED options.
//
// supplied is read only by a kind whose timeout is an operator's to set
// ({operator: …} in api/jobs.yaml — site_deep_read is the only one today);
// every other policy ignores it, so 0 is the ordinary argument.
//
// The type argument is explicit at every call site because Go cannot infer a
// type parameter from a concrete value passed to an interface parameter.
func addGovernedWorker[T river.JobArgs](reg *jobRegistry, w jobs.WorkOnly[T], supplied time.Duration) {
	var zero T
	kind := zero.Kind()
	reg.kinds = append(reg.kinds, kind)
	reg.wired[kind] = wiredWorker{args: zero, worker: w}
	// An undeclared kind is recorded and registered under the zero Spec rather
	// than rejected on the spot: MustBeTotal names every missing kind at once
	// at the end of assembly, which is a better report than the first one hit,
	// and the boot it then refuses is what keeps the zero Spec's timeout —
	// River's one-minute default — away from a running job.
	spec, _ := jobs.SpecFor(kind)
	river.AddWorker(reg.workers, jobs.Govern[T](w, spec, supplied))
}
