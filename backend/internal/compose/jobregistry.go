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
// of cannot be named at a call site at all; forbidigo closes the only way
// around, a direct river.AddWorker. MustBeTotal at the end of assembly is the
// runtime restatement of the same claim, over the kind list below rather than
// over the type set — it is what a hand-edited generated file, or a fixture
// registered through this helper directly, still has to answer to.

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
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{workers: river.NewWorkers()}
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
	// An undeclared kind is recorded and registered under the zero Spec rather
	// than rejected on the spot: MustBeTotal names every missing kind at once
	// at the end of assembly, which is a better report than the first one hit,
	// and the boot it then refuses is what keeps the zero Spec's timeout —
	// River's one-minute default — away from a running job.
	spec, _ := jobs.SpecFor(kind)
	river.AddWorker(reg.workers, jobs.Govern[T](w, spec, supplied))
}
