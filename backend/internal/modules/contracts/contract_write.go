// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

// The contract write paths: create, patch, archive.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// CreateContractInput is one agreement as a human recorded it. Status is
// absent by design: an agreement is born a draft and leaves that state only
// through an asserted transition.
type CreateContractInput struct {
	OrganizationID   ids.OrganizationID
	DealID           *ids.DealID
	ProjectID        *ids.ProjectID
	ContractNumber   *string
	Title            string
	ValueMinor       *int64
	Currency         *string
	ValueBasis       string
	StartsOn         *time.Time
	EndsOn           *time.Time
	RenewalOn        *time.Time
	AutoRenew        bool
	NoticePeriodDays *int
	SignedOn         *time.Time
	Source           string
}

// CreateContract records an agreement.
func (s *Store) CreateContract(ctx context.Context, in CreateContractInput) (crmcontracts.Contract, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionCreate); err != nil {
		return crmcontracts.Contract{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Contract{}, err
	}

	var out crmcontracts.Contract
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = createContractTx(ctx, tx, in, by, s.today())
		return err
	})
	return out, err
}

func createContractTx(ctx context.Context, tx pgx.Tx, in CreateContractInput, by string, asOf time.Time) (crmcontracts.Contract, error) {
	// Naming the counterparty is a read of it, and naming a deal is a read of
	// that deal — both are client-supplied references to row-scoped records, so
	// a caller may not hang an agreement off something it cannot see.
	if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.OrganizationID.UUID); err != nil {
		return crmcontracts.Contract{}, err
	}
	if in.DealID != nil {
		if err := auth.EnsureLinkTarget(ctx, tx, "deal", in.DealID.UUID); err != nil {
			return crmcontracts.Contract{}, err
		}
	}

	id := ids.New[ids.ContractKind]()
	_, err := tx.Exec(ctx,
		`INSERT INTO contract (id, organization_id, deal_id, project_id, contract_number, title,
		                       value_minor, currency, value_basis, starts_on, ends_on, renewal_on,
		                       auto_renew, notice_period_days, signed_on, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		id, in.OrganizationID, in.DealID, in.ProjectID, in.ContractNumber, in.Title,
		in.ValueMinor, in.Currency, in.ValueBasis, in.StartsOn, in.EndsOn, in.RenewalOn,
		in.AutoRenew, in.NoticePeriodDays, in.SignedOn, in.Source, by)
	if err != nil {
		if storekit.IsForeignKeyViolation(err) {
			return crmcontracts.Contract{}, apperrors.ErrNotFound
		}
		if constraint, ok := storekit.CheckViolation(err); ok {
			return crmcontracts.Contract{}, contractCheckError(constraint)
		}
		return crmcontracts.Contract{}, fmt.Errorf("insert contract: %w", err)
	}

	auditID, err := storekit.Audit(ctx, tx, "create", contractObject, id.UUID, nil,
		map[string]any{"title": in.Title, "organization_id": in.OrganizationID.UUID})
	if err != nil {
		return crmcontracts.Contract{}, fmt.Errorf("audit contract create: %w", err)
	}
	created := crmcontracts.PublicEventContractCreated{
		Title:          in.Title,
		OrganizationId: openapi_types.UUID(in.OrganizationID.UUID),
		Status:         StatusDraft,
		ValueBasis:     in.ValueBasis,
		ContractNumber: in.ContractNumber,
	}
	if in.DealID != nil {
		dealID := openapi_types.UUID(in.DealID.UUID)
		created.DealId = &dealID
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, created); err != nil {
		return crmcontracts.Contract{}, fmt.Errorf("emit contract.created: %w", err)
	}
	return readContract(ctx, tx, id, asOf)
}

// UpdateContract applies a partial patch. Status is absent by design: it moves
// through ChangeStatus, so a correction to a term can never silently activate
// an agreement.
func (s *Store) UpdateContract(ctx context.Context, id ids.ContractID, values map[string]any, ifVersion *int64) (crmcontracts.Contract, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionUpdate); err != nil {
		return crmcontracts.Contract{}, err
	}
	if len(values) == 0 {
		return s.GetContract(ctx, id)
	}

	var out crmcontracts.Contract
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The patch is a write, so the row must first be visible as a read —
		// otherwise a caller learns a contract exists by patching it.
		existing, err := readContract(ctx, tx, id, s.today())
		if err != nil {
			return err
		}
		patch, err := contractPatch(existing, values)
		if err != nil {
			return err
		}
		if err := patch.ApplyGuarded(ctx, tx, "contract", id.UUID, ifVersion); err != nil {
			if constraint, ok := storekit.CheckViolation(err); ok {
				return contractCheckError(constraint)
			}
			return fmt.Errorf("patch contract: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "update", contractObject, id.UUID, patch.Before(), patch.After())
		if err != nil {
			return fmt.Errorf("audit contract update: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID,
			crmcontracts.PublicEventContractUpdated{ChangedFields: patch.After()}); err != nil {
			return fmt.Errorf("emit contract.updated: %w", err)
		}
		out, err = readContract(ctx, tx, id, s.today())
		return err
	})
	return out, err
}

// ArchiveContract soft-deletes an agreement. The row and its history stay:
// deleting one would silently change whether an account ever counted as a
// customer and destroy the evidence behind a deal that was marked won.
func (s *Store) ArchiveContract(ctx context.Context, id ids.ContractID) error {
	if err := auth.Require(ctx, contractObject, principal.ActionDelete); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		existing, err := readContract(ctx, tx, id, s.today())
		if err != nil {
			return err
		}
		patch := storekit.NewPatch()
		patch.Set("archived_at", existing.ArchivedAt, time.Now().UTC())
		if err := patch.ApplyGuarded(ctx, tx, "contract", id.UUID, nil); err != nil {
			return fmt.Errorf("archive contract: %w", err)
		}
		auditID, err := storekit.Audit(ctx, tx, "archive", contractObject, id.UUID, patch.Before(), patch.After())
		if err != nil {
			return fmt.Errorf("audit contract archive: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID,
			crmcontracts.PublicEventContractArchived{OrganizationId: existing.OrganizationId}); err != nil {
			return fmt.Errorf("emit contract.archived: %w", err)
		}
		return nil
	})
}

// patchableColumns is the closed set a client patch may name. Status is absent
// deliberately (it moves through ChangeStatus), and so are the cancellation
// dates (they move through Cancel, which states the consequence in words).
var patchableColumns = map[string]bool{
	"deal_id": true, "project_id": true, "contract_number": true, "title": true,
	"value_minor": true, "currency": true, "value_basis": true,
	"starts_on": true, "ends_on": true, "renewal_on": true,
	"auto_renew": true, "notice_period_days": true, "signed_on": true,
}

// contractPatch turns a request body into a patch, refusing any column that is
// not a client's to set. An unknown key is a refusal rather than a silent drop:
// a caller who typed a field name wrong must learn that nothing happened.
func contractPatch(existing crmcontracts.Contract, values map[string]any) (*storekit.Patch, error) {
	patch := storekit.NewPatch()
	for column, value := range values {
		if !patchableColumns[column] {
			return nil, &ContractCheckError{Field: column,
				Reason: "this field is not editable here"}
		}
		patch.Set(column, priorValue(existing, column), value)
	}
	return patch, nil
}

// priorValue reads the column's current value for the audit diff. Only the
// columns patchableColumns admits are asked for.
func priorValue(c crmcontracts.Contract, column string) any {
	switch column {
	case "deal_id":
		return c.DealId
	case "project_id":
		return c.ProjectId
	case "contract_number":
		return c.ContractNumber
	case "title":
		return c.Title
	case "value_minor":
		return c.ValueMinor
	case "currency":
		return c.Currency
	case "value_basis":
		return string(c.ValueBasis)
	case "starts_on":
		return c.StartsOn
	case "ends_on":
		return c.EndsOn
	case "renewal_on":
		return c.RenewalOn
	case "auto_renew":
		return c.AutoRenew
	case "notice_period_days":
		return c.NoticePeriodDays
	case "signed_on":
		return c.SignedOn
	default:
		return nil
	}
}
