// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The noise disposition's two-stage effect on captured mail (ADR-0072/A118):
// hide it now, redact its content later. Both write the activity row, so both
// live here — the capture verdict engine drives them and never touches activity
// SQL.
//
// Both act on the SENDER, not on one message. A disposition is idempotent per
// address: the second and third mail from the same stranger join the open
// question rather than raising their own, so the ledger row names only the
// message that happened to arrive first. An effect keyed on that one id would
// hide message #1 and leave #2 and #3 on the timeline with their full bodies —
// "noise is not shown" defeated by sending two emails instead of one. The
// verdict decides whether this ADDRESS is a counterparty, so its effect covers
// every message that address sent.
//
// This is the ONE sanctioned hide-then-redact, and the shape matters. Hiding is
// immediate and recoverable, but un-archiving alone is not the recovery — the
// sweep would simply hide it again on the next tick. The recovery is to WRITE to
// the sender: correspondence is the T1 signal that they are a counterparty, and
// capture's noise scope stops applying to an address the workspace has replied
// to. Redaction is delayed by an undo window and nulls the content in place —
// the rows, their natural keys and their provenance survive as the tombstones
// that stop a replay from re-capturing what was just redacted. Neither stage
// deletes a row: a mistaken verdict must always leave something to recover from,
// and a hard delete would also let the same message land again tomorrow.
//
// Contrast the capture LABEL (capturelabel.go), which routes attention and
// changes nothing else. A noise VERDICT is a different authority: it decided the
// sender is not a counterparty at all, and it is confidence-floored precisely
// because it is allowed to hide things.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// HideCapturedNoiseTx archives the given captured messages on the CALLER's
// transaction, so the hiding commits with the verdict that authorized it — a
// disposition reading `noise` can never be visible without its mail being
// hidden, nor the reverse. Reports how many it hid.
//
// WHICH messages a noise disposition may touch is decided by the capture module,
// which owns the ledger and the scope rule; this takes ids and archives them.
// Idempotent through the archived_at IS NULL guard: a replay, or a message a
// human archived first, is skipped rather than archived twice.
func (s *Store) HideCapturedNoiseTx(ctx context.Context, tx pgx.Tx, activityIDs []ids.UUID) (int, error) {
	if len(activityIDs) == 0 {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `
		UPDATE activity SET archived_at = now(), updated_at = now()
		 WHERE id = ANY($1) AND archived_at IS NULL
		RETURNING id`, activityIDs)
	if err != nil {
		return 0, fmt.Errorf("activities: hiding noise-dispositioned mail: %w", err)
	}
	hidden, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return 0, fmt.Errorf("activities: hiding noise-dispositioned mail: %w", err)
	}
	for _, id := range hidden {
		// 'archive' is the audit vocabulary's own verb for exactly this, and the
		// vocabulary is a closed CHECK: a machine-decided hide is still an
		// archive, so it is recorded as one rather than as a private synonym.
		// The audit row carries no message content — naming the activity is
		// enough, and copying a body into the audit spine is what ADR-0072's
		// audit minimization removed from the capture path.
		auditID, err := storekit.Audit(ctx, tx, "archive", "activity", id, nil, nil)
		if err != nil {
			return 0, fmt.Errorf("activities: auditing the noise hide: %w", err)
		}
		// The same event a human archiving would raise, one per message.
		if err := storekit.EmitEvent(ctx, tx, auditID, id, crmcontracts.PublicEventActivityArchived{}); err != nil {
			return 0, fmt.Errorf("activities: announcing the noise hide: %w", err)
		}
	}
	return len(hidden), nil
}

// RedactCapturedNoiseTx nulls the content of the given hidden messages and drops
// the embeddings derived from them, on the CALLER's transaction — so the
// activity's text, its vectors, and (in the caller) the provider original behind
// it are destroyed together or not at all.
//
// It returns the ids it ACTUALLY redacted, which is not always what it was
// handed: a message a human un-archived since the sweep read its backlog keeps
// its content, and must therefore keep its original too. A destructive follow-up
// keyed on the proposed set rather than the done set would quietly destroy half
// of one.
//
// The embeddings matter as much as the text: an activity's subject and body are
// embedded when it is captured, so leaving the vector behind would let a
// similarity probe reconstruct what was just redacted — the residue is the
// content, in another shape. What survives is the row, its links, its source key
// and its provenance: the tombstone that stops a replay re-capturing what was
// just redacted.
func (s *Store) RedactCapturedNoiseTx(ctx context.Context, tx pgx.Tx, activityIDs []ids.UUID) ([]ids.UUID, error) {
	if len(activityIDs) == 0 {
		return nil, nil
	}
	// Two different questions, and conflating them is what made an earlier
	// version skip its own follow-up work. AUTHORITY to destroy is "this message
	// is still hidden" — a human who un-archived one during the undo window has
	// withdrawn that authority. Whether there is text left to null is a separate
	// matter: a message whose activity was already stripped but whose provider
	// original survives is unfinished work, and the caller still has to purge
	// that original. So the target set is the archived ones, and only the subset
	// that actually held content is audited as an erasure.
	rows, err := tx.Query(ctx, `
		WITH target AS (
		  SELECT id, (subject IS NOT NULL OR body IS NOT NULL OR raw IS NOT NULL) AS had_content
		    FROM activity
		   WHERE id = ANY($1) AND archived_at IS NOT NULL
		   FOR UPDATE
		), stripped AS (
		  UPDATE activity a
		     SET subject = NULL, body = NULL, raw = NULL, updated_at = now()
		    FROM target t
		   WHERE a.id = t.id AND t.had_content
		)
		SELECT id, had_content FROM target`, activityIDs)
	if err != nil {
		return nil, fmt.Errorf("activities: redacting noise-dispositioned mail: %w", err)
	}
	defer rows.Close()
	var done, destroyed []ids.UUID
	for rows.Next() {
		var id ids.UUID
		var hadContent bool
		if err := rows.Scan(&id, &hadContent); err != nil {
			return nil, fmt.Errorf("activities: redacting noise-dispositioned mail: %w", err)
		}
		done = append(done, id)
		if hadContent {
			destroyed = append(destroyed, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("activities: redacting noise-dispositioned mail: %w", err)
	}
	if len(destroyed) == 0 {
		return done, nil
	}
	// The vectors go with the text they were built from: an embedding of
	// redacted mail is that mail in another shape, still reachable by a
	// similarity probe.
	if _, err := tx.Exec(ctx, `
		DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = ANY($1)`, destroyed); err != nil {
		return nil, fmt.Errorf("activities: dropping the redacted mail's embeddings: %w", err)
	}
	for _, id := range destroyed {
		// 'erase' is the closed vocabulary's verb for content destruction, which
		// is precisely what this is — narrower in scope than an Art. 17 erasure,
		// identical in kind.
		if _, err := storekit.Audit(ctx, tx, "erase", "activity", id, nil, nil); err != nil {
			return nil, fmt.Errorf("activities: auditing the noise redaction: %w", err)
		}
	}
	return done, nil
}

// LinkCapturedMailTx links every captured message from one address to the person
// it turned out to belong to — the `real` verdict's mirror of the hide. The
// ledger row names one message, but the sender may have written several while
// the question was open, and all of them belong on that person's timeline.
//
// Only mail linked to nobody: a message already attached to a person belongs to
// that person's record, and a verdict about an unknown sender must not relabel
// it. Conflict-free on replay — the link table's uniqueness makes a second run a
// no-op rather than a duplicate.
func (s *Store) LinkCapturedMailTx(ctx context.Context, tx pgx.Tx, personID ids.PersonID, email string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
		SELECT a.workspace_id, a.id, 'person', $2
		  FROM activity a
		 WHERE a.counterparty_email = $1
		   AND a.kind = 'email' AND a.captured_by LIKE 'connector:%'
		   AND NOT EXISTS (
		     SELECT 1 FROM activity_link l
		      WHERE l.activity_id = a.id AND l.person_id IS NOT NULL)
		ON CONFLICT DO NOTHING`, email, personID)
	if err != nil {
		return fmt.Errorf("activities: linking captured mail to its counterparty: %w", err)
	}
	return nil
}
