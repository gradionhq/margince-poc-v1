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
// immediate and reversible: the rows stay, their links stay, un-archiving
// restores them whole. Redaction is delayed by an undo window and nulls the
// content in place — the rows, their natural keys and their provenance survive
// as the tombstones that stop a replay from re-capturing what was just redacted.
// Neither stage deletes a row: a mistaken verdict must always leave something to
// recover from, and a hard delete would also let the same message land again
// tomorrow.
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
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// capturedMailCohort selects every connector-captured message from one address.
// The provenance predicate matters: a note or a meeting a human recorded about
// this person is the workspace's own record, not the sender's mail, and a
// verdict about inbound mail has no authority over it.
const capturedMailCohort = `
	  counterparty_email = $1 AND kind = 'email' AND captured_by LIKE 'connector:%'`

// HideCapturedNoiseTx archives every captured message from one address on the
// CALLER's transaction, so the hiding commits with the verdict that authorized
// it — a disposition reading `noise` can never be visible without its mail being
// hidden, nor the reverse. Reports how many messages it hid.
//
// Idempotent through the archived_at IS NULL guard: a replay, or a message a
// human archived first, is skipped rather than archived twice.
func (s *Store) HideCapturedNoiseTx(ctx context.Context, tx pgx.Tx, email string) (int, error) {
	rows, err := tx.Query(ctx, `
		UPDATE activity SET archived_at = now(), updated_at = now()
		 WHERE `+capturedMailCohort+` AND archived_at IS NULL
		RETURNING id`, email)
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
		// The same event a human archiving would raise, one per message. Hiding
		// is hiding however it was decided, and every consumer that drops an
		// archived row from a timeline, a digest or a count must react
		// identically — a machine-hidden message that stayed in yesterday's
		// digest would be the "noise is not shown" promise going unkept.
		if err := storekit.EmitEvent(ctx, tx, auditID, id, crmcontracts.PublicEventActivityArchived{}); err != nil {
			return 0, fmt.Errorf("activities: announcing the noise hide: %w", err)
		}
	}
	return len(hidden), nil
}

// RedactCapturedNoise nulls the content of one address's hidden mail once its
// undo window has passed. The rows, their links, their source keys and their
// provenance stay: what is destroyed is the message text, which is the thing a
// person whose mail was never wanted has an interest in not being retained.
// Reports how many messages it redacted.
//
// Runs on its own transaction — the sweep processes one address at a time so a
// fault costs one sender's redaction and never the whole pass, and re-running
// finishes what a crash interrupted.
func (s *Store) RedactCapturedNoise(ctx context.Context, email string) (int, error) {
	var redacted int
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Only what is already hidden, and only what still has content: the
		// first guard keeps a redaction from running ahead of its hide, the
		// second is the sweep's idempotence — a message whose content is already
		// gone is not redacted twice, so re-running the same window churns no
		// audit rows.
		rows, err := tx.Query(ctx, `
			UPDATE activity
			   SET subject = NULL, body = NULL, raw = NULL, updated_at = now()
			 WHERE `+capturedMailCohort+` AND archived_at IS NOT NULL
			   AND (subject IS NOT NULL OR body IS NOT NULL OR raw IS NOT NULL)
			RETURNING id`, email)
		if err != nil {
			return fmt.Errorf("activities: redacting noise-dispositioned mail: %w", err)
		}
		redactedIDs, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		if err != nil {
			return fmt.Errorf("activities: redacting noise-dispositioned mail: %w", err)
		}
		for _, id := range redactedIDs {
			// 'erase' is the closed vocabulary's verb for content destruction,
			// which is precisely what this is — narrower in scope than an
			// Art. 17 erasure, identical in kind.
			if _, err := storekit.Audit(ctx, tx, "erase", "activity", id, nil, nil); err != nil {
				return fmt.Errorf("activities: auditing the noise redaction: %w", err)
			}
		}
		redacted = len(redactedIDs)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return redacted, nil
}

// LinkCapturedMailTx links every captured message from one address to the person
// it turned out to belong to — the `real` verdict's mirror of the hide. The
// ledger row names one message, but the sender may have written several while
// the question was open, and all of them belong on that person's timeline.
//
// Conflict-free on replay: the link table's uniqueness makes a second run a
// no-op rather than a duplicate.
func (s *Store) LinkCapturedMailTx(ctx context.Context, tx pgx.Tx, personID ids.PersonID, email string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
		SELECT workspace_id, id, 'person', $2
		  FROM activity
		 WHERE `+capturedMailCohort+`
		ON CONFLICT DO NOTHING`, email, personID)
	if err != nil {
		return fmt.Errorf("activities: linking captured mail to its counterparty: %w", err)
	}
	return nil
}
