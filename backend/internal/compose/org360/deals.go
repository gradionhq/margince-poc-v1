// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The deals section: the account's open pipeline, plus the two lifetime
// figures the header shows. Both are taken over the caller's deal row
// scope, so a total can never disclose a deal the list does not.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// openDealsWhere is the ONE spelling of "an open deal of this account that this
// caller may list", for a query that aliases deal as d.
//
// Both this section and the suggestion rules build their query around it. That is
// what makes the rules' guarantee structural rather than a claim two queries have
// to keep in step: a suggestion cannot name a deal the card would refuse to show,
// because a condition added here reaches both.
func openDealsWhere(orgPos int, dealScope string) string {
	return fmt.Sprintf(
		`WHERE d.organization_id = $%d AND d.status = 'open' AND d.archived_at IS NULL AND (%s)`,
		orgPos, dealScope)
}

// dealsSection reads the account's open deals plus the two lifetime
// figures the header shows. won_lifetime sums amount_minor_base — each
// deal's amount at its FROZEN close-time rate — so the figure never moves
// when today's FX does.
func dealsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time) (crmcontracts.Organization360Deals, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return crmcontracts.Organization360Deals{}, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT d.id, d.name, d.status, d.stage_id, s.name, d.amount_minor, d.currency,
		       d.expected_close_date, d.created_at, d.last_activity_at, d.wait_until
		FROM deal d
		LEFT JOIN stage s ON s.id = d.stage_id AND s.workspace_id = d.workspace_id
		%s
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT %d`, openDealsWhere(orgPos, dealScope), sectionLimit+1), args...)
	if err != nil {
		return crmcontracts.Organization360Deals{}, err
	}
	open, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.Organization360Deal, error) {
		var d crmcontracts.Organization360Deal
		var id, stageID ids.UUID
		var stageIDPtr *ids.UUID
		var status string
		var amountMinor *int64
		var currency *string
		var createdAt time.Time
		var expectedClose, lastActivityAt, waitUntil *time.Time
		// The wire type is a date, but the SCAN target is a time: pgx decodes
		// a binary DATE into time.Time and has no path into the contract's
		// Date wrapper, so scanning the field directly fails the whole 360 on
		// any account holding a dated deal. Every other reader of this column
		// spells it this way (deals/deal_read.go).
		if err := row.Scan(&id, &d.Name, &status, &stageIDPtr, &d.StageName, &amountMinor, &currency,
			&expectedClose, &createdAt, &lastActivityAt, &waitUntil); err != nil {
			return d, err
		}
		if expectedClose != nil {
			d.ExpectedCloseDate = &openapi_types.Date{Time: *expectedClose}
		}
		d.DealId = openapi_types.UUID(id)
		d.Status = crmcontracts.Organization360DealStatus(status)
		if stageIDPtr != nil {
			stageID = *stageIDPtr
			v := openapi_types.UUID(stageID)
			d.StageId = &v
		}
		if amountMinor != nil {
			d.Amount = &crmcontracts.Money{AmountMinor: amountMinor, Currency: currency}
		}
		d.Stalled = deals.IsStalled(status, createdAt, lastActivityAt, waitUntil, now)
		return d, nil
	})
	if err != nil {
		return crmcontracts.Organization360Deals{}, err
	}
	open, page := truncate(open)

	lifetime, lost, err := closedTotals(ctx, tx, orgID)
	if err != nil {
		return crmcontracts.Organization360Deals{}, err
	}
	return crmcontracts.Organization360Deals{
		Data:        open,
		Page:        page,
		WonLifetime: lifetime,
		LostCount:   lost,
	}, nil
}

// closedTotals sums won money and counts lost deals over the same row
// scope the open list uses — a total that included deals the caller cannot
// open would disclose their existence through arithmetic.
func closedTotals(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (crmcontracts.Money, int, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return crmcontracts.Money{}, 0, err
	}
	var wonMinor int64
	var lost int
	var baseCurrency string
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT coalesce(sum(d.amount_minor_base) FILTER (WHERE d.status = 'won'), 0)::bigint,
		       count(*) FILTER (WHERE d.status = 'lost'),
		       (SELECT base_currency FROM workspace WHERE id = d.workspace_id)
		FROM deal d
		WHERE d.organization_id = $%d AND d.archived_at IS NULL
		  AND d.status IN ('won','lost') AND (%s)
		GROUP BY d.workspace_id`, orgPos, dealScope), args...).Scan(&wonMinor, &lost, &baseCurrency)
	if errors.Is(err, pgx.ErrNoRows) {
		// No closed deal on this account yet: an honest zero in the
		// workspace's own currency, not a missing figure.
		return workspaceZero(ctx, tx)
	}
	if err != nil {
		return crmcontracts.Money{}, 0, err
	}
	return crmcontracts.Money{AmountMinor: &wonMinor, Currency: &baseCurrency}, lost, nil
}

func workspaceZero(ctx context.Context, tx pgx.Tx) (crmcontracts.Money, int, error) {
	var baseCurrency string
	if err := tx.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE id = $1`,
		storekit.MustWorkspace(ctx)).Scan(&baseCurrency); err != nil {
		return crmcontracts.Money{}, 0, fmt.Errorf("read workspace base currency: %w", err)
	}
	zero := int64(0)
	return crmcontracts.Money{AmountMinor: &zero, Currency: &baseCurrency}, 0, nil
}
