// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The sender's own sign-off (core 0235).
//
// Always the CALLER's, never anybody else's, and there is no seat that widens
// that — not admin, not ops. A signature is the words a person signs their name
// with, and no role in this product has a reason to read or rewrite another
// member's. The gate is therefore the actor themselves rather than an RBAC
// object: `owner_id = the caller` is the whole rule, applied in every statement.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SignatureMaxRunes bounds a signature. What one is FOR is a name, a role and a
// way to reach the sender; past this it is a document riding on every message
// the person sends.
const SignatureMaxRunes = 2000

// EmailSignature is the caller's sign-off as the settings tab shows it.
type EmailSignature struct {
	// Body empty means unsigned, which is the state of every member who has
	// never written one — and, until this table existed, of everyone.
	Body      string
	UpdatedAt *time.Time
}

// GetMyEmailSignature reads the caller's own signature. A member who has never
// written one has no row, and that is not an error: an empty body is the honest
// answer and the send path treats it as "sign nothing".
func (s *Store) GetMyEmailSignature(ctx context.Context) (EmailSignature, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return EmailSignature{}, apperrors.ErrPermissionDenied
	}
	var out EmailSignature
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT body, updated_at FROM email_signature
			 WHERE owner_id = $1 AND archived_at IS NULL`, actor.UserID).
			Scan(&out.Body, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return EmailSignature{}, fmt.Errorf("people: reading the caller's email signature: %w", err)
	}
	return out, nil
}

// SaveMyEmailSignature upserts the caller's own row.
//
// An empty body CLEARS it: a member emptying the field means "send my mail
// unsigned", not "leave what was there". The row survives the clearing so the
// audit trail keeps both sides of the change.
func (s *Store) SaveMyEmailSignature(ctx context.Context, body string) (EmailSignature, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return EmailSignature{}, apperrors.ErrPermissionDenied
	}
	trimmed := strings.TrimSpace(body)
	if len([]rune(trimmed)) > SignatureMaxRunes {
		return EmailSignature{}, &SignatureTooLongError{Runes: len([]rune(trimmed))}
	}
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		before, err := readSignatureTx(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO email_signature (owner_id, body) VALUES ($1, $2)
			ON CONFLICT (owner_id) DO UPDATE SET body = $2, archived_at = NULL`,
			actor.UserID, trimmed); err != nil {
			return err
		}
		// A signature goes out under the sender's name on every message they
		// send, so a change to it is a change to how they are represented —
		// exactly the kind of fact that has to be answerable later.
		//
		// The TEXT stays out of the audit payload and out of the event: it is
		// the member's own words about themselves, a reader needs to know the
		// sign-off changed rather than what somebody's home address is, and the
		// audit log is read by more people than the signature is.
		auditID, err := storekit.Audit(ctx, tx, "update", "user", actor.UserID,
			map[string]any{"has_signature": before != ""},
			map[string]any{"has_signature": trimmed != ""})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, actor.UserID,
			crmcontracts.PublicEventEmailSignatureChanged{HasSignature: trimmed != ""})
	})
	if err != nil {
		return EmailSignature{}, fmt.Errorf("people: saving the caller's email signature: %w", err)
	}
	return s.GetMyEmailSignature(ctx)
}

// SignatureFor reads one user's signature for the send path.
//
// Separate from the Get above because the caller is different: that one answers
// a settings tab about the person reading it, this one answers the mailer about
// the person SENDING. Both resolve the same row; neither lets one member read
// another's, because the send path only ever asks about its own authenticated
// sender.
func (s *Store) SignatureFor(ctx context.Context, userID ids.UUID) (string, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID != userID {
		// A send signs with the SENDER's own sign-off. Asking for anybody
		// else's is a bug in the caller, not a permission to widen.
		return "", apperrors.ErrPermissionDenied
	}
	var body string
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var readErr error
		body, readErr = readSignatureTx(ctx, tx, userID)
		return readErr
	})
	if err != nil {
		return "", fmt.Errorf("people: reading the sender's email signature: %w", err)
	}
	return body, nil
}

func readSignatureTx(ctx context.Context, tx pgx.Tx, userID ids.UUID) (string, error) {
	var body string
	err := tx.QueryRow(ctx, `
		SELECT body FROM email_signature
		 WHERE owner_id = $1 AND archived_at IS NULL`, userID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return body, err
}

// SignatureTooLongError names the field and the bar, because a refusal that
// only says "too long" leaves the member counting characters by hand.
type SignatureTooLongError struct{ Runes int }

func (e *SignatureTooLongError) Error() string {
	return fmt.Sprintf("people: a signature is at most %d characters, this one is %d",
		SignatureMaxRunes, e.Runes)
}

// FieldFault names the offending input for EVERY surface, not just this
// module's HTTP transport. The MCP tool surface reaches this code through the
// datasource seam and never through that transport, so a refusal expressed only
// as a transport branch would reach an agent as an internal fault it was told
// to retry — retrying a signature that is too long forever.
func (e *SignatureTooLongError) FieldFault() (field, code, message string) {
	return "body", "too_long", e.Error()
}
