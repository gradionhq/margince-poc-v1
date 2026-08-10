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
	"github.com/gradionhq/margince/backend/internal/modules/identity"
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
		       -- Read as text, then parsed below. pgx decodes a bare DATE into
		       -- its own type and refuses the contract's Date wrapper, so the
		       -- scan fails at RUNTIME on any deal that names a close date —
		       -- and it 500s the whole company page, not just this card.
		       to_char(d.expected_close_date, 'YYYY-MM-DD'),
		       d.created_at, d.last_activity_at, d.wait_until
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
		var lastActivityAt, waitUntil *time.Time
		var closeOn *string
		if err := row.Scan(&id, &d.Name, &status, &stageIDPtr, &d.StageName, &amountMinor, &currency,
			&closeOn, &createdAt, &lastActivityAt, &waitUntil); err != nil {
			return d, err
		}
		if closeOn != nil {
			parsed, err := time.Parse(time.DateOnly, *closeOn)
			if err != nil {
				return d, fmt.Errorf("reading the deal's expected close date: %w", err)
			}
			d.ExpectedCloseDate = &openapi_types.Date{Time: parsed}
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
	// The basis is resolved here rather than selected alongside the sums: it
	// is one installation-wide value, not a per-row one, and it no longer
	// lives on a row this query can reach.
	baseCurrency, err := identity.BaseCurrencyOf(ctx, tx)
	if err != nil {
		return crmcontracts.Money{}, 0, err
	}
	var wonMinor int64
	var lost int
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT coalesce(sum(d.amount_minor_base) FILTER (WHERE d.status = 'won'), 0)::bigint,
		       count(*) FILTER (WHERE d.status = 'lost')
		FROM deal d
		WHERE d.organization_id = $%d AND d.archived_at IS NULL
		  AND d.status IN ('won','lost') AND (%s)
		GROUP BY d.workspace_id`, orgPos, dealScope), args...).Scan(&wonMinor, &lost)
	if errors.Is(err, pgx.ErrNoRows) {
		// No closed deal on this account yet: an honest zero in the
		// installation's own currency, not a missing figure.
		return installationZero(baseCurrency)
	}
	if err != nil {
		return crmcontracts.Money{}, 0, err
	}
	return crmcontracts.Money{AmountMinor: &wonMinor, Currency: &baseCurrency}, lost, nil
}

func installationZero(baseCurrency string) (crmcontracts.Money, int, error) {
	zero := int64(0)
	return crmcontracts.Money{AmountMinor: &zero, Currency: &baseCurrency}, 0, nil
}
