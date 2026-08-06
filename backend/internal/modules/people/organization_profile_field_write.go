// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// canonicalOrgColumn maps a profile field onto the organization column that
// actually holds the value (ADR-0085 / A130). Where a mapping exists, THE
// COLUMN IS THE VALUE and the sidecar row is only its receipt — so a
// correction has to move both, or the header keeps showing what the user just
// corrected and the write is a lie about having been accepted (PO-AC-N-1).
//
// registered_address is deliberately absent: it is the registry address, a
// different fact from the operating address in the six address columns, and
// collapsing them was never intended. Everything else in the vocabulary has no
// column at all and lives only in the sidecar.
func canonicalOrgColumn(field string) (string, bool) {
	switch field {
	case "display_name":
		return "display_name", true
	case "legal_name":
		return "legal_name", true
	case "industry":
		return "industry", true
	default:
		return "", false
	}
}

// ProfileFieldWriteInput carries a correction. Value is nil for a confirmation,
// which is the same act without a value change (PO-AC-N-3).
type ProfileFieldWriteInput struct {
	Value     *string
	IfVersion *int64
}

// UpdateOrganizationProfileField corrects a profile field's value.
func (s *Store) UpdateOrganizationProfileField(
	ctx context.Context, orgID ids.OrganizationID, field string, in ProfileFieldWriteInput,
) (crmcontracts.CompanyProfileField, error) {
	if in.Value == nil {
		return crmcontracts.CompanyProfileField{}, fmt.Errorf(
			"a correction carries a value; use the confirm operation to agree without changing one: %w",
			apperrors.ErrConflict)
	}
	return s.writeProfileField(ctx, orgID, field, in)
}

// ConfirmOrganizationProfileField records that a human read the claim and
// agreed. No value moves; the field goes from extracted to confirmed.
func (s *Store) ConfirmOrganizationProfileField(
	ctx context.Context, orgID ids.OrganizationID, field string, in ProfileFieldWriteInput,
) (crmcontracts.CompanyProfileField, error) {
	in.Value = nil
	return s.writeProfileField(ctx, orgID, field, in)
}

// writeProfileField is the one path both verbs take, so the provenance flip,
// the canonical-column write, the audit image and the event cannot diverge
// between correcting and confirming.
func (s *Store) writeProfileField(
	ctx context.Context, orgID ids.OrganizationID, field string, in ProfileFieldWriteInput,
) (crmcontracts.CompanyProfileField, error) {
	var out crmcontracts.CompanyProfileField
	// A profile field is an assertion about the organization, so it is the
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
		if err := ensureOrgReadable(ctx, tx, orgID); err != nil {
			return err
		}
		// The transaction's own clock, so every row this write stamps agrees
		// and a test can pin it without the store carrying a clock.
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return fmt.Errorf("read transaction time: %w", err)
		}
		before, err := readProfileFieldRow(ctx, tx, orgID, field)
		if err != nil {
			return err
		}

		p := storekit.NewPatch()
		if in.Value != nil {
			p.Set("value", before.Value, *in.Value)
		}
		// The machine's proposal is NOT overwritten: evidence_snippet,
		// source_url and confidence stay exactly as extracted, and the before
		// image below carries them into the audit trail (PO-AC-N-2). What
		// changes is who now stands behind the value.
		p.Set("source", before.Source, string(crmcontracts.CompanyProfileFieldSourceHuman))
		p.Set("verified_at", before.VerifiedAt, now)
		p.Set("verified_by", before.VerifiedBy, actor.UserID)

		if err := p.ApplyGuarded(ctx, tx, "organization_profile_field", before.ID, in.IfVersion); err != nil {
			return err
		}

		// The half that makes the correction real.
		if column, canonical := canonicalOrgColumn(field); canonical && in.Value != nil {
			if err := writeCanonicalOrgColumn(ctx, tx, orgID, column, *in.Value); err != nil {
				return err
			}
		}

		// The before image is the machine's claim in full — snippet, source and
		// confidence included — so the proposal survives the correction in the
		// audit trail even though the row now reads as human (PO-AC-N-2).
		auditID, err := storekit.Audit(ctx, tx, "update", "organization_profile_field",
			before.ID, before.auditImage(), p.After())
		if err != nil {
			return fmt.Errorf("audit organization profile field write: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID,
			crmcontracts.PublicEventOrganizationUpdated{
				ChangedFields: map[string]any{field: p.After()},
			}); err != nil {
			return fmt.Errorf("emit organization.updated: %w", err)
		}

		out, err = readProfileFieldWire(ctx, tx, orgID, field)
		return err
	})
	return out, err
}

// writeCanonicalOrgColumn moves the value the sidecar was only describing, and
// stamps the name provenance the rename recheck reads, so a corrected display
// name is treated as human-set exactly like one typed into the edit form.
func writeCanonicalOrgColumn(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, column, value string,
) error {
	// The column name comes from canonicalOrgColumn's closed switch, never from
	// caller input, so the interpolation cannot carry anything a request chose.
	tag, err := tx.Exec(ctx,
		`UPDATE organization SET `+column+` = $2 WHERE id = $1`, orgID, value)
	if err != nil {
		return fmt.Errorf("write canonical organization column: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

type profileFieldRow struct {
	ID              ids.UUID
	Value           string
	Source          string
	EvidenceSnippet *string
	SourceURL       *string
	Confidence      *float32
	VerifiedAt      *time.Time
	VerifiedBy      *ids.UUID
}

func (r profileFieldRow) auditImage() map[string]any {
	return map[string]any{
		"value": r.Value, "source": r.Source,
		"evidence_snippet": r.EvidenceSnippet, "source_url": r.SourceURL,
		"confidence":  r.Confidence,
		"verified_at": r.VerifiedAt, "verified_by": r.VerifiedBy,
	}
}

func readProfileFieldRow(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, field string,
) (profileFieldRow, error) {
	var r profileFieldRow
	err := tx.QueryRow(ctx, `
		SELECT id, value, source, evidence_snippet, source_url, confidence, verified_at, verified_by
		  FROM organization_profile_field
		 WHERE workspace_id = $1 AND organization_id = $2 AND field = $3`,
		workspaceID(ctx), orgID, field,
	).Scan(&r.ID, &r.Value, &r.Source, &r.EvidenceSnippet, &r.SourceURL, &r.Confidence,
		&r.VerifiedAt, &r.VerifiedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperrors.ErrNotFound
	}
	if err != nil {
		return r, fmt.Errorf("read organization profile field: %w", err)
	}
	return r, nil
}

func readProfileFieldWire(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, field string,
) (crmcontracts.CompanyProfileField, error) {
	var (
		pf           crmcontracts.CompanyProfileField
		fieldV, srcV string
	)
	err := tx.QueryRow(ctx, `
		SELECT field, value, source, captured_by, evidence_snippet, source_url, confidence,
		       retrieved_at, verified_at, verified_by, updated_at
		  FROM organization_profile_field
		 WHERE workspace_id = $1 AND organization_id = $2 AND field = $3`,
		workspaceID(ctx), orgID, field,
	).Scan(&fieldV, &pf.Value, &srcV, &pf.CapturedBy, &pf.EvidenceSnippet, &pf.SourceUrl,
		&pf.Confidence, &pf.RetrievedAt, &pf.VerifiedAt, &pf.VerifiedBy, &pf.UpdatedAt)
	if err != nil {
		return pf, fmt.Errorf("re-read organization profile field: %w", err)
	}
	pf.Field = crmcontracts.CompanyProfileFieldField(fieldV)
	pf.Source = crmcontracts.CompanyProfileFieldSource(srcV)
	return pf, nil
}
