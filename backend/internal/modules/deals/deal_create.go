// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// Creating a deal: the store-opened entry point (CreateDeal), the
// caller-opened one (CreateDealTx, for a caller whose own write must land with
// the deal or not at all), the validation both settle before any transaction
// opens, and the transactional body they share. Split from deal.go to keep
// each file one concept under the 500-LOC cap; the update half lives in
// deal_update.go.

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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// CreateDealInput is one deal birth: the record's own fields plus the pipeline
// placement it is born on. CustomFields carries the request body's extra
// top-level keys.
type CreateDealInput struct {
	Name           string
	AmountMinor    *int64
	Currency       *string
	PipelineID     ids.PipelineID
	StageID        ids.StageID
	OrganizationID *ids.OrganizationID
	// PartnerOrganizationID and PartnerAttribution are the one fact the
	// schema stores as one: the deal_partner_attribution_pairing CHECK
	// rejects either half alone, so birthAttribution below settles them
	// together rather than letting a half-filled pair reach the insert.
	PartnerOrganizationID *ids.OrganizationID
	PartnerAttribution    *string
	ProjectID             *ids.ProjectID
	OwnerID               *ids.UserID
	// OwnerExact states that OwnerID — nil included — IS the decided owner,
	// so the actor fallback below must not run. The lead-qualify seam sets
	// it: the deal inherits the LEAD's owner, and an unassigned lead
	// qualifies into an unassigned deal rather than one silently owned by
	// whoever clicked Qualify.
	OwnerExact    bool
	ExpectedClose *time.Time
	Source        string
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (storekit customcolumns).
	CustomFields map[string]any
}

// CreateDeal inserts the deal, its first stage-history row, the audit row and
// the outbox event inside the store's own transaction — the ordinary CRUD
// entry point (Handlers→Store). Use CreateDealTx when the write must share a
// caller-opened transaction.
func (s *Store) CreateDeal(ctx context.Context, in CreateDealInput) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionCreate); err != nil {
		return crmcontracts.Deal{}, err
	}
	born, err := s.readyDealCreate(ctx, in)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	if !in.OwnerExact {
		in.OwnerID = storekit.OwnerOrActor(ctx, in.OwnerID)
	}
	active, err := s.activeColumns(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}

	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.createDealInTx(ctx, tx, in, born, active)
		return err
	})
	return out, err
}

// CreateDealTx is CreateDeal for a caller that already opened a transaction —
// one whose own write must land with this deal or not at all. Same gates in
// the same order; only the transaction is borrowed.
//
// Custom fields are refused rather than dropped: the catalog they are matched
// against is read in a transaction of its own, which is exactly the second
// connection a caller-opened seam must not take.
func (s *Store) CreateDealTx(ctx context.Context, tx pgx.Tx, in CreateDealInput) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionCreate); err != nil {
		return crmcontracts.Deal{}, err
	}
	if len(in.CustomFields) > 0 {
		return crmcontracts.Deal{}, ErrCustomFieldsNeedTheStoresOwnTransaction
	}
	born, err := s.readyDealCreate(ctx, in)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	if !in.OwnerExact {
		in.OwnerID = storekit.OwnerOrActor(ctx, in.OwnerID)
	}
	return s.createDealInTx(ctx, tx, in, born, nil)
}

// bornDeal is what a create settles before any transaction opens: who is
// stamped as having captured the row, and what it claims about the partner it
// names. Both travel to createDealInTx as decided values, so the insert has
// nothing left to resolve and no half-settled pair can reach the pairing CHECK.
type bornDeal struct {
	by string
	// attribution is nil exactly when the deal names no partner — the pair the
	// deal_partner_attribution_pairing CHECK admits populated together or not
	// at all.
	attribution *string
}

// readyDealCreate runs what a create settles BEFORE any transaction opens —
// the captured-by resolution, the money-pair invariant and the partner pair.
// Both entry points call it, so neither can drift from the other's validation.
func (s *Store) readyDealCreate(ctx context.Context, in CreateDealInput) (bornDeal, error) {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return bornDeal{}, err
	}
	// The money pair holds from birth (data-model §6): a deal with an
	// amount and no currency would silently skip the FX freeze at close
	// and trip the deal_closed_fx CHECK far from the cause. values.Money
	// is the one spelling of "a valid amount+currency" — the same rule
	// the schema CHECKs repeat.
	if (in.AmountMinor == nil) != (in.Currency == nil) {
		return bornDeal{}, &AmountCurrencyPairError{Missing: missingMoneyHalf(in.AmountMinor == nil)}
	}
	if in.AmountMinor != nil {
		if _, err := values.NewMoney(*in.AmountMinor, *in.Currency); err != nil {
			return bornDeal{}, err
		}
	}
	attribution, err := birthAttribution(in)
	if err != nil {
		return bornDeal{}, err
	}
	return bornDeal{by: by, attribution: attribution}, nil
}

// birthAttribution settles what a newborn deal claims about the partner it
// names, refusing what it may not claim, under the same rules the update path
// holds one file over in applyPartnerAttributionPatch.
//
// The vocabulary is closed. An attribution naming no partner is refused rather
// than defaulted: there is nobody to attribute it to, and inventing a partner
// is worse than saying no. Naming a partner without an attribution means
// "sourced" — what the link meant for every row written before the column
// existed, so an older caller and a newer one say the same thing.
//
// A deal naming no partner carries no attribution, which is what the
// deal_partner_attribution_pairing CHECK requires: the columns are populated
// together or not at all. Settling this before any transaction opens is what
// earns the caller "you left out the partner" instead of a constraint
// violation from the database.
//
// The vocabulary is checked before the pairing, in that order, because the
// update path checks it in that order — the same body must earn the same
// refusal whichever verb carried it.
func birthAttribution(in CreateDealInput) (*string, error) {
	if in.PartnerAttribution != nil {
		if err := validPartnerAttribution(*in.PartnerAttribution); err != nil {
			return nil, err
		}
	}
	if in.PartnerOrganizationID == nil {
		if in.PartnerAttribution == nil {
			return nil, nil //nolint:nilnil // both halves empty IS the settled answer for a deal naming no partner — the pairing CHECK admits the pair populated together or not at all, and a sentinel here would be an error the caller must discard.
		}
		return nil, &PartnerAttributionUnpairedError{}
	}
	if in.PartnerAttribution != nil {
		return in.PartnerAttribution, nil
	}
	sourced := attributionSourced
	return &sourced, nil
}

// ensureBirthLinksVisible holds every row-scoped record a newborn deal points
// at to the caller's own row scope.
//
// An FK argument that names a row-scoped business record is a read of that
// record: embedding organization_id into a deal the caller will read back
// discloses the link, so the target must be visible under the caller's row
// scope — not merely same-workspace, which the composite FK already enforces.
// The partner link is the same kind of disclosure and carries the same gate.
//
// Owner references point at app_user, which carries no row scope: any workspace
// member may be an owner, so the FK check alone governs them.
func ensureBirthLinksVisible(ctx context.Context, tx pgx.Tx, in CreateDealInput) error {
	// A link the deal does not name is not a read, so it is left out rather
	// than checked as a zero id.
	var links []recordLink
	if in.OrganizationID != nil {
		links = append(links, recordLink{linkEntityOrganization, in.OrganizationID.UUID})
	}
	if in.PartnerOrganizationID != nil {
		links = append(links, recordLink{linkEntityOrganization, in.PartnerOrganizationID.UUID})
	}
	if in.ProjectID != nil {
		links = append(links, recordLink{linkEntityProject, in.ProjectID.UUID})
	}
	for _, link := range links {
		if err := auth.EnsureLinkTarget(ctx, tx, link.entity, link.id); err != nil {
			return err
		}
	}
	return nil
}

// recordLink is one row-scoped record a deal points at, named by the entity the
// visibility gate knows it as.
type recordLink struct {
	entity string
	id     ids.UUID
}

// The entities a deal's birth links point at, as the visibility gate names
// them. Both the customer and the partner are organizations.
const (
	linkEntityOrganization = "organization"
	linkEntityProject      = "project"
)

// createDealInTx guards the birth invariants (open stage, future close,
// visible organization), inserts the deal with its first stage-history
// row, and runs the write shape — all inside the caller's transaction.
func (s *Store) createDealInTx(ctx context.Context, tx pgx.Tx, in CreateDealInput, born bornDeal, active []fieldcatalog.Column) (crmcontracts.Deal, error) {

	if err := ensureOpenBirthStage(ctx, tx, in.StageID, in.PipelineID); err != nil {
		return crmcontracts.Deal{}, err
	}

	// INV-CLOSE-PAST (formulas §11): deals are born open, and an open
	// deal never claims a past close date — reject at source rather
	// than let the nightly corrector inherit a knowingly-invalid row.
	if err := s.rejectPastCloseDate(ctx, tx, in.ExpectedClose); err != nil {
		return crmcontracts.Deal{}, err
	}

	if err := ensureBirthLinksVisible(ctx, tx, in); err != nil {
		return crmcontracts.Deal{}, err
	}
	// Visible is not enough for the partner: it must actually BE one, or the
	// deal reads as credited and can never earn anything (the accrual prices
	// from the partner row's margin tier).
	if in.PartnerOrganizationID != nil {
		if err := s.installation.EnsurePartner(ctx, tx, *in.PartnerOrganizationID); err != nil {
			return crmcontracts.Deal{}, err
		}
	}

	id := ids.New[ids.DealKind]()
	cfCols, cfHolders, args := storekit.InsertFragments(active, in.CustomFields, []any{
		id, in.Name, in.AmountMinor, in.Currency, in.PipelineID, in.StageID,
		in.OrganizationID, in.PartnerOrganizationID, born.attribution,
		in.ProjectID, in.OwnerID, in.ExpectedClose, in.Source, born.by,
	})
	_, err := tx.Exec(ctx,
		`INSERT INTO deal (id, name, amount_minor, currency, pipeline_id, stage_id,
		                   organization_id, partner_org_id, partner_attribution,
		                   project_id, owner_id, expected_close_date, source, captured_by`+cfCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14`+cfHolders+`)`,
		args...)
	if err != nil {
		// Covers the remaining FKs (pipeline, owner); the stage/pipeline
		// pairing and the organization target were pre-checked above.
		if constraint, ok := storekit.CheckViolation(err); ok && constraint == dealProjectSameOrgConstraint {
			return crmcontracts.Deal{}, &DealProjectOrgMismatchError{}
		}
		if storekit.IsForeignKeyViolation(err) {
			return crmcontracts.Deal{}, apperrors.ErrNotFound
		}
		return crmcontracts.Deal{}, fmt.Errorf("insert deal: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_stage_history (deal_id, from_stage_id, to_stage_id, changed_by, amount_minor_at_change, currency_at_change)
		 VALUES ($1, NULL, $2, $3, $4, $5)`,
		id, in.StageID, born.by, in.AmountMinor, in.Currency); err != nil {
		return crmcontracts.Deal{}, fmt.Errorf("record stage history: %w", err)
	}

	auditID, err := storekit.Audit(ctx, tx, "create", "deal", id.UUID, nil, map[string]any{dealNameColumn: in.Name})
	if err != nil {
		return crmcontracts.Deal{}, fmt.Errorf("audit deal create: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventDealCreated{Name: in.Name}); err != nil {
		return crmcontracts.Deal{}, fmt.Errorf("emit deal.created: %w", err)
	}
	out, err := readDealForCaller(ctx, tx, id, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Deal{}, fmt.Errorf("read created deal: %w", err)
	}
	return out, nil
}
