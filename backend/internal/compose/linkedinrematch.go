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
// The pass only ever looks at UNMATCHED ghosts, so a human's confirmation or
// rejection is never revisited and a caught-up workspace costs one query.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

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

// linkedInRematchInterval is hourly rather than daily, because the window it
// covers is the workspace's first day: an export uploaded during onboarding is
// waiting on a capture backfill that finishes in minutes, and a rep who
// imported their network in the morning should not have to wait until tomorrow
// to see it on an account.
const linkedInRematchInterval = time.Hour

type linkedInRematchWorker struct {
	river.WorkerDefaults[LinkedInRematchArgs]
	pool      *pgxpool.Pool
	store     *people.Store
	authority authz.Resolver
	log       *slog.Logger
}

func newLinkedInRematchWorker(pool *pgxpool.Pool, store *people.Store, authority authz.Resolver, log *slog.Logger) *linkedInRematchWorker {
	return &linkedInRematchWorker{pool: pool, store: store, authority: authority, log: log}
}

// Work re-matches each workspace's unmatched ghosts, one workspace at a time so
// a failure in one leaves the others swept.
func (w *linkedInRematchWorker) Work(ctx context.Context, _ *river.Job[LinkedInRematchArgs]) error {
	workspaces, err := liveWorkspaceIDs(ctx, w.pool)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	for _, ws := range workspaces {
		// Re-key BEFORE matching. A stale company key both misses its account
		// and duplicates on the next import, and matching duplicates would
		// double every reach count the matches feed.
		if err := w.renormalizeWorkspace(ctx, ws); err != nil {
			w.log.WarnContext(ctx, "linkedin re-match: workspace re-normalize failed",
				"workspace", ws.String(), "err", err)
			continue
		}
		matched, err := w.sweepWorkspace(ctx, ws)
		if err != nil {
			w.log.WarnContext(ctx, "linkedin re-match: workspace sweep failed",
				"workspace", ws.String(), "err", err)
			continue
		}
		if matched.Confirmed+matched.Suggested > 0 {
			w.log.InfoContext(ctx, "linkedin re-match: new matches",
				"workspace", ws.String(),
				"confirmed", matched.Confirmed, "suggested", matched.Suggested)
		}
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

// systemContext binds the workspace and the maintenance principal both passes
// run under, spelled once so they cannot diverge.
func (w *linkedInRematchWorker) systemContext(ctx context.Context, ws ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
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
			_, err = StageLinkedInMatches(ownerCtx, w.pool, approvalsServiceWithEffects(w.pool), w.store)
			return err
		})
	return total, err
}
