// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
		if err := ensureOrgReadable(ctx, tx, orgID); err != nil {
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
		p.Set(auditKeySource, before.Source, sourceHuman)
		p.Set(auditKeyVerifiedAt, before.VerifiedAt, now)
		p.Set(auditKeyVerifiedBy, before.VerifiedBy, actor.UserID)

		if err := p.ApplyGuarded(ctx, tx, "organization_fact", before.ID, in.IfVersion); err != nil {
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
		"evidence_snippet": r.EvidenceSnippet, "source_url": r.SourceURL,
		auditKeyConfidence: r.Confidence,
		auditKeyVerifiedAt: r.VerifiedAt, auditKeyVerifiedBy: r.VerifiedBy,
	}
}

// factKeyColumn is the stable per-value identity a fact is addressed by: a
// category can hold several values of the same field, so the key rather than
// the field name is what a correction names.
const factKeyColumn = "value_key"

func readFactRow(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, factKey string,
) (factRow, error) {
	var r factRow
	err := tx.QueryRow(ctx, `
		SELECT id, value, source, evidence_snippet, source_url, confidence, verified_at, verified_by
		  FROM organization_fact
		 WHERE workspace_id = $1 AND organization_id = $2 AND `+factKeyColumn+` = $3`,
		workspaceID(ctx), orgID, factKey,
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
	err := tx.QueryRow(ctx, `
		SELECT id, category, field, value, value_key, source, captured_by,
		       evidence_snippet, source_url, confidence,
		       retrieved_at, verified_at, verified_by, updated_at
		  FROM organization_fact
		 WHERE workspace_id = $1 AND organization_id = $2 AND `+factKeyColumn+` = $3`,
		workspaceID(ctx), orgID, factKey,
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
