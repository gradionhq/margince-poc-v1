// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package commissions

// The commission store: the ledger's reads, and the row mapping every path
// shares.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// commissionObject is this record type's RBAC object name, spelled once.
const commissionObject = "commission"

// The ledger's closed lifecycle. An entry accrues when a deal is won, is
// approved by someone who owns the commercial decision, and is then paid.
// Void is the exit at any point, and it is always accompanied by a reversal
// row rather than an edit.
const (
	StatusAccrued  = "accrued"
	StatusApproved = "approved"
	StatusPaid     = "paid"
	StatusVoid     = "void"
)

// The two attributions an entry can be accrued under. Only sourced accrues
// today; influenced is carried so the ledger can say what it was accrued under
// if that ever changes, rather than leaving a row whose basis is unreadable.
const (
	AttributionSourced    = "sourced"
	AttributionInfluenced = "influenced"
)

// Store owns the commission_entry table.
type Store struct {
	// db binds the installation this store runs for.
	db *database.DB
}

// NewStore builds the commission store.
func NewStore(db *database.DB) *Store {
	return &Store{db: db}
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.db.Tx(ctx, fn)
}

// commissionColumns is the select list every read shares, in the order
// scanEntry expects.
const commissionColumns = `id, deal_id, partner_org_id, status, trigger_event_id,
	attribution_at_accrual, margin_tier_at_accrual, rate_bps,
	basis_amount_minor, currency, fx_rate_to_base, amount_minor,
	reversal_of, void_reason, captured_by, version, created_at, updated_at`

func scanEntry(row pgx.Row) (crmcontracts.CommissionEntry, error) {
	var e crmcontracts.CommissionEntry
	var id, dealID, partnerID ids.UUID
	var triggerEvent, reversalOf *ids.UUID
	var version int64
	if err := row.Scan(&id, &dealID, &partnerID, &e.Status, &triggerEvent,
		&e.AttributionAtAccrual, &e.MarginTierAtAccrual, &e.RateBps,
		&e.BasisAmountMinor, &e.Currency, &e.FxRateToBase, &e.AmountMinor,
		&reversalOf, &e.VoidReason, &e.CapturedBy, &version,
		&e.CreatedAt, &e.UpdatedAt); err != nil {
		return crmcontracts.CommissionEntry{}, err
	}
	e.Id = openapi_types.UUID(id)
	e.DealId = openapi_types.UUID(dealID)
	e.PartnerOrgId = openapi_types.UUID(partnerID)
	e.ReversalOf = uuidPtr(reversalOf)
	e.Version = &version
	return e, nil
}

// uuidPtr converts an optional scanned id to the wire's optional uuid.
func uuidPtr(v *ids.UUID) *openapi_types.UUID {
	if v == nil {
		return nil
	}
	out := openapi_types.UUID(*v)
	return &out
}

// GetCommissionEntry resolves one ledger row the caller may see.
func (s *Store) GetCommissionEntry(ctx context.Context, id ids.CommissionEntryID) (crmcontracts.CommissionEntry, error) {
	if err := auth.Require(ctx, commissionObject, principal.ActionRead); err != nil {
		return crmcontracts.CommissionEntry{}, err
	}
	var out crmcontracts.CommissionEntry
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readEntry(ctx, tx, id)
		return err
	})
	return out, err
}

func readEntry(ctx context.Context, tx pgx.Tx, id ids.CommissionEntryID) (crmcontracts.CommissionEntry, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	scope, err := VisibleClause(ctx, "", arg)
	if err != nil {
		return crmcontracts.CommissionEntry{}, err
	}
	where := storekit.SQLf("id = $%d", idPos)
	if scope != "" {
		where += " AND " + scope
	}

	e, err := scanEntry(tx.QueryRow(ctx,
		`SELECT `+commissionColumns+` FROM commission_entry WHERE `+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		// Existence-hiding: a row outside the caller's scope is indistinguishable
		// from one that does not exist.
		return crmcontracts.CommissionEntry{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.CommissionEntry{}, fmt.Errorf("read commission entry: %w", err)
	}
	return e, nil
}
