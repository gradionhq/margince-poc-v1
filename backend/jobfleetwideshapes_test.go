// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The FleetWide gate's own falsification, kept beside it: every dispatch shape
// the tree actually uses, proven accepted, and the shapes it exists to reject —
// a dispatcher doing a tenant's work, and a fan-out built around the
// chokepoints — proven rejected. A fitness function is only worth its blocking power
// if it never blocks a legitimate author — the one that does gets weakened by
// the person it stopped, and the weakening is what the next fleet loop walks
// back in through.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// parseFleetWideSource parses one synthetic compose file.
func parseFleetWideSource(t *testing.T, src string) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the synthetic source: %v", err)
	}
	return fset, []*ast.File{file}
}

// TestTheFleetWideGateAcceptsEveryDispatchShapeInTheTree pins the gate against
// its own false positives. A gate that rejected a legitimate dispatcher would
// be fixed by weakening it, and the weakening is what the next sweep loop walks
// back in through — so every shape the tree actually uses is a test.
func TestTheFleetWideGateAcceptsEveryDispatchShapeInTheTree(t *testing.T) {
	for name, work := range fleetWideShapesInTheTree() {
		t.Run(name, func(t *testing.T) {
			fset, files := parseFleetWideSource(t, fleetWideFixture(work))
			dispatchers, orphans := analyzeFleetWide(fset, files)
			if len(orphans) != 0 {
				t.Fatalf("the args→worker association failed: %v", orphans)
			}
			if len(dispatchers) != 1 {
				t.Fatalf("resolved %d dispatchers, want exactly 1", len(dispatchers))
			}
			if !dispatchers[0].fansOut {
				t.Errorf("the gate does not recognize this dispatch shape as a fan-out; it is in the tree and must be in the allowlist")
			}
			if len(dispatchers[0].writes) != 0 {
				t.Errorf("the gate reads a tenant write into a dispatcher that makes none: %v", dispatchers[0].writes)
			}
		})
	}
}

// TestTheFleetWideGateRejectsADispatcherThatDoesTenantWork is the falsification
// the gate exists for: both plants declare FleetWide, both would keep a null
// `args->>'workspace_id'`, and both do a tenant's work inside one row.
func TestTheFleetWideGateRejectsADispatcherThatDoesTenantWork(t *testing.T) {
	plants := map[string]string{
		"a write in the dispatcher's own body": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				for _, ws := range w.fleet {
					if _, err := w.pool.Exec(ctx, ` + "`UPDATE deal SET stage = 'stale' WHERE workspace_id = $1`" + `, ws); err != nil {
						w.log.WarnContext(ctx, "sweep failed", "workspace", ws)
					}
				}
				return nil
			}`,
		"a write behind a helper on the same worker": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				return jobs.FaultContext(ctx, w.sweepEveryTenant(ctx))
			}

			func (w *sweepWorker) sweepEveryTenant(ctx context.Context) error {
				_, err := w.pool.Exec(ctx, ` + "`DELETE FROM idempotency_key WHERE expires_at < now()`" + `)
				return err
			}`,
	}
	for name, work := range plants {
		t.Run(name, func(t *testing.T) {
			fset, files := parseFleetWideSource(t, fleetWideFixture(work))
			dispatchers, _ := analyzeFleetWide(fset, files)
			if len(dispatchers) != 1 {
				t.Fatalf("resolved %d dispatchers, want exactly 1", len(dispatchers))
			}
			if len(dispatchers[0].writes) == 0 {
				t.Errorf("the gate sees no tenant write in a dispatcher that issues one — it would let this shape back into the tree")
			}
			if dispatchers[0].fansOut {
				t.Errorf("the gate reads a fan-out into a dispatcher that enqueues nothing")
			}
		})
	}
}

// TestTheFleetWideGateRejectsAFanOutThatGoesAroundTheChokepoints — both plants
// below DO enqueue one child per due row, and both were accepted shapes until
// the three helpers became the only place a fan-out child's insert options are
// built. They are rejected now because what they enqueue is wrong rather than
// missing: no sweep tag, so the child is invisible to both sweep gauges, and
// whatever attempt cap the author typed instead of the one the child declares.
// A dispatcher shaped like this is the defect that shipped once already.
func TestTheFleetWideGateRejectsAFanOutThatGoesAroundTheChokepoints(t *testing.T) {
	plants := map[string]string{
		"a due-scan fanning out one client.Insert per due row": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				due, enumErr := w.registry.DueConnections(ctx)
				client := river.ClientFromContext[pgx.Tx](ctx)
				for _, d := range due {
					if _, err := client.Insert(ctx, SweepWorkspaceArgs{Workspace: d.Workspace}, nil); err != nil {
						enumErr = errors.Join(enumErr, err)
					}
				}
				return jobs.FaultContext(ctx, enumErr)
			}`,
		"a due-scan resolving the client through the Safely variant": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
				if err != nil {
					return jobs.FaultContext(ctx, err)
				}
				due, enumErr := w.registry.DueConnections(ctx)
				for _, d := range due {
					if _, err := client.Insert(ctx, SweepWorkspaceArgs{Workspace: d.Workspace}, nil); err != nil {
						enumErr = errors.Join(enumErr, err)
					}
				}
				return jobs.FaultContext(ctx, enumErr)
			}`,
	}
	for name, work := range plants {
		t.Run(name, func(t *testing.T) {
			fset, files := parseFleetWideSource(t, fleetWideFixture(work))
			dispatchers, _ := analyzeFleetWide(fset, files)
			if len(dispatchers) != 1 {
				t.Fatalf("resolved %d dispatchers, want exactly 1", len(dispatchers))
			}
			if dispatchers[0].fansOut {
				t.Errorf("the gate accepts a fan-out built around the chokepoints; an untagged child " +
					"would be enqueued with numbers the contract does not govern, and nothing would say so")
			}
		})
	}
}

// fleetWideFixture wraps one Work method in the smallest file that declares a
// FleetWide args type and a worker for it, so a shape can be tested without a
// package to compile it in. The worker embeds nothing, as the tree's do: the
// Work signature is what associates it with its args.
func fleetWideFixture(work string) string {
	return `package compose

type SweepArgs struct{}

func (SweepArgs) Kind() string { return "sweep" }
func (SweepArgs) FleetWide()   {}

type sweepWorker struct {
	pool *pgxpool.Pool
}
` + work + "\n"
}

// fleetWideShapesInTheTree is every dispatch shape the tree actually uses,
// kept beside the gate as its own falsification. Its own function because the
// table is the content and the loop above is four assertions.
func fleetWideShapesInTheTree() map[string]string {
	return map[string]string{
		"dispatchPerWorkspace over the live fleet": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
					workspaceSweepOpts(SweepWorkspaceArgs{}.Kind()),
					func(ws ids.UUID) river.JobArgs { return SweepWorkspaceArgs{Workspace: ws} }))
			}`,
		"dispatchWith over an archived-inclusive enumeration": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				workspaces, err := enumerateEveryWorkspace(ctx, w.pool)
				if err != nil {
					return jobs.FaultContext(ctx, err)
				}
				return jobs.FaultContext(ctx, dispatchWith(ctx, workspaces, clientInsertMany(ctx),
					workspaceSweepOpts(SweepWorkspaceArgs{}.Kind()),
					func(ws ids.UUID) river.JobArgs { return SweepWorkspaceArgs{Workspace: ws} }))
			}`,
		"a due-scan fanning out one dispatchOne per due row": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				due, enumErr := w.registry.DueConnections(ctx)
				for _, d := range due {
					if err := dispatchOne(ctx, SweepWorkspaceArgs{Workspace: d.Workspace}, nil); err != nil {
						enumErr = errors.Join(enumErr, err)
					}
				}
				return jobs.FaultContext(ctx, enumErr)
			}`,
		"a due-scan fanning out with the caller's own insert options": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				due, enumErr := w.store.DueDeferredBuilds(ctx)
				for _, ref := range due {
					if err := dispatchOne(ctx, SweepWorkspaceArgs{Workspace: ref.Workspace}, buildInsertOpts()); err != nil {
						w.log.WarnContext(ctx, "retry enqueue failed", "build", ref.BuildID)
					}
				}
				return jobs.FaultContext(ctx, enumErr)
			}`,
		"a helper on the same worker that holds the fan-out": `
			func (w *sweepWorker) Work(ctx context.Context, job *river.Job[SweepArgs]) error {
				return jobs.FaultContext(ctx, w.fanOut(ctx, job.Args))
			}

			func (w *sweepWorker) fanOut(ctx context.Context, args SweepArgs) error {
				workspaces, err := enumerateWorkspaces(ctx, w.pool)
				if err != nil {
					return err
				}
				return w.store.SeedFleet(ctx, args.Run, func(tx pgx.Tx) error {
					return dispatchWith(ctx, workspaces, txInsertMany(tx),
						workspaceSweepOpts(SweepWorkspaceArgs{}.Kind()),
						func(ws ids.UUID) river.JobArgs { return SweepWorkspaceArgs{Workspace: ws} })
				})
			}`,
		"a due-scan that reads a tenant table before fanning out": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				rows, err := w.pool.Query(ctx, ` + "`SELECT workspace_id FROM connection WHERE next_sweep_at <= now() FOR UPDATE`" + `)
				if err != nil {
					return jobs.FaultContext(ctx, err)
				}
				defer rows.Close()
				return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
					workspaceSweepOpts(SweepWorkspaceArgs{}.Kind()),
					func(ws ids.UUID) river.JobArgs { return SweepWorkspaceArgs{Workspace: ws} }))
			}`,
	}
}
