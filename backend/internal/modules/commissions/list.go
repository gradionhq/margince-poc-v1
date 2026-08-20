// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package commissions

// The ledger's enumerations: the page, and the open-liability summary.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ListInput narrows the ledger.
type ListInput struct {
	Cursor       *string
	Limit        *int
	PartnerOrgID *ids.OrganizationID
	DealID       *ids.DealID
	Status       *string
}

// List pages the ledger under the caller's row scope.
func (s *Store) List(ctx context.Context, in ListInput) (crmcontracts.CommissionEntryListResponse, error) {
	if err := auth.Require(ctx, commissionObject, principal.ActionRead); err != nil {
		return crmcontracts.CommissionEntryListResponse{}, err
	}
	var out crmcontracts.CommissionEntryListResponse
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = listTx(ctx, tx, in)
		return err
	})
	return out, err
}

func listTx(ctx context.Context, tx pgx.Tx, in ListInput) (crmcontracts.CommissionEntryListResponse, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }

	where, err := listPredicates(ctx, in, arg)
	if err != nil {
		return crmcontracts.CommissionEntryListResponse{}, err
	}

	limit := storekit.ClampLimit(in.Limit)
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s FROM commission_entry WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d`,
		commissionColumns, strings.Join(where, " AND "), arg(limit+1)), args...)
	if err != nil {
		return crmcontracts.CommissionEntryListResponse{}, fmt.Errorf("list commission entries: %w", err)
	}
	defer rows.Close()

	entries := make([]crmcontracts.CommissionEntry, 0, limit)
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return crmcontracts.CommissionEntryListResponse{}, fmt.Errorf("scan commission entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return crmcontracts.CommissionEntryListResponse{}, fmt.Errorf("read commission page: %w", err)
	}

	page := crmcontracts.PageInfo{}
	// One row beyond the page proves another page exists without a second
	// count query, which would answer a different question under concurrency.
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[len(entries)-1]
		next := storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))
		page.NextCursor = &next
		page.HasMore = true
	}
	return crmcontracts.CommissionEntryListResponse{Data: entries, Page: page}, nil
}

// listPredicates renders the WHERE arms a ledger enumeration carries: the
// caller's inherited scope first, then whatever they narrowed by.
func listPredicates(ctx context.Context, in ListInput, arg func(any) int) ([]string, error) {
	where := []string{"TRUE"}
	scope, err := VisibleClause(ctx, "", arg)
	if err != nil {
		return nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	if in.PartnerOrgID != nil {
		where = append(where, storekit.SQLf("partner_org_id = $%d", arg(*in.PartnerOrgID)))
	}
	if in.DealID != nil {
		where = append(where, storekit.SQLf("deal_id = $%d", arg(*in.DealID)))
	}
	if in.Status != nil {
		where = append(where, storekit.SQLf("status = $%d", arg(*in.Status)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		cursor, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)",
			arg(cursor.CreatedAt), arg(cursor.ID)))
	}
	return where, nil
}

// Summary answers what is owed, grouped by partner and status.
//
// Summed PER CURRENCY, never across one: adding EUR to VND produces a number
// that means nothing, and a liability figure nobody can act on is worse than
// no figure. A caller wanting one total converts deliberately.
func (s *Store) Summary(ctx context.Context) (crmcontracts.CommissionSummaryResponse, error) {
	if err := auth.Require(ctx, commissionObject, principal.ActionRead); err != nil {
		return crmcontracts.CommissionSummaryResponse{}, err
	}
	var out crmcontracts.CommissionSummaryResponse
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = summaryTx(ctx, tx)
		return err
	})
	return out, err
}

func summaryTx(ctx context.Context, tx pgx.Tx) (crmcontracts.CommissionSummaryResponse, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	// The same inherited scope the page carries: a total that summed rows the
	// caller cannot open would leak their value through the aggregate.
	scope, err := VisibleClause(ctx, "", arg)
	if err != nil {
		return crmcontracts.CommissionSummaryResponse{}, err
	}
	where := "TRUE"
	if scope != "" {
		where = scope
	}

	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT partner_org_id, status, currency, count(*), coalesce(sum(amount_minor), 0)
		   FROM commission_entry WHERE %s
		  GROUP BY partner_org_id, status, currency
		  ORDER BY partner_org_id, status, currency`, where), args...)
	if err != nil {
		return crmcontracts.CommissionSummaryResponse{}, fmt.Errorf("summarize commissions: %w", err)
	}
	defer rows.Close()

	out := crmcontracts.CommissionSummaryResponse{Data: []crmcontracts.CommissionSummaryRow{}}
	for rows.Next() {
		var row crmcontracts.CommissionSummaryRow
		var partner ids.UUID
		if err := rows.Scan(&partner, &row.Status, &row.Currency, &row.EntryCount, &row.AmountMinor); err != nil {
			return crmcontracts.CommissionSummaryResponse{}, fmt.Errorf("scan commission summary: %w", err)
		}
		row.PartnerOrgId = openapi_types.UUID(partner)
		out.Data = append(out.Data, row)
	}
	if err := rows.Err(); err != nil {
		return crmcontracts.CommissionSummaryResponse{}, fmt.Errorf("read commission summary: %w", err)
	}
	return out, nil
}
