// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The scan_status vocabulary, spelled by the contract's Attachment schema
// and the 0070 CHECK constraint.
const (
	scanStatusScanning = string(crmcontracts.AttachmentScanStatusScanning)
	scanStatusClean    = string(crmcontracts.AttachmentScanStatusClean)
	scanStatusBlocked  = string(crmcontracts.AttachmentScanStatusBlocked)
)

// ErrScanPending refuses the download stream while the row is still
// 'scanning' — retryable; the handler maps it to 409 scan_pending. The
// metadata row itself stays disclosed.
var ErrScanPending = errors.New("activities: attachment scan is still pending")

// ErrAttachmentBlocked refuses the download stream for a quarantined row —
// terminal; the handler maps it to 409 attachment_blocked.
var ErrAttachmentBlocked = errors.New("activities: attachment is blocked by the virus scan")

// ErrInvalidScanVerdict reports a Scanner that returned something outside
// the verdict vocabulary; the row is left unchanged.
var ErrInvalidScanVerdict = errors.New("activities: a scan verdict must be clean or blocked")

// Scanner is the injectable virus-scan seam (RD-T05). Scan inspects the
// object at storageKey and returns "clean" or "blocked" — never "scanning",
// which is the row's own pre-verdict default. No real scanning product is
// integrated anywhere in this codebase (out of scope for V1);
// Store.MarkScanResult is the only caller, driven by tests and
// administration, never by a public HTTP endpoint.
type Scanner interface {
	Scan(ctx context.Context, storageKey string) (status string, err error)
}

// FakeScanner is the safe test/dev double for Scanner: it always returns
// the fixed Result it was constructed with, demonstrating the injection
// seam without pretending to scan anything.
type FakeScanner struct{ Result string }

var _ Scanner = FakeScanner{}

// Scan returns f.Result unconditionally — see FakeScanner's doc comment.
func (f FakeScanner) Scan(context.Context, string) (string, error) {
	return f.Result, nil
}

// MarkScanResult applies a Scanner's verdict to a live attachment: the
// only path off the 'scanning' default (a row never auto-transitions).
// Authority inherits from the parent like every attachment op — Update on
// the parent object type plus parent row visibility, with denial reading
// as not-found (existence-hiding). A verdict outside clean|blocked is
// refused with ErrInvalidScanVerdict and the row stays untouched. The
// applied verdict is an audited update carrying the status transition.
func (s *Store) MarkScanResult(ctx context.Context, id ids.UUID, scanner Scanner) (crmcontracts.Attachment, error) {
	var storageKey string
	if err := s.tx(ctx, func(tx pgx.Tx) error {
		var entityType string
		var entityID ids.UUID
		row := tx.QueryRow(ctx,
			`SELECT entity_type, entity_id, storage_key FROM attachment WHERE id = $1 AND archived_at IS NULL`, id)
		switch err := row.Scan(&entityType, &entityID, &storageKey); {
		case errors.Is(err, pgx.ErrNoRows):
			return apperrors.ErrNotFound
		case err != nil:
			return err
		}
		if err := requireParentOrHide(ctx, entityType, principal.ActionUpdate); err != nil {
			return err
		}
		return ensureAttachmentParentVisible(ctx, tx, entityType, entityID)
	}); err != nil {
		return crmcontracts.Attachment{}, err
	}

	// The scanner runs outside any transaction (it may reach the object
	// store or an external engine); the write below re-reads the row so a
	// concurrent archive between the two is an honest not-found.
	verdict, err := scanner.Scan(ctx, storageKey)
	if err != nil {
		return crmcontracts.Attachment{}, err
	}
	if verdict != scanStatusClean && verdict != scanStatusBlocked {
		return crmcontracts.Attachment{}, fmt.Errorf("%w: scanner returned %q", ErrInvalidScanVerdict, verdict)
	}

	var out crmcontracts.Attachment
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var before string
		row := tx.QueryRow(ctx,
			`SELECT scan_status FROM attachment WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, id)
		switch err := row.Scan(&before); {
		case errors.Is(err, pgx.ErrNoRows):
			return apperrors.ErrNotFound
		case err != nil:
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE attachment SET scan_status = $1 WHERE id = $2`, verdict, id); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "attachment", id,
			map[string]any{"scan_status": before},
			map[string]any{"scan_status": verdict}); err != nil {
			return err
		}
		att, err := readAttachment(ctx, tx, id)
		if err != nil {
			return err
		}
		out = att
		return nil
	})
	return out, err
}
