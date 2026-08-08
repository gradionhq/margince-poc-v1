// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The extension tier's job SEAM: how a unit's scheduled pass becomes two live
// River kinds without api/jobs.yaml ever naming it.
//
// It cannot go through declaredJobArgs. That union's members are bare
// identifiers in package compose, compiled from the base contract — an
// extension module cannot be named there, and the generated file is committed
// and drift-gated, so a composed installation could not regenerate it without
// failing the gate on every build that enabled a unit. What CAN be composed is
// data: the unit's fragment declares the two kinds, gen-composition reads them
// out of the merged contract and re-emits them as extension.JobDeclaration
// literals, and this file turns each one into a registered dispatcher and a
// registered workspace child over ONE pair of args types.
//
// One pair, many kinds, because River resolves a kind from the args VALUE on
// insert (args.Kind()) and from the registered args value on registration
// (river.AddWorkerArgs). So `Kind()` reading a field is not a trick — it is the
// only shape that lets a composed set of unknown size reach River at all —
// and every other governed property still comes from the declaration, which is
// what jobs.RegisterComposed puts where SpecFor can answer for it.
//
// extjobsrun.go is the other half: the args types, the two workers, and the
// authority the child re-derives before it runs anything.

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// composedJob is one SERVED extension job: the contract's declaration and the
// unit's behavior, joined at boot.
type composedJob struct {
	decl   extension.JobDeclaration
	handle extension.JobHandler
}

// composedJobs holds the served set of this boot, written once by
// RegisterExtensions before any runner is built. Same shape and same reason as
// composedTools: the mutex guards the write-then-read ordering across the
// boot/serve boundary, not concurrent registrations.
var composedJobs struct {
	mu   sync.RWMutex
	jobs []composedJob
}

func setComposedJobs(set []composedJob) {
	composedJobs.mu.Lock()
	defer composedJobs.mu.Unlock()
	composedJobs.jobs = set
}

func servedExtensionJobs() []composedJob {
	composedJobs.mu.RLock()
	defer composedJobs.mu.RUnlock()
	return composedJobs.jobs
}

// buildExtensionJobs joins every handler-bearing Jobs entry to the declaration
// its unit's fragment published, and refuses the shapes this seam must not run.
// A job with no handler is inert — the manifest records the request and nothing
// ticks — so it is skipped here, exactly as a handler-less tool is.
func buildExtensionJobs(exts []extension.Extension, decls []extension.JobDeclaration) ([]composedJob, error) {
	declared := make(map[string]extension.JobDeclaration, len(decls))
	for _, d := range decls {
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("compose: %w", err)
		}
		key := verbKey(d.Unit, d.Job)
		if prior, dup := declared[key]; dup {
			return nil, fmt.Errorf("compose: extension %q declares job %q twice (kinds %s and %s) — one job, one pair of kinds",
				d.Unit, d.Job, prior.DispatcherKind(), d.DispatcherKind())
		}
		declared[key] = d
	}
	var built []composedJob
	// The kind namespace is global to River even though a job name is unique
	// within a unit, so a collision is checked on the derived KIND. Two units
	// cannot reach one here (the namespace carries the unit name), but a
	// composed set assembled outside the generator path could, and this stays
	// the fail-closed boundary for exactly that case.
	served := make(map[string]extension.Name)
	for _, e := range exts {
		for _, job := range e.Jobs {
			if job.Handle == nil {
				continue
			}
			d, ok := declared[verbKey(e.Name, job.Name)]
			if !ok {
				return nil, fmt.Errorf("compose: extension %q runs job %q but no kind in its api/jobs.yaml fragment declares it", e.Name, job.Name)
			}
			if owner, dup := served[d.DispatcherKind()]; dup {
				return nil, fmt.Errorf("compose: extensions %q and %q both run a job under kind %q", owner, e.Name, d.DispatcherKind())
			}
			served[d.DispatcherKind()] = e.Name
			if err := refuseUnrunnableJob(d); err != nil {
				return nil, fmt.Errorf("compose: extension %q, job %q: %w", e.Name, job.Name, err)
			}
			built = append(built, composedJob{decl: d, handle: job.Handle})
		}
	}
	return built, nil
}

// refuseUnrunnableJob is the job seam's pair of boot refusals. Both have a
// served-tool counterpart in adaptExtensionTool, and both are stronger here for
// the same single reason: a job has no caller.
func refuseUnrunnableJob(d extension.JobDeclaration) error {
	tier, err := mcpTier(d.Tier)
	if err != nil {
		return err
	}
	// A served 🟡 TOOL is refused because this surface cannot stage its
	// approval. A 🟡 JOB is worse than unstageable — it is incoherent. The
	// approval a confirm-first tier promises is a human deciding one CALL, and
	// a clock's tick is a call nobody made, addressed to nobody, arriving when
	// nobody asked. There is no request to hold open and no one whose decision
	// would be recorded, so this is refused whether or not the staging seam is
	// ever wired.
	if tier == mcp.TierConfirmationRequired {
		return fmt.Errorf("a job may not request the confirm-first tier — a job has no caller, so its confirmation is one nobody could ever be asked for")
	}
	scope, err := mcpScope(d.RequestedScope)
	if err != nil {
		return err
	}
	// Same argument, one step further. A served outbound TOOL is refused
	// because leaving the workspace needs the confirm-first tier this surface
	// cannot stage. An outbound JOB cannot reach that tier at all — the line
	// above forecloses it — so what a scheduled send/enrich asks for is
	// autonomous outbound authority on a timer: a message to a destination the
	// product did not choose, sent on a clock, with no human in the loop at any
	// point. That is not a tier this tier can resolve; it is a capability this
	// tier does not have.
	if scope.Egresses() {
		return fmt.Errorf("a job may not spend the outbound %q cap — a scheduled pass that leaves the workspace is autonomous outbound authority on a timer, and the confirm-first tier that would bound it is one a job cannot hold", scope)
	}
	return nil
}

// composedJobSpecs is the declaration table the composed set contributes: two
// Specs per served job, which jobs.RegisterComposed puts behind SpecFor so a
// composed kind answers the same questions a core one does — its wall clock,
// its queue, its attempt cap, who fans out to it.
func composedJobSpecs(set []composedJob) []jobs.Spec {
	specs := make([]jobs.Spec, 0, 2*len(set))
	for _, j := range set {
		specs = append(specs,
			jobs.Spec{
				Kind: j.decl.DispatcherKind(),
				// The args type is shared across every composed job, so GoType
				// is the same for all of them. That is honest rather than
				// lossy: the pairing gate exists to catch a Kind() copied from
				// the struct beside it, and here Kind() IS the field the
				// registration passed, so the two cannot disagree.
				GoType:     "extJobDispatcherArgs",
				Role:       jobs.Dispatcher,
				Queue:      j.decl.Queue,
				Timeout:    jobs.TimeoutPolicy{Fixed: j.decl.DispatcherTimeout},
				FanOutUnit: jobs.FanOutWorkspace,
				FanOutTo:   j.decl.ChildKind(),
				OptsOwner:  jobs.OptsArgs,
				Cadence:    jobs.Cadence{Fixed: j.decl.Cadence},
				Args:       []jobs.ArgField{{Name: "JobKind", Scalar: true, Reason: "the composed kind this row dispatches; one args type serves every extension job, so the kind is data rather than a Go type"}},
			},
			jobs.Spec{
				Kind:        j.decl.ChildKind(),
				GoType:      "extJobWorkspaceArgs",
				Role:        jobs.Workspace,
				Queue:       j.decl.Queue,
				Timeout:     jobs.TimeoutPolicy{Fixed: j.decl.Timeout},
				MaxAttempts: j.decl.MaxAttempts,
				OptsOwner:   jobs.OptsFanOut,
				Args: []jobs.ArgField{
					{Name: "JobKind", Scalar: true, Reason: "see the dispatcher's"},
					{Name: "Workspace"},
					{Name: "Principal", Reason: "a reference to the app_user the dispatcher recorded; the authority it holds is re-derived at execution and never carried in the row"},
				},
			},
		)
	}
	return specs
}

// addExtensionJobs registers this boot's served extension jobs into the runner
// and hands back the schedules that drive them — the same shape every other
// self-registering group in wireJobs takes.
//
// It registers NOTHING when the composed set is empty, which is every vanilla
// process: the ext_ queues, kinds and ticks simply do not exist there, and that
// is what keeps the composed lane a superset of the vanilla one rather than a
// different program.
func addExtensionJobs(reg *jobRegistry, pool *pgxpool.Pool, log *slog.Logger) []*river.PeriodicJob {
	set := servedExtensionJobs()
	if len(set) == 0 {
		return nil
	}
	var periodic []*river.PeriodicJob
	for _, j := range set {
		addComposedWorker(reg,
			extJobDispatcherArgs{JobKind: j.decl.DispatcherKind()},
			&extJobDispatcherWorker{pool: pool, decl: j.decl},
			j.decl.DispatcherTimeout)
		addComposedWorker(reg,
			extJobWorkspaceArgs{JobKind: j.decl.ChildKind()},
			&extJobWorkspaceWorker{pool: pool, decl: j.decl, handle: j.handle, log: log},
			j.decl.Timeout)
		periodic = append(periodic, periodicForComposed(j.decl))
	}
	return periodic
}

// periodicForComposed is periodicFor for a composed kind. It is a separate
// function rather than a widened one because periodicFor's type parameter IS
// the closed declared set — that constraint is what keeps an undeclared CORE
// kind unschedulable, and widening it to river.JobArgs to admit these two would
// give that guarantee up for every caller.
//
// RunOnStart matches every other pass here, for the reason periodicFor states:
// a restart must not defer a catch-up pass by a whole interval.
func periodicForComposed(d extension.JobDeclaration) *river.PeriodicJob {
	args := extJobDispatcherArgs{JobKind: d.DispatcherKind()}
	return river.NewPeriodicJob(
		river.PeriodicInterval(d.Cadence),
		func() (river.JobArgs, *river.InsertOpts) { return args, sweepInsertOpts() },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

// addComposedWorker is addGovernedWorker for a kind that is not in
// declaredJobArgs and cannot be: the args VALUE carries the kind, so the
// registration has to pass one rather than let River instantiate the zero type.
//
// Everything else is addGovernedWorker's: the worker reaches River as
// jobs.WorkOnly wrapped in jobs.Govern, so it answers none of River's option
// methods for itself, and the kind is recorded for jobs.MustBeTotal — which now
// finds it, because RegisterExtensions declared it through jobs.RegisterComposed
// before this runs.
func addComposedWorker[T river.JobArgs](reg *jobRegistry, args T, w jobs.WorkOnly[T], timeout time.Duration) {
	kind := args.Kind()
	reg.kinds = append(reg.kinds, kind)
	reg.wired[kind] = wiredWorker{args: args, worker: w}
	spec, _ := jobs.SpecFor(kind)
	//nolint:forbidigo // the ONE sanctioned AddWorkerArgs: a composed kind lives in the args value, not the type, so River must be handed the value — still wrapped in jobs.Govern and still recorded for MustBeTotal
	river.AddWorkerArgs(reg.workers, args, jobs.Govern[T](w, spec, timeout))
}
