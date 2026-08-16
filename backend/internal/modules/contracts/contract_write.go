// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

// The contract write paths: create, patch, archive.

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	if err := ensureLinksVisible(ctx, tx, dealRef(in.DealID), projectRef(in.ProjectID)); err != nil {
		return crmcontracts.Contract{}, err
	}
	if err := ensureLinksShareOrganization(ctx, tx, in.OrganizationID.UUID,
		dealRef(in.DealID), projectRef(in.ProjectID)); err != nil {
		return crmcontracts.Contract{}, err
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
func (s *Store) UpdateContract(ctx context.Context, id ids.ContractID, in crmcontracts.UpdateContractRequest, ifVersion *int64) (crmcontracts.Contract, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionUpdate); err != nil {
		return crmcontracts.Contract{}, err
	}

	var out crmcontracts.Contract
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The patch is a write, so the row must first be visible as a read —
		// otherwise a caller learns a contract exists by patching it.
		existing, err := writableContract(ctx, tx, id, s.today())
		if err != nil {
			return err
		}
		// Naming a deal or a project is a read of it, on a PATCH exactly as on a
		// create. Without this a caller re-points a contract at a record it
		// cannot see — and because the organization arm of the visibility
		// predicate enforces capture privacy while the deal arm does not,
		// moving the anchor would strip that boundary from the row for good.
		if err := ensureLinksVisible(ctx, tx, uuidRef("deal", in.DealId), uuidRef("project", in.ProjectId)); err != nil {
			return err
		}
		if err := ensureLinksShareOrganization(ctx, tx, ids.UUID(existing.OrganizationId),
			uuidRef("deal", in.DealId), uuidRef("project", in.ProjectId)); err != nil {
			return err
		}
		patch := contractPatch(existing, in)
		if patch.Empty() {
			out = existing
			return nil
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
		existing, err := writableContract(ctx, tx, id, s.today())
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

// contractPatch turns the decoded body into a patch. The generated request
// struct IS the allowlist — a field it does not carry cannot be set, and a key
// the client misspelled is refused by the decoder before it reaches here, so
// there is no second list to keep in step with the contract.
func contractPatch(existing crmcontracts.Contract, in crmcontracts.UpdateContractRequest) *storekit.Patch {
	patch := storekit.NewPatch()
	if in.DealId != nil {
		patch.Set("deal_id", existing.DealId, *in.DealId)
	}
	if in.ProjectId != nil {
		patch.Set("project_id", existing.ProjectId, *in.ProjectId)
	}
	if in.ContractNumber != nil {
		patch.Set("contract_number", existing.ContractNumber, *in.ContractNumber)
	}
	if in.Title != nil {
		patch.Set("title", existing.Title, *in.Title)
	}
	if in.ValueMinor != nil {
		patch.Set("value_minor", existing.ValueMinor, *in.ValueMinor)
	}
	if in.Currency != nil {
		patch.Set("currency", existing.Currency, *in.Currency)
	}
	if in.ValueBasis != nil {
		patch.Set("value_basis", string(existing.ValueBasis), string(*in.ValueBasis))
	}
	if in.StartsOn != nil {
		patch.Set("starts_on", existing.StartsOn, in.StartsOn.Time)
	}
	if in.EndsOn != nil {
		patch.Set("ends_on", existing.EndsOn, in.EndsOn.Time)
	}
	if in.RenewalOn != nil {
		patch.Set("renewal_on", existing.RenewalOn, in.RenewalOn.Time)
	}
	if in.AutoRenew != nil {
		patch.Set("auto_renew", existing.AutoRenew, *in.AutoRenew)
	}
	if in.NoticePeriodDays != nil {
		patch.Set("notice_period_days", existing.NoticePeriodDays, *in.NoticePeriodDays)
	}
	if in.SignedOn != nil {
		patch.Set("signed_on", existing.SignedOn, in.SignedOn.Time)
	}
	return patch
}

// linkRef is one client-supplied reference to a row-scoped record: the table it
// names, and the id, when the request carried one.
type linkRef struct {
	table string
	id    *ids.UUID
}

func dealRef(id *ids.DealID) linkRef {
	if id == nil {
		return linkRef{table: "deal"}
	}
	return linkRef{table: "deal", id: &id.UUID}
}

func projectRef(id *ids.ProjectID) linkRef {
	if id == nil {
		return linkRef{table: "project"}
	}
	return linkRef{table: "project", id: &id.UUID}
}

// uuidRef names a link the patch body carried. An absent field is not a
// reference and is not checked; it leaves the column alone.
func uuidRef(table string, id *openapi_types.UUID) linkRef {
	if id == nil {
		return linkRef{table: table}
	}
	parsed := ids.UUID(*id)
	return linkRef{table: table, id: &parsed}
}

// ensureLinksVisible gates every client-supplied reference this write carries.
// Spelled once because create and patch must apply the same rule: the recurring
// defect in this tree is the second call site that forgets the first's gate.
func ensureLinksVisible(ctx context.Context, tx pgx.Tx, refs ...linkRef) error {
	for _, ref := range refs {
		if ref.id == nil {
			continue
		}
		if err := auth.EnsureLinkTarget(ctx, tx, ref.table, *ref.id); err != nil {
			return err
		}
	}
	return nil
}

// CrossOrganizationLinkError reports a deal or project that belongs to a
// different company than the contract does.
type CrossOrganizationLinkError struct{ Field string }

func (e *CrossOrganizationLinkError) Error() string {
	return "the " + strings.TrimSuffix(e.Field, "_id") + " belongs to a different company than this contract"
}

// ensureLinksShareOrganization refuses a contract whose deal or project belongs
// to another company.
//
// This is a VISIBILITY rule as much as a data-integrity one. The predicate that
// decides who may read a contract judges a deal-anchored contract by its DEAL
// alone, so pairing company A's contract with company B's deal would publish
// A's agreement to everyone who can see B — including through the events it
// emits. Two independent "can you see it" checks cannot catch that; only asking
// whether the two name the same company can.
func ensureLinksShareOrganization(ctx context.Context, tx pgx.Tx, orgID ids.UUID, refs ...linkRef) error {
	for _, ref := range refs {
		if ref.id == nil {
			continue
		}
		var linkedOrg ids.UUID
		//nolint:gosec // the table name is a package literal from dealRef/projectRef, never client input
		query := "SELECT organization_id FROM " + ref.table + " WHERE id = $1"
		err := tx.QueryRow(ctx, query, *ref.id).Scan(&linkedOrg)
		if errors.Is(err, pgx.ErrNoRows) {
			// EnsureLinkTarget already ran, so an absent row here means it was
			// archived or deleted in between; answer as it does.
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read %s organization: %w", ref.table, err)
		}
		if linkedOrg != orgID {
			return &CrossOrganizationLinkError{Field: ref.table + "_id"}
		}
	}
	return nil
}
