// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The forecast's own history: what a deal's amount and close date were, when
// they moved, and who moved them. It lives beside the stage history rather than
// inside it — see recordForecastMovement and the 0215 migration for why the two
// tables stay apart.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// forecastColumns are the fields a forecast reconstruction reads. A move in any
// of them is what deal_forecast_history exists to record.
var forecastColumns = []string{amountField, currencyField, closeDateField}

// recordForecastMovement snapshots the forecast fields when an update moves one
// of them without moving the stage.
//
// deal_stage_history answers "what did this deal look like when it entered this
// stage", and it is written on creation and on a move — nowhere else. So an
// amount revised in place and a close date slipped leave no trace at all, and
// the second of those is the single most common reason a real forecast moves. A
// reconstruction built from stage history alone reconciles over stage movement
// and omits the rest while presenting itself as the whole answer: a partial sum
// wearing the label of a total, which is what finance/formulas.go already
// refuses by hand elsewhere.
//
// It writes to its own table rather than adding an arm to deal_stage_history
// because five readers hold that table to its stated meaning — health.go reads
// max(changed_at) as "when did this deal last move", the automation previews
// COUNT rows as movements — and a row that is not a transition would make every
// edited deal look freshly moved. See the migration for the full argument.
//
// The values are the deal's state AFTER the change, which is what a
// reconstruction asking "what was the forecast as of date T" needs from the
// latest row at or before T. The patch decides WHETHER to write; it already
// compared old against new, so a close date cleared or set for the first time
// counts as movement exactly as a revision does.
func recordForecastMovement(ctx context.Context, tx pgx.Tx, id ids.DealID,
	current crmcontracts.Deal, in UpdateDealInput, after map[string]any,
) error {
	moved := false
	for _, column := range forecastColumns {
		if _, ok := after[column]; ok {
			moved = true
			break
		}
	}
	if !moved {
		return nil
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	amount, currency := current.AmountMinor, current.Currency
	if in.AmountMinor != nil {
		amount = in.AmountMinor
	}
	if in.Currency != nil {
		currency = in.Currency
	}
	// Carried as *time.Time rather than the contract's Date wrapper: this is
	// the value the driver encodes into a `date` column, and a wrapper it has
	// to be taught about is a second place the column's type is decided.
	var closeDate *time.Time
	if current.ExpectedCloseDate != nil {
		closeDate = &current.ExpectedCloseDate.Time
	}
	if in.ExpectedClose != nil {
		closeDate = in.ExpectedClose
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_forecast_history
		   (workspace_id, deal_id, changed_by, amount_minor_at_change, currency_at_change, close_date_at_change)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		storekit.MustWorkspace(ctx), id, by, amount, currency, closeDate); err != nil {
		return fmt.Errorf("record forecast history: %w", err)
	}
	return nil
}
