// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// evidenceWrite names what differs between the two evidence sidecars a human
// can correct or confirm — the facts table and the profile-field table. Both
// are written in the one shape writeEvidence spells; everything a caller has
// to supply is here, and a third sidecar would supply the same five things.
type evidenceWrite[T any] struct {
	// table is the sidecar being written, for the guarded patch and the audit row.
	table string
	// archived is the write's archived-row filter, declared here beside its own
	// table so the claim "this table has no archived_at" stays checkable at the
	// call site rather than being inherited from the shared writer.
	archived storekit.ArchivedFilter
	// changedKey names the claim in the emitted organization.updated event.
	changedKey string
	// value is the corrected value, or nil for a confirmation — agreeing with a
	// claim changes who stands behind it, not what it says.
	value     *string
	ifVersion *int64
	// readBefore locates the row and returns the machine's claim in full, which
	// becomes the audit before-image.
	readBefore func(context.Context, pgx.Tx) (evidenceRow, error)
	// canonical moves the corrected value out of the sidecar and onto the
	// record it describes. Nil for a claim that lives only in the sidecar and
	// so has nothing to keep in step.
	canonical func(context.Context, pgx.Tx) error
	// readAfter re-reads the written row as its wire shape.
	readAfter func(context.Context, pgx.Tx) (T, error)
}

// writeEvidence is the one way a human correction or confirmation reaches an
// evidence sidecar (PO-AC-N-2).
//
// The machine's proposal is NOT overwritten: evidence_snippet, source_url and
// confidence stay exactly as extracted, and the before image carries them into
// the audit trail. What changes is who now stands behind the value.
//
// Neither sidecar is archivable: each is deleted with the organization it
// describes rather than retired on its own, which is why both declare
// NoArchiveColumn below.
func writeEvidence[T any](
	ctx context.Context, s *Store, orgID ids.OrganizationID, w evidenceWrite[T],
) (T, error) {
	var out T
	// A claim is an assertion about the organization, so it is the
	// organization's own update grant that governs it — there is no separate
	// object to grant, and inventing one would let a role edit a company's
	// industry through its receipt while being denied it on the record.
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return out, err
	}
	// A confirmation names the human who gave it; a principal with no user
	// cannot confirm anything on anyone's behalf.
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == (ids.UUID{}) {
		return out, fmt.Errorf(
			"confirming a claim records who agreed, and this call carries no user: %w",
			apperrors.ErrPermissionDenied)
	}

	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureOrgWritable(ctx, tx, orgID); err != nil {
			return err
		}
		// The transaction's own clock, so every row this write stamps agrees
		// and a test can pin it without the store carrying a clock.
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return fmt.Errorf("read transaction time: %w", err)
		}
		before, err := w.readBefore(ctx, tx)
		if err != nil {
			return err
		}

		p := storekit.NewPatch()
		if w.value != nil {
			p.Set(auditKeyValue, before.Value, *w.value)
		}
		p.Set(auditKeySource, before.Source, companySourceHuman)
		p.Set(auditKeyVerifiedAt, before.VerifiedAt, now)
		p.Set(auditKeyVerifiedBy, before.VerifiedBy, actor.UserID)
		// The row changes HANDS, not just provenance. Both enrichment upserts
		// decline to overwrite a row whose captured_by is a human, and they test
		// that column rather than `source` — so a verdict that moved source
		// alone was reclaimed by the next ordinary refresh, silently undoing
		// the correction a person had just made.
		capturedBy, err := storekit.CapturedBy(ctx)
		if err != nil {
			return err
		}
		p.Set(auditKeyCapturedBy, before.CapturedBy, capturedBy)

		if err := p.ApplyGuardedIn(ctx, tx, w.table, before.ID,
			w.ifVersion, w.archived); err != nil {
			return err
		}
		if w.canonical != nil {
			if err := w.canonical(ctx, tx); err != nil {
				return err
			}
		}

		auditID, err := storekit.Audit(ctx, tx, "update", w.table,
			before.ID, before.auditImage(), p.After())
		if err != nil {
			return fmt.Errorf("audit %s write: %w", w.table, err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID,
			crmcontracts.PublicEventOrganizationUpdated{
				ChangedFields: map[string]any{w.changedKey: p.After()},
			}); err != nil {
			return fmt.Errorf("emit organization.updated: %w", err)
		}

		out, err = w.readAfter(ctx, tx)
		return err
	})
	return out, err
}
