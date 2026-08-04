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
// Three ways around that constraint remain, and each has its own gate,
// because none of them is something the compiler can refuse:
//
//   - Going to River directly. All three of its registration spellings —
//     AddWorker, AddWorkerArgs, AddWorkerSafely — take an unconstrained type
//     parameter and skip jobs.Govern besides, so a worker registered that way
//     answers River's option methods for itself again. forbidigo bans all
//     three outside this file.
//   - Calling addGovernedWorker below, which is constrained only to
//     river.JobArgs. That is what fixtures do, and it is deliberate; the kind
//     it records is what jobs.MustBeTotal refuses to boot on.
//   - Growing a KIND ALIAS on a type that is already declared. River registers
//     the work unit under Kind() and under every alias besides, so the union
//     is satisfied while River works a kind the file never named. The kinds
//     recorded below are therefore all of them, not just the primary.
//
// So the compiler holds the sanctioned path, forbidigo holds the way around
// it, and MustBeTotal holds what is left — including a hand-edited generated
// file, whose union the compiler would believe.

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
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

// misfiledKinds names every registration whose args type is not the one the
// contract pairs with the kind it registered under.
//
// The registration path can only ASK for the kind: Kind() is a method on the
// args type, and whatever string it returns is the one River works the rows of
// and the one the Spec is looked up by. So an args type whose Kind() names
// ANOTHER declared kind — the ordinary way one args struct is written, by
// copying the one beside it — is registered under that kind and takes its
// timeout, its queue and its attempt cap, while the kind it was meant to serve
// silently has no worker. Every other gate on this path is satisfied by that:
// both kinds are declared, so the closed type set admits it and MustBeTotal
// finds nothing missing.
//
// Spec.GoType is the other half of the pairing, carried in the compiled table
// precisely so it can be asked here. Findings rather than one error because a
// swap misfiles two kinds at once, and naming one of them would send the
// reader round the loop twice.
func misfiledKinds(wired map[string]wiredWorker) []string {
	var findings []string
	for _, kind := range slices.Sorted(maps.Keys(wired)) {
		spec, declared := jobs.SpecFor(kind)
		if !declared {
			// An undeclared kind has no GoType to be paired with, and is
			// MustBeTotal's finding (and the census totality arm's) already.
			continue
		}
		if got := goTypeName(reflect.TypeOf(wired[kind].args)); got != spec.GoType {
			findings = append(findings, fmt.Sprintf(
				"%s is registered with %s, but api/jobs.yaml pairs that kind with %s — River works these rows under the declaration %s carries, not %s's; check what Kind() returns",
				kind, got, spec.GoType, spec.GoType, got))
		}
	}
	return findings
}

// everyKindIsRegisteredWithItsDeclaredType is the boot half of the pairing
// check, refused for the same reason MustBeTotal's totality is: a process that
// started anyway would work rows under a wall clock and an attempt cap chosen
// for a different job.
func (r *jobRegistry) everyKindIsRegisteredWithItsDeclaredType() error {
	findings := misfiledKinds(r.wired)
	if len(findings) == 0 {
		return nil
	}
	return errors.New("compose: a worker is registered under a kind the contract pairs with another args type:\n  " +
		strings.Join(findings, "\n  "))
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
	// Every kind THIS registration makes workable, which is Kind() plus every
	// alias the args type answers to: River registers the work unit under all
	// of them (worker.go's AddWorker), so recording only the primary would
	// leave MustBeTotal blind to exactly the kind nobody declared. The
	// contract has no way to declare an alias — go_type is unique per kind, so
	// one args struct is one kind here — which makes any alias a boot refusal,
	// and the census says so at build time.
	reg.kinds = append(reg.kinds, kind)
	if aliased, answers := river.JobArgs(zero).(river.JobArgsWithKindAliases); answers {
		reg.kinds = append(reg.kinds, aliased.KindAliases()...)
	}
	reg.wired[kind] = wiredWorker{args: zero, worker: w}
	// An undeclared kind is recorded and registered under the zero Spec rather
	// than rejected on the spot: MustBeTotal names every missing kind at once
	// at the end of assembly, which is a better report than the first one hit,
	// and the boot it then refuses is what keeps the zero Spec's timeout —
	// River's one-minute default — away from a running job.
	spec, _ := jobs.SpecFor(kind)
	river.AddWorker(reg.workers, jobs.Govern[T](w, spec, supplied))
}
