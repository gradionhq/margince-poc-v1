// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The org-360 read side of the evidence sidecars: the confirmed facts
// (organization_fact) and profile fields (organization_profile_field) an
// organization carries, row-scoped and evidence-or-omit. Values are staged
// and accepted through the deep-read/enrich pipeline; these reads only
// surface what a human already confirmed. An org with none answers [] —
// an empty picture is honest, never an error.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ensureOrgReadable gates a row-scoped org sub-resource read: the row-scope
// clause plus an existence probe, so a non-existent or foreign org is
// existence-hidden (404) exactly like GetOrganization — never a misleading
// empty list. EnsureVisible alone skips the existence check when the
// caller's row-scope is unrestricted (its clause is empty).
func ensureOrgReadable(ctx context.Context, tx pgx.Tx, id ids.OrganizationID) error {
	if err := auth.EnsureVisible(ctx, tx, "organization", id.UUID); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM organization WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("probe organization existence: %w", err)
	}
	if !exists {
		return apperrors.ErrNotFound
	}
	return nil
}

// ListOrganizationFacts returns the org's confirmed facts, ordered for a
// stable grouped render (category, then field, then the per-value key).
func (s *Store) ListOrganizationFacts(ctx context.Context, id ids.OrganizationID) ([]crmcontracts.OrganizationFact, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []crmcontracts.OrganizationFact
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureOrgReadable(ctx, tx, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, category, field, value, value_key, source, captured_by,
			       evidence_snippet, source_url, confidence, updated_at
			  FROM organization_fact
			 WHERE workspace_id = $1 AND organization_id = $2
			 ORDER BY category, field, value_key, value`,
			workspaceID(ctx), id)
		if err != nil {
			return fmt.Errorf("list organization facts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				fact                    crmcontracts.OrganizationFact
				factID                  ids.UUID
				category, field, source string
			)
			if err := rows.Scan(&factID, &category, &field, &fact.Value, &fact.ValueKey,
				&source, &fact.CapturedBy, &fact.EvidenceSnippet, &fact.SourceUrl,
				&fact.Confidence, &fact.UpdatedAt); err != nil {
				return fmt.Errorf("scan organization fact: %w", err)
			}
			// The row's own id, so a brief sentence written from this fact can
			// cite something the reader can open.
			id := openapi_types.UUID(factID)
			fact.Id = &id
			fact.Category = crmcontracts.OrganizationFactCategory(category)
			fact.Field = crmcontracts.OrganizationFactField(field)
			fact.Source = crmcontracts.OrganizationFactSource(source)
			// Computed at read rather than stored: the rules are a judgment
			// about shape and will be tuned, and a stored verdict would keep
			// answering with the version of the rule that happened to run on
			// the day the row landed.
			if reason := factSuspectReason(fact.Field, fact.Value); reason != "" {
				suspect := crmcontracts.OrganizationFactSuspectReason(reason)
				fact.SuspectReason = &suspect
			}
			out = append(out, fact)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate organization facts: %w", err)
		}
		return nil
	})
	return out, err
}

// ListOrganizationProfileFields returns the org's confirmed profile fields
// (organization_profile_field). Items reuse CompanyProfileField — the
// table's field/source vocabulary is identical (migration 0099).
func (s *Store) ListOrganizationProfileFields(ctx context.Context, id ids.OrganizationID) ([]crmcontracts.CompanyProfileField, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []crmcontracts.CompanyProfileField
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureOrgReadable(ctx, tx, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, field, value, source, captured_by,
			       evidence_snippet, source_url, confidence, updated_at
			  FROM organization_profile_field
			 WHERE workspace_id = $1 AND organization_id = $2
			 ORDER BY field`,
			workspaceID(ctx), id)
		if err != nil {
			return fmt.Errorf("list organization profile fields: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				pf            crmcontracts.CompanyProfileField
				rowID         ids.UUID
				field, source string
			)
			if err := rows.Scan(&rowID, &field, &pf.Value, &source, &pf.CapturedBy,
				&pf.EvidenceSnippet, &pf.SourceUrl, &pf.Confidence, &pf.UpdatedAt); err != nil {
				return fmt.Errorf("scan organization profile field: %w", err)
			}
			// The row's own identity, so a dossier sentence written from this
			// field can cite something the reader can open.
			wireID := openapi_types.UUID(rowID)
			pf.Id = &wireID
			pf.Field = crmcontracts.CompanyProfileFieldField(field)
			pf.Source = crmcontracts.CompanyProfileFieldSource(source)
			out = append(out, pf)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate organization profile fields: %w", err)
		}
		return nil
	})
	return out, err
}
