// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The run-history read (A72/ADR-0035 Am.1): one automation's firings of
// EVERY outcome, reconstructed from workflow_run. The linkage is the
// engine's own vocabulary — workflow_run.handler equals the automation's
// catalog key, and runKey suffixes the idempotency key with
// "@<automation id>" — so the history needs no schema of its own.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// runStatusByOutcome maps the contract's outcome vocabulary onto the
// workflow_run.status set (the two differ where the wire says what the
// user sees — "fired", "queued_for_approval" — and the row says what the
// engine did).
var runStatusByOutcome = map[string]string{
	"fired":               "applied",
	"failed":              "failed",
	"blocked":             "blocked",
	"skipped":             "skipped",
	"queued_for_approval": "requires_approval",
}

// runOutcomeByStatus is the inverse mapping; the status CHECK constraint
// (migration 0061) closes the domain, so the map is total over stored rows.
var runOutcomeByStatus = map[string]string{
	"applied":           "fired",
	"failed":            "failed",
	"blocked":           "blocked",
	"skipped":           "skipped",
	"requires_approval": "queued_for_approval",
}

// AutomationRunRecord is one firing of one automation instance, as the
// engine recorded it.
type AutomationRunRecord struct {
	ID           ids.UUID
	Status       string
	Planned      json.RawMessage
	Applied      json.RawMessage
	Detail       *string // the `error` column: why for failed/blocked/skipped, the staging pointer while parked
	TriggerEvent ids.UUID
	Tier         string // the parent automation's tier — the tier that fired
	CreatedAt    time.Time
}

// Outcome renders the record's status in the contract vocabulary.
func (r AutomationRunRecord) Outcome() string {
	return runOutcomeByStatus[r.Status]
}

// AutomationRunPage is one keyset page of run history, newest first.
type AutomationRunPage struct {
	Items      []AutomationRunRecord
	NextCursor string
	HasMore    bool
}

// ListRuns pages one automation's run history, newest first, optionally
// filtered to one outcome. A foreign or absent automation reads as
// absent (404), exactly like Get.
func (s *AutomationStore) ListRuns(ctx context.Context, id ids.AutomationID, cursor *string, limit *int, outcome *string) (AutomationRunPage, error) {
	if err := auth.Require(ctx, "automation", principal.ActionRead); err != nil {
		return AutomationRunPage{}, err
	}
	var statusFilter *string
	if outcome != nil {
		status, ok := runStatusByOutcome[*outcome]
		if !ok {
			return AutomationRunPage{}, &ParamError{Field: "outcome",
				Reason: "must be one of fired, failed, blocked, skipped, queued_for_approval"}
		}
		statusFilter = &status
	}
	n := storekit.ClampLimit(limit)

	var page AutomationRunPage
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var key, tier string
		err := tx.QueryRow(ctx,
			`SELECT key, tier FROM automation WHERE id = $1 AND archived_at IS NULL`, id).Scan(&key, &tier)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}

		// runKey suffixes "@<automation id>" and a UUID carries no LIKE
		// metacharacters, so the pattern matches exactly this instance.
		where := "handler = $1 AND idempotency_key LIKE '%@' || $2"
		args := []any{key, id.String()}
		if statusFilter != nil {
			args = append(args, *statusFilter)
			where += " AND status = $3"
		}
		if cursor != nil && *cursor != "" {
			c, err := storekit.DecodeCursor(*cursor)
			if err != nil {
				return err
			}
			args = append(args, c.CreatedAt, c.ID)
			where += storekit.SQLf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
		}
		rows, err := tx.Query(ctx, storekit.SQLf(`
			SELECT id, status, planned, applied, error, trigger_event, created_at
			FROM workflow_run WHERE %s
			ORDER BY created_at DESC, id DESC LIMIT %d`, where, n+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rec := AutomationRunRecord{Tier: tier}
			if err := rows.Scan(&rec.ID, &rec.Status, &rec.Planned, &rec.Applied,
				&rec.Detail, &rec.TriggerEvent, &rec.CreatedAt); err != nil {
				return err
			}
			page.Items = append(page.Items, rec)
		}
		return rows.Err()
	})
	if err != nil {
		return AutomationRunPage{}, err
	}
	if len(page.Items) > n {
		page.Items = page.Items[:n]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = storekit.EncodeCursor(last.CreatedAt, last.ID)
		page.HasMore = true
	}
	return page, nil
}
