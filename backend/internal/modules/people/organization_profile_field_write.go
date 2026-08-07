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

// ensureOrgWritable is ensureOrgReadable's write-side twin: same row-scope and
// existence hiding, plus the liveness the read deliberately does not require.
//
// The evidence of an archived company stays READABLE — that is the record of
// what was known when it was retired. Correcting it is a different act, and
// PATCH /organizations/{id} refuses it. Both evidence writers gate here so the
// refusal does not depend on whether the corrected field happens to have a
// canonical column behind it.
func ensureOrgWritable(ctx context.Context, tx pgx.Tx, id ids.OrganizationID) error {
	return auth.EnsureVisibleLive(ctx, tx, "organization", id.UUID)
}

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
	case fieldDisplayName:
		return fieldDisplayName, true
	case fieldLegalName:
		return fieldLegalName, true
	case fieldIndustry:
		return fieldIndustry, true
	default:
		return "", false
	}
}

// Audit-image keys the two evidence sidecars share. A correction's before image
// is the machine's whole claim, so both sidecars write the same shape and each
// key is spelled once. auditKeySource, auditKeySourceURL and companySourceHuman
// already exist in company.go and are reused rather than respelled here.
const (
	auditKeyValue           = "value"
	auditKeyEvidenceSnippet = "evidence_snippet"
	auditKeyConfidence      = "confidence"
	auditKeyVerifiedAt      = "verified_at"
	auditKeyVerifiedBy      = "verified_by"
)

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
		if err := ensureOrgWritable(ctx, tx, orgID); err != nil {
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
			p.Set(auditKeyValue, before.Value, *in.Value)
		}
		// The machine's proposal is NOT overwritten: evidence_snippet,
		// source_url and confidence stay exactly as extracted, and the before
		// image below carries them into the audit trail (PO-AC-N-2). What
		// changes is who now stands behind the value.
		p.Set(auditKeySource, before.Source, companySourceHuman)
		p.Set(auditKeyVerifiedAt, before.VerifiedAt, now)
		p.Set(auditKeyVerifiedBy, before.VerifiedBy, actor.UserID)

		// A receipt is not archivable: it has no archived_at, because it is
		// deleted with the organization it describes rather than retired on its
		// own. NoArchiveColumn is how that is spelled.
		if err := p.ApplyGuardedIn(ctx, tx, "organization_profile_field", before.ID,
			in.IfVersion, storekit.NoArchiveColumn); err != nil {
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

// writeCanonicalOrgColumn moves the value the sidecar was only describing.
//
// A correction here IS an organization edit, so it owes the record everything
// UpdateOrganization owes it — the same four obligations, or a correction
// becomes a back door that reaches the same columns with fewer rules:
//
//   - LIVE ONLY. PATCH /organizations/{id} refuses an archived record; without
//     the same filter, correcting its display_name through the receipt would
//     rewrite a record the ordinary path says is gone.
//   - THE VERSION MOVES, so another editor holding the organization's If-Match
//     is told the row changed under them. trg_organization_updated does that
//     for any UPDATE on the row, which is why nothing here sets it.
//   - name_source = 'human'. A human naming the company is the top of the
//     provenance lattice (ADR-0072/A118). Left unstamped, the next enrichment
//     run overwrites the correction as though no one had made it.
//   - THE RENAME RECHECK RUNS. A new name can collide with an existing company,
//     and the duplicate queue only learns about it if this asks.
func writeCanonicalOrgColumn(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, column, value string,
) error {
	renamed := column == fieldDisplayName || column == fieldLegalName
	if renamed {
		// Workspace-wide, and taken before the row write for the ordering rule
		// on lockOrgNameWrites. Only a rename pays for it.
		if err := lockOrgNameWrites(ctx, tx); err != nil {
			return err
		}
	}
	// The column name comes from canonicalOrgColumn's closed switch, never from
	// caller input, so the interpolation cannot carry anything a request chose.
	// name_source rides the same statement rather than a second UPDATE: one
	// write, so the value and its provenance cannot land apart.
	nameSource := ""
	if renamed {
		nameSource = ", name_source = 'human'"
	}
	tag, err := tx.Exec(ctx,
		`UPDATE organization SET `+column+` = $2`+nameSource+`
		  WHERE id = $1 AND archived_at IS NULL`, orgID, value)
	if err != nil {
		return fmt.Errorf("write canonical organization column: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	if !renamed {
		return nil
	}
	editor, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	return recheckOrgNameForDuplicates(ctx, tx, orgID, editor)
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
		auditKeyValue: r.Value, auditKeySource: r.Source,
		auditKeyEvidenceSnippet: r.EvidenceSnippet, auditKeySourceURL: r.SourceURL,
		auditKeyConfidence: r.Confidence,
		auditKeyVerifiedAt: r.VerifiedAt, auditKeyVerifiedBy: r.VerifiedBy,
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
