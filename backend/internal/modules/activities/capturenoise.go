// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The noise disposition's two-stage effect on an activity (ADR-0072/A118): hide
// it now, redact its content later. Both write the activity row, so both live
// here — the capture verdict engine drives them and never touches activity SQL.
//
// This is the ONE sanctioned hide-then-redact, and the shape matters. Hiding is
// immediate and reversible: the row stays, its links stay, un-archiving restores
// it whole. Redaction is delayed by an undo window and nulls the content in
// place — the row, its natural key and its provenance survive as the tombstone
// that stops a replay from re-capturing what was just redacted. Neither stage
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
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// HideCapturedNoiseTx archives one captured activity on the CALLER's
// transaction, so the hiding commits with the verdict that authorized it — a
// disposition reading `noise` can never be visible without its mail being
// hidden, nor the reverse.
//
// Idempotent through the archived_at IS NULL guard: a replay, or an activity a
// human archived first, is a no-op rather than a second archive or an error.
//
// The audit row carries no message content — naming the activity is enough to
// explain the action, and copying a body into the audit spine is exactly what
// ADR-0072's audit minimization removed from the capture path.
func (s *Store) HideCapturedNoiseTx(ctx context.Context, tx pgx.Tx, id ids.ActivityID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE activity SET archived_at = now(), updated_at = now()
		 WHERE id = $1 AND archived_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("activities: hiding a noise-dispositioned activity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	// 'archive' is the audit vocabulary's own verb for exactly this, and the
	// vocabulary is a closed CHECK: a machine-decided hide is still an archive,
	// so it is recorded as one rather than as a private synonym.
	auditID, err := storekit.Audit(ctx, tx, "archive", "activity", id.UUID, nil, nil)
	if err != nil {
		return fmt.Errorf("activities: auditing the noise hide: %w", err)
	}
	// The same event a human archiving the activity would raise. Hiding is
	// hiding however it was decided, and every consumer that drops an archived
	// row from a timeline, a digest or a count must react identically — a
	// machine-hidden message that stayed in yesterday's digest would be exactly
	// the "noise is not shown" promise going unkept.
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventActivityArchived{}); err != nil {
		return fmt.Errorf("activities: announcing the noise hide: %w", err)
	}
	return nil
}

// RedactCapturedNoise nulls the content of one hidden activity once its undo
// window has passed. The row, its links, its source key and its provenance stay:
// what is destroyed is the message text, which is the thing a person whose mail
// was never wanted has an interest in not being retained.
//
// Runs on its own transaction — the redaction sweep processes rows one at a
// time so a fault costs one activity's redaction and never a whole batch, and
// re-running finishes what a crash interrupted.
func (s *Store) RedactCapturedNoise(ctx context.Context, id ids.ActivityID) (bool, error) {
	var redacted bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The guard is the redaction's own idempotence: a row whose content is
		// already gone is not redacted twice, and the sweep can be re-run over
		// the same window without churning audit rows.
		tag, err := tx.Exec(ctx, `
			UPDATE activity
			   SET subject = NULL, body = NULL, raw = NULL, updated_at = now()
			 WHERE id = $1 AND archived_at IS NOT NULL
			   AND (subject IS NOT NULL OR body IS NOT NULL OR raw IS NOT NULL)`, id)
		if err != nil {
			return fmt.Errorf("activities: redacting a noise-dispositioned activity: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		redacted = true
		// 'erase' is the closed vocabulary's verb for content destruction, which
		// is precisely what this is — narrower in scope than an Art. 17 erasure,
		// identical in kind.
		if _, err := storekit.Audit(ctx, tx, "erase", "activity", id.UUID, nil, nil); err != nil {
			return fmt.Errorf("activities: auditing the noise redaction: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return redacted, nil
}
