// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// FactWriteInput carries a fact correction. Value is nil for a confirmation.
type FactWriteInput struct {
	Value     *string
	IfVersion *int64
}

// UpdateOrganizationFact corrects an extracted fact's value.
//
// Unlike a profile field, a fact has no canonical column anywhere — the whole
// claim lives in the sidecar — so the row IS the value and there is no second
// write to keep in step.
func (s *Store) UpdateOrganizationFact(
	ctx context.Context, orgID ids.OrganizationID, factKey string, in FactWriteInput,
) (crmcontracts.OrganizationFact, error) {
	if in.Value == nil {
		return crmcontracts.OrganizationFact{}, fmt.Errorf(
			"a correction carries a value; use the confirm operation to agree without changing one: %w",
			apperrors.ErrConflict)
	}
	return s.writeFact(ctx, orgID, factKey, in)
}

// ConfirmOrganizationFact records that a human read the fact and agreed.
func (s *Store) ConfirmOrganizationFact(
	ctx context.Context, orgID ids.OrganizationID, factKey string, in FactWriteInput,
) (crmcontracts.OrganizationFact, error) {
	in.Value = nil
	return s.writeFact(ctx, orgID, factKey, in)
}

func (s *Store) writeFact(
	ctx context.Context, orgID ids.OrganizationID, factKey string, in FactWriteInput,
) (crmcontracts.OrganizationFact, error) {
	var out crmcontracts.OrganizationFact
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return out, err
	}
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
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return fmt.Errorf("read transaction time: %w", err)
		}
		before, err := readFactRow(ctx, tx, orgID, factKey)
		if err != nil {
			return err
		}

		p := storekit.NewPatch()
		if in.Value != nil {
			p.Set(auditKeyValue, before.Value, *in.Value)
		}
		// The extraction's snippet, source and confidence stay put; the before
		// image carries them into the audit trail (PO-AC-N-2).
		p.Set(auditKeySource, before.Source, companySourceHuman)
		p.Set(auditKeyVerifiedAt, before.VerifiedAt, now)
		p.Set(auditKeyVerifiedBy, before.VerifiedBy, actor.UserID)

		// No archived_at on this sidecar either — the fact is deleted with its
		// organization, never retired alone.
		if err := p.ApplyGuardedIn(ctx, tx, "organization_fact", before.ID,
			in.IfVersion, storekit.NoArchiveColumn); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "update", "organization_fact",
			before.ID, before.auditImage(), p.After())
		if err != nil {
			return fmt.Errorf("audit organization fact write: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID,
			crmcontracts.PublicEventOrganizationUpdated{
				ChangedFields: map[string]any{factKey: p.After()},
			}); err != nil {
			return fmt.Errorf("emit organization.updated: %w", err)
		}

		out, err = readFactWire(ctx, tx, orgID, factKey)
		return err
	})
	return out, err
}

type factRow struct {
	ID              ids.UUID
	Value           string
	Source          string
	EvidenceSnippet *string
	SourceURL       *string
	Confidence      *float32
	VerifiedAt      *time.Time
	VerifiedBy      *ids.UUID
}

func (r factRow) auditImage() map[string]any {
	return map[string]any{
		auditKeyValue: r.Value, auditKeySource: r.Source,
		auditKeyEvidenceSnippet: r.EvidenceSnippet, auditKeySourceURL: r.SourceURL,
		auditKeyConfidence: r.Confidence,
		auditKeyVerifiedAt: r.VerifiedAt, auditKeyVerifiedBy: r.VerifiedBy,
	}
}

// splitFactKey reads the `<field>:<value_key>` identity the contract addresses
// a fact by (FactKey parameter) into the two columns that actually locate the
// row.
//
// NEITHER HALF IS ENOUGH ALONE. A multi-value field holds several rows, so
// `field` does not name one; and every company fact carries an empty value_key
// by the org_fact_value_key_cardinality check, so matching on value_key alone would
// make `phone`, `founded_year` and `contact_email` all answer to the same
// query and a correction would land on whichever row the scan reached first.
// The pair is exact: uq_org_fact is unique on (category, field, value_key), and
// org_fact_field_vocab gives each field exactly one category, so field
// determines category and (field, value_key) identifies the row.
//
// The split is on the FIRST colon, so a normalized value_key may contain one.
func splitFactKey(factKey string) (field, valueKey string, ok bool) {
	field, valueKey, ok = strings.Cut(factKey, ":")
	if !ok || field == "" {
		return "", "", false
	}
	return field, valueKey, true
}

// errMalformedFactKey refuses a key that names no row, rather than letting it
// fall through to a not-found that would read as "this fact once existed".
func errMalformedFactKey() error {
	return &values.ParseError{
		Field: "factKey", Code: "fact_key_malformed",
		Message: `a fact key is spelled <field>:<value_key>; a single-value fact ends in a bare colon, e.g. "phone:"`,
	}
}

func readFactRow(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, factKey string,
) (factRow, error) {
	var r factRow
	field, valueKey, ok := splitFactKey(factKey)
	if !ok {
		return r, errMalformedFactKey()
	}
	err := tx.QueryRow(ctx, `
		SELECT id, value, source, evidence_snippet, source_url, confidence, verified_at, verified_by
		  FROM organization_fact
		 WHERE workspace_id = $1 AND organization_id = $2 AND field = $3 AND value_key = $4`,
		workspaceID(ctx), orgID, field, valueKey,
	).Scan(&r.ID, &r.Value, &r.Source, &r.EvidenceSnippet, &r.SourceURL, &r.Confidence,
		&r.VerifiedAt, &r.VerifiedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperrors.ErrNotFound
	}
	if err != nil {
		return r, fmt.Errorf("read organization fact: %w", err)
	}
	return r, nil
}

func readFactWire(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, factKey string,
) (crmcontracts.OrganizationFact, error) {
	var (
		f           crmcontracts.OrganizationFact
		id          ids.UUID
		category    string
		field, srcV string
	)
	keyField, valueKey, ok := splitFactKey(factKey)
	if !ok {
		return f, errMalformedFactKey()
	}
	err := tx.QueryRow(ctx, `
		SELECT id, category, field, value, value_key, source, captured_by,
		       evidence_snippet, source_url, confidence,
		       retrieved_at, verified_at, verified_by, updated_at
		  FROM organization_fact
		 WHERE workspace_id = $1 AND organization_id = $2 AND field = $3 AND value_key = $4`,
		workspaceID(ctx), orgID, keyField, valueKey,
	).Scan(&id, &category, &field, &f.Value, &f.ValueKey, &srcV, &f.CapturedBy,
		&f.EvidenceSnippet, &f.SourceUrl, &f.Confidence,
		&f.RetrievedAt, &f.VerifiedAt, &f.VerifiedBy, &f.UpdatedAt)
	if err != nil {
		return f, fmt.Errorf("re-read organization fact: %w", err)
	}
	wireID := openapi_types.UUID(id)
	f.Id = &wireID
	f.Category = crmcontracts.OrganizationFactCategory(category)
	f.Field = crmcontracts.OrganizationFactField(field)
	f.Source = crmcontracts.OrganizationFactSource(srcV)
	return f, nil
}
