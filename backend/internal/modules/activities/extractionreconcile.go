// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ExtractionActivityReconcileWindow bounds how far back a settled reading is
// re-announced.
//
// It is wider than the projection's live set on purpose: a settled reading
// whose closing event was lost renders forever as whatever it last was —
// running, most damagingly — and the only way the projection learns otherwise
// is a pass that says so again. It is narrower than the projection's retention,
// because re-announcing a reading the projection has already aged out would
// resurrect it.
const ExtractionActivityReconcileWindow = 24 * time.Hour

// reconcileExtractionSQL selects the readings whose current state the
// projection could still be wrong about: everything live, and everything
// settled recently enough that a wrong display would still be on screen.
//
// TWO ARMS rather than one OR, and that is what makes it indexable: a predicate
// of `status IN ('queued','running') OR finished_at > $1` can use neither
// partial index, so every pass would scan the whole reading history and sort
// it. Each arm here matches one of the two partial indexes attachment_extraction
// carries for exactly this pass.
//
// Each arm carries its OWN budget, and the union is not re-limited. A single
// outer LIMIT would be filled from the live arm first — Postgres evaluates
// UNION ALL in order — so on an installation holding a full batch of live
// readings the settled arm would never be reached, and the settled arm is the
// one that repairs the worst display there is: a reading whose closing event
// was lost renders as running forever.
//
// Ordered by activity_announced_at, oldest first, NULLS FIRST — the pass's
// ROTATION KEY, and the reason a bounded batch makes progress at all. Ordering
// by anything an announce does not change selects the same rows every tick
// forever: a reading past the batch bound would never be reconciled, and a
// permanently-live one (a job discarded after its attempts, which strands the
// row until a human presses the button again) would occupy a slot and write a
// ledger row every tick for an announcement the guard then refuses.
const reconcileExtractionSQL = `
(
  SELECT ` + extractionReadColumns + `
    FROM attachment_extraction
   WHERE status IN ('queued','running')
   ORDER BY activity_announced_at ASC NULLS FIRST
   LIMIT $2
)
UNION ALL
(
  SELECT ` + extractionReadColumns + `
    FROM attachment_extraction
   WHERE status IN ('done','failed')
     AND finished_at > $1
   ORDER BY activity_announced_at ASC NULLS FIRST
   LIMIT $3
)`

// markExtractionAnnouncedSQL advances the rotation key for the batch just
// announced. In the same transaction as the announcements, so a pass that rolls
// back re-selects exactly what it failed on rather than skipping it.
const markExtractionAnnouncedSQL = `
UPDATE attachment_extraction SET activity_announced_at = now() WHERE id = ANY($1)`

// ReconcileExtractionActivity re-publishes the current state of every reading
// the AI-activity projection could still be wrong about, returning how many it
// announced.
//
// It re-PUBLISHES rather than writing the projection directly, and that is the
// whole design: ai_task_run has exactly one writer, so the guard is the only
// thing that ever decides what lands. A repair path that wrote the table itself
// would be a second writer with no guard, and it would win races against the
// bus it is supposed to be repairing.
func (s *Store) ReconcileExtractionActivity(ctx context.Context, limit int, now time.Time) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("activities: the extraction-activity reconcile limit must be positive, got %d", limit)
	}
	total := 0
	err := s.tx(ctx, func(tx pgx.Tx) error {
		reads, err := selectExtractionReads(ctx, tx, now.Add(-ExtractionActivityReconcileWindow), limit)
		if err != nil {
			return err
		}
		announced := make([]ids.UUID, 0, len(reads))
		for _, read := range reads {
			if err := logExtractionActivity(reannounceCtx(ctx, read), tx, read); err != nil {
				return err
			}
			announced = append(announced, read.ID)
			total++
		}
		if len(announced) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, markExtractionAnnouncedSQL, announced); err != nil {
			return fmt.Errorf("mark extraction readings announced: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// extractionReannounceActor is who a re-announcement is BY. The pass is what is
// speaking — the ledger row should say so — and the reading's requester rides
// as on-behalf-of, which is the same shape the document reader itself takes
// when it works a reading for somebody.
const extractionReannounceActor = "system:extraction_activity_reannounce"

// reannounceCtx puts the READING's own requester behind the pass's principal.
//
// This is the difference between a repair and a data loss. The write shape
// stamps the envelope actor from the context, and the projection derives who an
// occurrence belongs to from that actor — so a pass that announced under a bare
// system principal would refile every reading it repaired as workspace work,
// permanently, and the person whose reading it is would lose it from their own
// display at exactly the moment the repair fired.
//
// The principal is REPLACED rather than amended, so this does not depend on who
// called: a caller's own actor is not the authority for a re-announcement, and
// amending one would produce shapes no writer emits (a human on behalf of
// another human) that the projection refuses outright.
func reannounceCtx(ctx context.Context, read ExtractionRead) context.Context {
	// A requester the id grammar does not admit leaves on-behalf-of zero, and
	// the occurrence reconciles as workspace-scoped — which is what it already
	// was, since the original event carried the same unreadable actor.
	requester, _ := principal.HumanUserID(read.RequestedBy)
	return principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalSystem,
		ID:         extractionReannounceActor,
		UserID:     requester,
		OnBehalfOf: requester,
	})
}

// selectExtractionReads materializes the pass's whole batch before anything is
// announced: the announce writes to the same connection, and a partly-consumed
// pgx.Rows cannot share it.
func selectExtractionReads(ctx context.Context, tx pgx.Tx, since time.Time, limit int) ([]ExtractionRead, error) {
	// Split evenly, and never to zero: the two arms repair different failures
	// and a pass that could only ever reach one of them is half a repair.
	perArm := max(limit/2, 1)
	rows, err := tx.Query(ctx, reconcileExtractionSQL, since, perArm, perArm)
	if err != nil {
		return nil, fmt.Errorf("select extraction readings to reconcile: %w", err)
	}
	defer rows.Close()
	var out []ExtractionRead
	for rows.Next() {
		read, err := scanExtractionRead(rows)
		if err != nil {
			return nil, fmt.Errorf("scan extraction reading to reconcile: %w", err)
		}
		out = append(out, read)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read extraction readings to reconcile: %w", err)
	}
	return out, nil
}
