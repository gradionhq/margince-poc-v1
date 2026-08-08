// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The running half of the extension job seam: the two args types every composed
// job shares, the dispatcher that fans out over the fleet, and the workspace
// child that re-derives its authority and then runs the unit's tick.
//
// extjobs.go is the other half — what gets registered, and the two shapes that
// are refused before anything is.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// extJobDispatcherArgs is EVERY composed extension job's dispatcher row.
//
// The kind is a FIELD rather than a constant, which is the one thing about this
// pair that differs from every other args type in the tree, and it is not a
// convenience: the composed set is not known when this package is compiled, so
// there is no type to write per kind. River reads the kind off the args value
// on both sides of the seam — args.Kind() on insert, and the value handed to
// river.AddWorkerArgs on registration — so a field is exactly as load-bearing
// as a constant would be, and the census's kind↔type pairing still holds
// because the registration passes the same value the insert will.
type extJobDispatcherArgs struct {
	JobKind string `json:"job_kind"`
}

// Kind is the composed dispatcher kind this row carries, ext_<unit>_<job>.
func (a extJobDispatcherArgs) Kind() string { return a.JobKind }

// FleetWide marks this as a dispatcher: it enumerates and enqueues and touches
// no tenant data (jobs.FleetWide).
func (extJobDispatcherArgs) FleetWide() {}

// extJobWorkspaceArgs is one workspace's tick of one composed job.
type extJobWorkspaceArgs struct {
	JobKind string `json:"job_kind"`
	// Workspace is the tenant this tick is for. The runner binds it before the
	// handler is entered, so a unit's tick can never see a global scope.
	Workspace ids.UUID `json:"workspace_id"`
	// Principal is a REFERENCE to the app_user the dispatcher recorded as this
	// tick's initiator — an id and nothing else. What that principal may do is
	// deliberately NOT in the row: it is re-derived at execution (see
	// deriveAuthority), because a row can sit in the queue across a
	// deactivation, and authority copied at enqueue time would outlive the
	// revocation that was supposed to end it.
	Principal ids.UUID `json:"principal_id"`
}

// Kind is the composed child kind, ext_<unit>_<job>_ws.
func (a extJobWorkspaceArgs) Kind() string { return a.JobKind }

// WorkspaceID binds this tick to its tenant (jobs.WorkspaceScoped).
func (a extJobWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// extJobDispatcherWorker fans one composed job out over the fleet.
type extJobDispatcherWorker struct {
	pool *pgxpool.Pool
	decl extension.JobDeclaration
}

// Work enqueues one child per live workspace, as ONE atomic insert.
//
// The fan-out is over ALL live workspaces because enablement is DIRECTORY
// PRESENCE: an installation that composed a unit composed it for the whole
// installation, and there is no per-tenant switch for the tick to consult. When
// one lands, it belongs in the enumeration below and nowhere else.
func (w *extJobDispatcherWorker) Work(ctx context.Context, _ *river.Job[extJobDispatcherArgs]) error {
	workspaces, err := enumerateWorkspaces(ctx, w.pool)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	// The actor is resolved BEFORE the insert, per workspace, inside that
	// workspace's own GUC — the sanctioned shape for a fleet pass that needs
	// tenant rows: enumerate the workspace table (which carries no tenant
	// scope), then enter each tenant to read anything of theirs. It costs one
	// round trip per workspace, which is the price of app_user being
	// RLS-scoped; a single fleet-wide read of it would have to be rls-exempt,
	// and a job seam is not a place to widen that list.
	actors := make(map[ids.UUID]ids.UUID, len(workspaces))
	for _, ws := range workspaces {
		actor, err := extensionJobActor(ctx, w.pool, ws)
		if err != nil {
			return jobs.FaultContext(ctx, err)
		}
		actors[ws] = actor
	}
	child := w.decl.ChildKind()
	// workspaceSweepOpts and not sweepInsertOpts. The difference is the whole
	// fan-out: sweepInsertOpts' uniqueness window is ByState ONLY, so N children
	// of one kind collapse to a single row and every workspace but one is
	// silently dropped. workspaceSweepOpts adds ByArgs, which makes the
	// workspace part of the unique key.
	return jobs.FaultContext(ctx, dispatchWith(ctx, workspaces, clientInsertMany(ctx), workspaceSweepOpts(child),
		func(ws ids.UUID) river.JobArgs {
			return extJobWorkspaceArgs{JobKind: child, Workspace: ws, Principal: actors[ws]}
		}))
}

// extensionJobActor answers the app_user one workspace's extension ticks are
// initiated by: its agent seat, which is the actor the product already has for
// work nobody asked for.
//
// A workspace with no live agent seat answers the zero id, and its child is
// enqueued anyway. That combination is deliberate: the fan-out stays total —
// one row per live workspace, which is what makes a missed tenant visible as a
// failed row rather than as an absence — and the tick that cannot name an
// initiator fails at the authority derivation with a message that says so.
func extensionJobActor(ctx context.Context, pool *pgxpool.Pool, ws ids.UUID) (ids.UUID, error) {
	var actor ids.UUID
	err := database.WithWorkspaceTx(principal.WithWorkspaceID(ctx, ws), pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM app_user
			  WHERE is_agent AND status = 'active' AND archived_at IS NULL
			  ORDER BY created_at LIMIT 1`).Scan(&actor)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.UUID{}, nil
	}
	if err != nil {
		return ids.UUID{}, fmt.Errorf("compose: resolving the extension job actor for workspace %s: %w", ws, err)
	}
	return actor, nil
}

// extJobWorkspaceWorker runs ONE workspace's tick of one composed job.
type extJobWorkspaceWorker struct {
	pool   *pgxpool.Pool
	decl   extension.JobDeclaration
	handle extension.JobHandler
	log    *slog.Logger
}

// errStaleJobPrincipal is what a tick meets when the principal its row names is
// no longer one this workspace has. It is a failure and not a skip: a pass that
// quietly did nothing because its actor was deactivated is indistinguishable,
// in River and in every gauge, from one that ran and found nothing to do.
var errStaleJobPrincipal = errors.New("compose: the principal this extension job was enqueued under is no longer live in this workspace")

func (w *extJobWorkspaceWorker) Work(ctx context.Context, job *river.Job[extJobWorkspaceArgs]) error {
	// FIRST, and before anything reads a row: the workspace this tick is for.
	// Every capability the handler is about to be handed re-derives the tenant
	// from the context it is invoked on, so an unpinned invocation would not
	// leak — it would refuse — but it would refuse at the unit's first query,
	// which names the wrong fault.
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	actor, err := w.deriveAuthority(wsCtx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	wsCtx = principal.WithActor(wsCtx, actor)
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
	return jobs.FaultContext(ctx, w.tick(wsCtx))
}

// deriveAuthority re-reads the recorded principal AT EXECUTION and refuses when
// it is not a live seat of this workspace.
//
// This is the whole reason the row carries an id rather than a principal. River
// is a queue: a child can be enqueued minutes or hours before it runs, can be
// retried after a backoff, and can be rescued after a crash — and across any of
// those windows the person or seat behind it can be deactivated, suspended or
// archived. A principal serialised at enqueue time would keep working through
// all three, which is precisely the authority somebody revoked.
//
// The read is workspace-pinned, so a principal id from another tenant does not
// resolve here at all — the RLS policy is what makes the workspace part of the
// question rather than a column this query has to remember to compare.
func (w *extJobWorkspaceWorker) deriveAuthority(ctx context.Context, args extJobWorkspaceArgs) (principal.Principal, error) {
	if args.Principal == (ids.UUID{}) {
		return principal.Principal{}, fmt.Errorf("%w: the dispatcher recorded no actor for this workspace", errStaleJobPrincipal)
	}
	var seat string
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT seat_type FROM app_user
			  WHERE id = $1 AND status = 'active' AND archived_at IS NULL`, args.Principal).Scan(&seat)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return principal.Principal{}, fmt.Errorf("%w (principal %s)", errStaleJobPrincipal, args.Principal)
	}
	if err != nil {
		return principal.Principal{}, fmt.Errorf("compose: deriving extension job authority: %w", err)
	}
	return principal.Principal{
		Type:     principal.PrincipalAgent,
		ID:       "agent:" + w.decl.DispatcherKind(),
		UserID:   args.Principal,
		SeatType: principal.SeatType(seat),
		// The declared scope and nothing wider. It is a REQUEST an operator
		// resolves, so it bounds the tick rather than granting it anything: the
		// capability surface the handler holds is the Runtime's, and this is
		// what the audit rows record the tick as having asked for.
		Scopes: principal.NewScopeSet(principal.Scope(w.decl.RequestedScope)),
	}, nil
}

// tick mints the call-scoped Runtime, runs the unit's handler with it, and
// releases it — the job seam's copy of extensionTool.Handle, and for the same
// reason: nothing an extension can reach exists until this line runs.
//
// The panic recovery is HERE rather than left to River's. River does recover a
// panicking worker and fails the attempt, which is the behaviour that matters —
// one attempt dies, the runner does not. What recovering here buys is the two
// things River's cannot: the released Runtime is guaranteed (the defer below
// runs before the stack unwinds past this frame), and the failure names the
// unit and the job rather than reporting an anonymous panic from inside a
// shared args type that serves every composed job in the process.
func (w *extJobWorkspaceWorker) tick(ctx context.Context) (err error) {
	pool, vault := boundExtensionRuntime()
	rt := runtimeFor(ctx, string(w.decl.Unit), pool, vault)
	defer rt.release()
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("extension job tick panicked",
				"unit", string(w.decl.Unit), "job", w.decl.Job, "kind", w.decl.ChildKind(), "panic", r)
			err = fmt.Errorf("compose: extension %q job %q panicked: %v", w.decl.Unit, w.decl.Job, r)
		}
	}()
	return w.handle(ctx, rt)
}
