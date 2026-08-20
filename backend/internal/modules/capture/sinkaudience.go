// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// limitLinkLessAudience keeps a captured message that will link to NO record
// inside its participants. The row scope makes a link-less activity
// workspace-shared — right for a hand-written note, wrong for a mailbox
// owner's correspondence with a sender the ladder just judged noise or
// infrastructure: nobody but the people on it has a reason to read it. Only
// the TERMINAL no-record outcomes qualify (captured without a counterparty,
// suppressed); a deferred sender may still be admitted and linked later, and
// a connector-supplied link keeps the row readable through that record.
func limitLinkLessAudience(ctx context.Context, tx pgx.Tx, id ids.ActivityID, rec connector.NormalizedRecord, decision counterpartyDecision) error {
	if decision.create || len(rec.Links) > 0 {
		return nil
	}
	if decision.traceOutcome != TraceCaptured && decision.traceOutcome != TraceSuppressed {
		return nil
	}
	// The row was inserted in this transaction, so the only way it is not
	// exactly one row is a bug in the insert above; saying so is cheaper than
	// a silent no-op.
	tag, err := tx.Exec(ctx, `UPDATE activity SET audience = $2 WHERE id = $1 AND audience <> $2`, id, audienceParticipants)
	if err != nil {
		return fmt.Errorf("capture: limiting a link-less message to its participants: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("capture: limiting a link-less message to its participants: %d rows, want 1", tag.RowsAffected())
	}
	return nil
}

// audienceParticipants is the activity audience a link-less captured message
// is held in (platform/auth ActivityContentClause names the arms).
const audienceParticipants = "participants"

// capturedAudience answers the audience a freshly captured activity is born
// with. Mail sharing ON (the default) births an email workspace-readable —
// the point of capturing into a shared CRM. Switched OFF, an email is held to
// its participants and the capturing mailbox owner from the moment it lands;
// the setting moves the default for NEW mail only, and non-mail kinds
// (meetings, channel messages) keep the workspace default either way.
func capturedAudience(ctx context.Context, tx pgx.Tx, kind string) (string, error) {
	if kind != "email" {
		return audienceWorkspace, nil
	}
	sharing, err := settings.ApplyTx(ctx, tx, MailSharing)
	if err != nil {
		return "", fmt.Errorf("capture: reading the mail-sharing posture: %w", err)
	}
	if sharing {
		return audienceWorkspace, nil
	}
	return audienceParticipants, nil
}

const audienceWorkspace = "workspace"
