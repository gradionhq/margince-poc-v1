// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Re-matching LinkedIn ghosts as the CRM fills up (ADR-0078 §2.1b).
//
// The upload handler matches once, against whatever the workspace happened to
// know at that second. On a new installation that is close to nothing: an
// export is uploaded during onboarding, and the people and accounts it could
// match arrive over the following hours as mail capture runs. Every one of
// those arrivals is a match that the upload could not have made and that
// nothing else was going to make either — the ghost stays unmatched forever,
// and the account page keeps saying nobody here knows anyone.
//
// Measured on a real 5,064-row export: 54 of the workspace's contacts appeared
// in it by name, and the upload-time pass matched 13. The rest were people and
// employers the CRM learned about minutes later.
//
// The MATCH only ever looks at unmatched ghosts, so a decision a human has
// already made is never revisited. The pass around it is wider, deliberately:
// it also PROPOSES, and a suggestion that never became a proposal is invisible
// to everyone until some pass re-reaches it. So the enumeration takes undecided
// ghosts of either status and runs the staging pass for each owner it finds.
//
// That costs one re-read plus one idempotent staging attempt per outstanding
// suggestion, and it is paid even when every one of them has already been
// refused — a refusal is recorded on the approval, not on the ghost row, so
// nothing here can tell those apart. A caught-up workspace still costs one
// query; a workspace holding refusals does not.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
)

// LinkedInRematchArgs is the sweep's (empty) job payload.
type LinkedInRematchArgs struct{}

// Kind is the River job kind for the LinkedIn re-match sweep.
func (LinkedInRematchArgs) Kind() string { return "linkedin_rematch" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (LinkedInRematchArgs) FleetWide() {}

type linkedInRematchWorker struct {
	pool      *pgxpool.Pool
	store     *people.Store
	authority authz.Resolver
	// approvals is built ONCE, with the worker, rather than inside the
	// per-owner loop: it registers a dozen effects over a dozen stores and
	// answers the same service every time, so rebuilding it per member per
	// sweep pass was work with no result.
	approvals *approvals.Service
	log       *slog.Logger
}

func newLinkedInRematchWorker(pool *pgxpool.Pool, store *people.Store, authority authz.Resolver, log *slog.Logger) *linkedInRematchWorker {
	return &linkedInRematchWorker{
		pool: pool, store: store, authority: authority,
		approvals: approvalsServiceWithEffects(pool), log: log,
	}
}

// Work re-matches each workspace's unmatched ghosts, one workspace at a time so
// a failure in one leaves the others swept.
func (w *linkedInRematchWorker) Work(ctx context.Context, _ *river.Job[LinkedInRematchArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(LinkedInRematchWorkspaceArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return LinkedInRematchWorkspaceArgs{Workspace: ws} }))
}

// LinkedInRematchWorkspaceArgs is one workspace's LinkedIn re-match pass.
type LinkedInRematchWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (LinkedInRematchWorkspaceArgs) Kind() string { return "linkedin_rematch_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a LinkedInRematchWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// linkedInRematchWorkspaceWorker runs one workspace's pass. It reuses the dispatcher's
// wiring rather than a second copy of it.
type linkedInRematchWorkspaceWorker struct {
	*linkedInRematchWorker
}

func (w *linkedInRematchWorkspaceWorker) Work(ctx context.Context, job *river.Job[LinkedInRematchWorkspaceArgs]) error {
	if _, err := workspaceJobCtx(ctx, job.Args); err != nil {
		return jobs.FaultContext(ctx, err)
	}
	// Re-key BEFORE matching. A stale company key both misses its account and
	// duplicates on the next import, and matching duplicates would double
	// every reach count the matches feed.
	if err := w.renormalizeWorkspace(ctx, job.Args.Workspace); err != nil {
		return jobs.FaultContext(ctx, err)
	}
	matched, err := w.sweepWorkspace(ctx, job.Args.Workspace)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if matched.Confirmed+matched.Suggested > 0 {
		w.log.InfoContext(ctx, "linkedin re-match: new matches",
			"workspace", job.Args.Workspace.String(),
			"confirmed", matched.Confirmed, "suggested", matched.Suggested)
	}
	return nil
}

// renormalizeWorkspace recomputes the stored company keys and collapses the
// duplicates a previous normalizer left. Idempotent, so a caught-up workspace
// costs one scan and writes nothing.
func (w *linkedInRematchWorker) renormalizeWorkspace(ctx context.Context, ws ids.UUID) error {
	result, err := w.store.RenormalizeLinkedInCompanyKeys(w.systemContext(ctx, ws))
	if err != nil {
		return err
	}
	if result.Rekeyed+result.Merged > 0 {
		w.log.InfoContext(ctx, "linkedin re-normalize: company keys rebuilt",
			"workspace", ws.String(), "rekeyed", result.Rekeyed, "merged", result.Merged)
	}
	return nil
}

// systemContext binds the workspace, the trace and the maintenance principal
// both passes run under, spelled once so they cannot diverge.
//
// The correlation id is bound HERE rather than at the job entry point because
// this is the only context either pass runs under, and the write shape refuses
// an unbound trace: the staging the sweep performs commits an audit row and an
// outbox event, so a pass built without one fails on its first proposal and
// takes the whole workspace's re-match down with it — permanently, since the
// retry rebuilds the same context. A pass and a re-key are two operations and
// carry a trace each.
func (w *linkedInRematchWorker) systemContext(ctx context.Context, ws ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:linkedin_rematch",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}

func (w *linkedInRematchWorker) sweepWorkspace(ctx context.Context, ws ids.UUID) (people.LinkedInMatchResult, error) {
	// Workspace-wide, which is what the zero owner means: this pass is not
	// reporting one person's upload back to them, it is catching up every
	// member's ghosts against records the workspace has since learned.
	// Per OWNER, under that owner's own authority: see linkedinowner.go for why
	// a system principal here is an existence oracle.
	var total people.LinkedInMatchResult
	err := forEachGhostOwner(w.systemContext(ctx, ws), w.pool, w.authority, ws,
		func(ownerCtx context.Context, owner ids.UUID) error {
			matched, err := w.store.MatchLinkedInConnections(ownerCtx, owner)
			if err != nil {
				return err
			}
			total.Confirmed += matched.Confirmed
			total.Suggested += matched.Suggested
			// A suggestion is only useful once somebody can see it, and the
			// member who owns it is not necessarily importing today: the sweep
			// stages under the same authority it matched under.
			//
			// Over the WHOLE outstanding set, which is also this pass's
			// rescue duty: an owner reached here because ghostOwners now
			// enumerates `suggested` too may hold suggestions that never
			// became proposals, and this is the pass that proposes them.
			//
			// It rescues what is still proposable. A suggestion whose contact
			// has since been archived — or merged away, which archives the
			// source and re-points nothing — is read by neither this pass nor
			// the matcher, so it stays where it is; the ghost row has no
			// terminal state for a contact that left the live set.
			_, err = StageLinkedInMatches(ownerCtx, w.approvals, w.store)
			return err
		})
	return total, err
}
