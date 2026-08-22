// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The money a project's deals add up to, for the project page header.

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ProjectDealTotals is what the deals rolled up to one project are worth,
// in the installation's base currency.
//
// The won figure is amount_minor_base — each deal's amount at the rate frozen
// when it closed — so it never moves when today's FX does. The open figure
// cannot be read the same way: that generated column is null on every open
// deal, because the rate freezes on CLOSE (deal_advance), and summing it
// alone would price every open pipeline at nothing. So an open deal already
// in the base currency contributes its own amount, and an open deal in
// another currency contributes nothing until it closes — the same rule the
// company page's pipeline fold keeps (org360 baseValueOf), spelled in SQL by
// OpenDealBaseValueSQL.
type ProjectDealTotals struct {
	OpenMinor int64
	WonMinor  int64
	// Currency is the base currency both figures are in.
	Currency string
}

// ProjectDealTotalsTx sums the open and the won deals of a project over the
// caller's deal row scope, inside a caller-opened transaction. A total that
// counted a deal the caller's list would not show discloses that deal
// through arithmetic, which is why the scope clause is here and not only on
// the list.
func (s *Store) ProjectDealTotalsTx(ctx context.Context, tx pgx.Tx, id ids.ProjectID) (ProjectDealTotals, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return ProjectDealTotals{}, err
	}
	base, err := s.installation.BaseCurrency(ctx, tx)
	if err != nil {
		return ProjectDealTotals{}, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	basePos := arg(base)
	where := []string{storekit.SQLf("d.project_id = $%d", arg(id)), "d.archived_at IS NULL"}
	scope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return ProjectDealTotals{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	totals := ProjectDealTotals{Currency: base}
	// An ungrouped aggregate answers exactly one row whatever it counted, so
	// a project with no deal folds to an honest pair of zeros.
	err = tx.QueryRow(ctx, storekit.SQLf(`
		SELECT coalesce(sum(%s) FILTER (WHERE d.status = 'open'), 0)::bigint,
		       coalesce(sum(d.amount_minor_base) FILTER (WHERE d.status = 'won'), 0)::bigint
		FROM deal d
		WHERE %s`, OpenDealBaseValueSQL("d", storekit.SQLf("$%d", basePos)), strings.Join(where, " AND ")), args...).Scan(&totals.OpenMinor, &totals.WonMinor)
	if err != nil {
		return ProjectDealTotals{}, err
	}
	return totals, nil
}
