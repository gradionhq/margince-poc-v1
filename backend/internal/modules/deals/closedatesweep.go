// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The nightly close-date corrector (formulas-and-rules §11, DECISIONS
// A6, B-E09.20): the enforcement half of INV-CLOSE-PAST. Every open
// deal the §11 assessment flags is corrected the same night on the A6
// risk tier — 🟢 a low-stakes clear-overdue date is rolled forward
// finally (reversible: the audit row carries before/after), 🟡 a
// forecast-bearing / missing / unrealistic date is replaced with a
// PROVISIONAL guess (the invariant holds instantly, the deal stays out
// of Commit) and a close_date_correction approval asks a human for the
// real date, 🔻 a deal that has gone quiet is downgraded one forecast
// notch instead of being optimistically re-dated. Follows the retention
// evaluator's shape: one pass over every live workspace, one audited
// transaction per corrected deal.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// closeDateBatch bounds how many deals one workspace pass corrects — a
// first run against a migrated backlog drains over successive nights.
const closeDateBatch = 200

// CloseDateCorrectionKind is the approvals staging kind the 🟡/🔻 tiers
// surface through; its decision grant lives in the approvals module and
// its confirm effect is injected at the composition root.
const CloseDateCorrectionKind = "close_date_correction"

// CorrectionStager is the approvals seam the composition root fills
// (a module never imports a sibling): stage a 🟡 confirm-the-real-date
// proposal, and ask whether one is already pending so a nightly sweep —
// whose proposed date moves with "today" — cannot stack duplicates.
type CorrectionStager interface {
	HasPendingCorrection(ctx context.Context, dealID ids.UUID) (bool, error)
	StageCorrection(ctx context.Context, dealID ids.UUID, targetVersion int64, summary string, proposal CloseDateCorrection) error
}

// CloseDateCorrection is the staged proposed_change payload: everything
// a human needs to confirm (or edit) the replacement date, and the
// confirm effect needs to apply it.
type CloseDateCorrection struct {
	DealID ids.UUID `json:"deal_id"`
	// ExpectedCloseDate is the proposed date, date-only wire form.
	ExpectedCloseDate string          `json:"expected_close_date"`
	PreviousCloseDate *string         `json:"previous_close_date"`
	Flags             []CloseDateFlag `json:"flags"`
	// Basis is the plain-language derivation of the proposed date — the
	// "no mystery number" duty (P6) applied to a guess.
	Basis string `json:"basis"`
}

// UnmarshalCloseDateCorrection decodes a staged (possibly human-edited)
// proposal back into the typed form the confirm effect applies.
func UnmarshalCloseDateCorrection(raw json.RawMessage) (CloseDateCorrection, error) {
	var c CloseDateCorrection
	if err := json.Unmarshal(raw, &c); err != nil {
		return CloseDateCorrection{}, fmt.Errorf("close_date_correction payload: %w", err)
	}
	if c.DealID.IsZero() {
		return CloseDateCorrection{}, errors.New("close_date_correction payload names no deal")
	}
	if _, err := time.Parse(time.DateOnly, c.ExpectedCloseDate); err != nil {
		return CloseDateCorrection{}, fmt.Errorf("close_date_correction payload date: %w", err)
	}
	return c, nil
}

// CloseDateCorrector drives the sweep; the worker ticks it nightly.
type CloseDateCorrector struct {
	pool   *pgxpool.Pool
	stager CorrectionStager
	log    *slog.Logger
	// now is the corrector's clock so the fixed-clock invariant test
	// ("no open deal survives the run with a past date") can pin a day.
	now func() time.Time
}

func NewCloseDateCorrector(pool *pgxpool.Pool, stager CorrectionStager, log *slog.Logger) *CloseDateCorrector {
	return &CloseDateCorrector{pool: pool, stager: stager, log: log, now: time.Now}
}

// Sweep is one pass over every live workspace. Like retention, the
// workspace list is bounded by fleet size, and one tenant's failure must
// not starve the rest.
func (c *CloseDateCorrector) Sweep(ctx context.Context) error {
	rows, err := c.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return err
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return err
	}
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		wsCtx = principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system:close-date"})
		wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
		if err := c.sweepWorkspace(wsCtx); err != nil {
			c.log.Error("close-date sweep: workspace pass failed", "workspace", wsID, "err", err)
		}
	}
	return nil
}

// closeDateCandidate is one open deal the SQL pre-filter surfaced; the
// pure §11 assessment decides the truth in Go.
type closeDateCandidate struct {
	id             ids.UUID
	name           string
	createdAt      time.Time
	lastActivityAt *time.Time
	waitUntil      *time.Time
	expectedClose  *time.Time
	provisional    bool
	forecastCat    *string
	pipelineID     ids.UUID
	winProbability int
	remainingOpen  int
}

func (c *CloseDateCorrector) sweepWorkspace(ctx context.Context) error {
	var tzName string
	var candidates []closeDateCandidate
	now := c.now().UTC()
	err := database.WithWorkspaceTx(ctx, c.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT timezone FROM workspace WHERE id = $1`,
			storekit.MustWorkspace(ctx)).Scan(&tzName); err != nil {
			return fmt.Errorf("read workspace timezone: %w", err)
		}
		// The pre-filter is a deliberate superset of the §11 flags — a
		// date inside the widest (stalled) window, missing, or still
		// provisional; anything beyond it cannot be flagged today.
		rows, err := tx.Query(ctx, `
			SELECT d.id, d.name, d.created_at, d.last_activity_at, d.wait_until,
			       d.expected_close_date, d.close_date_provisional, d.forecast_category,
			       d.pipeline_id, s.win_probability,
			       (SELECT count(*) FROM stage s2
			         WHERE s2.pipeline_id = d.pipeline_id AND s2.archived_at IS NULL
			           AND s2.semantic = 'open' AND s2.position >= s.position)
			FROM deal d
			JOIN stage s ON s.id = d.stage_id
			WHERE d.status = 'open' AND d.archived_at IS NULL
			  AND (d.expected_close_date IS NULL
			       OR d.expected_close_date <= (timezone($1, now()))::date + $2::int
			       OR d.close_date_provisional)
			ORDER BY d.created_at, d.id
			LIMIT $3`, tzName, StalledThresholdDays, closeDateBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cand closeDateCandidate
			if err := rows.Scan(&cand.id, &cand.name, &cand.createdAt, &cand.lastActivityAt,
				&cand.waitUntil, &cand.expectedClose, &cand.provisional, &cand.forecastCat,
				&cand.pipelineID, &cand.winProbability, &cand.remainingOpen); err != nil {
				return err
			}
			candidates = append(candidates, cand)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return fmt.Errorf("workspace timezone %q: %w", tzName, err)
	}

	velocities := map[ids.UUID]float64{}
	for _, cand := range candidates {
		velocity, known := velocities[cand.pipelineID]
		if !known {
			if velocity, err = c.stageVelocityDays(ctx, cand.pipelineID); err != nil {
				return fmt.Errorf("stage velocity for pipeline %s: %w", cand.pipelineID, err)
			}
			velocities[cand.pipelineID] = velocity
		}
		category := effectiveForecastCategory(cand.forecastCat, cand.winProbability)
		hygiene := CloseDateAssessment(CloseDateInput{
			Status:              "open",
			ExpectedClose:       cand.expectedClose,
			CreatedAt:           cand.createdAt,
			LastActivityAt:      cand.lastActivityAt,
			WaitUntil:           cand.waitUntil,
			StageWinProbability: cand.winProbability,
			RemainingOpenStages: cand.remainingOpen,
			InForecastCommit:    category == "commit" || category == "best_case",
			StageVelocityDays:   velocity,
		}, now, loc)
		if err := c.correct(ctx, cand, hygiene, category); err != nil {
			return fmt.Errorf("close-date correction on %s: %w", cand.id, err)
		}
	}
	return nil
}

// stageVelocityDays is §11's experience-informed pace: the workspace
// median duration of completed stage stints across won deals of the
// pipeline. Below the CLOSE_DATE_MIN_HISTORY floor the observation is
// noise, so zero is returned and the fold falls back to the default.
func (c *CloseDateCorrector) stageVelocityDays(ctx context.Context, pipelineID ids.UUID) (float64, error) {
	var wonDeals int
	var medianSeconds *float64
	err := database.WithWorkspaceTx(ctx, c.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			WITH stints AS (
				SELECT d.id AS deal_id,
				       extract(epoch FROM lead(h.changed_at) OVER (PARTITION BY h.deal_id ORDER BY h.changed_at, h.id) - h.changed_at) AS secs
				FROM deal_stage_history h
				JOIN deal d ON d.id = h.deal_id
				WHERE d.pipeline_id = $1 AND d.status = 'won' AND d.archived_at IS NULL
			)
			SELECT count(DISTINCT deal_id),
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY secs)
			FROM stints WHERE secs IS NOT NULL`, pipelineID).Scan(&wonDeals, &medianSeconds)
	})
	if err != nil {
		return 0, err
	}
	if wonDeals < CloseDateMinHistory || medianSeconds == nil || *medianSeconds <= 0 {
		return 0, nil
	}
	return *medianSeconds / 86400, nil
}

// effectiveForecastCategory is the §7 reading: the rep's explicit
// override wins; otherwise the stage probability derives the default
// (commit ≥ 90, best-case ≥ 50).
func effectiveForecastCategory(override *string, winProbability int) string {
	if override != nil {
		return *override
	}
	switch {
	case winProbability >= forecastCommitMinProb:
		return "commit"
	case winProbability >= lateStageMinProb:
		return "best_case"
	default:
		return "pipeline"
	}
}

// forecastDowngrade is the 🔻 notch: Commit→Best-case→Pipeline→Omitted,
// never below Omitted.
func forecastDowngrade(category string) string {
	switch category {
	case "commit":
		return "best_case"
	case "best_case":
		return "pipeline"
	default:
		return "omitted"
	}
}

// correct applies one deal's A6 tier. The write runs in its own audited
// transaction; the 🟡 staging follows it (Stage opens its own) — if the
// staging fails the provisional row simply re-enters the next sweep.
func (c *CloseDateCorrector) correct(ctx context.Context, cand closeDateCandidate, hygiene CloseDateHygiene, category string) error {
	if !hygiene.Flagged {
		if cand.provisional {
			// The date itself is clean (the sweep set it), but the human
			// has not confirmed it yet: keep the 🟡 surface alive if the
			// previous staging expired undecided.
			return c.ensureStaged(ctx, cand.id, 0, cand.name, CloseDateCorrection{
				DealID:            cand.id,
				ExpectedCloseDate: cand.expectedClose.Format(time.DateOnly),
				PreviousCloseDate: dateString(cand.expectedClose),
				Basis:             "provisional date from an earlier nightly correction, still awaiting confirmation",
			})
		}
		return nil
	}

	proposal := CloseDateCorrection{
		DealID:            cand.id,
		ExpectedCloseDate: hygiene.ProposedClose.Format(time.DateOnly),
		PreviousCloseDate: dateString(cand.expectedClose),
		Flags:             hygiene.Flags,
		Basis:             fmt.Sprintf("%d open stage(s) remaining × stage velocity", max(1, cand.remainingOpen)),
	}

	switch hygiene.Action {
	case CloseDateActionAutoApply:
		_, err := c.apply(ctx, cand, "auto_apply", func(p *storekit.Patch) {
			p.Set("expected_close_date", cand.expectedClose, *hygiene.ProposedClose)
			if cand.provisional {
				p.Set("close_date_provisional", true, false)
			}
		}, map[string]any{"flags": hygiene.Flags, "basis": proposal.Basis})
		return err

	case CloseDateActionProvisionalConfirm:
		version, err := c.apply(ctx, cand, "provisional_confirm", func(p *storekit.Patch) {
			p.Set("expected_close_date", cand.expectedClose, *hygiene.ProposedClose)
			if !cand.provisional {
				p.Set("close_date_provisional", false, true)
			}
		}, map[string]any{"flags": hygiene.Flags, "basis": proposal.Basis})
		if err != nil {
			return err
		}
		return c.ensureStaged(ctx, cand.id, version, cand.name, proposal)

	case CloseDateActionDowngradeAndReview:
		notched := forecastDowngrade(category)
		version, err := c.apply(ctx, cand, "downgrade_and_review", func(p *storekit.Patch) {
			p.Set("forecast_category", cand.forecastCat, notched)
			if hygiene.Provisional {
				// Only the invariant forces a date onto a quiet deal —
				// never an optimistic re-date on top of the downgrade.
				p.Set("expected_close_date", cand.expectedClose, *hygiene.ProposedClose)
				if !cand.provisional {
					p.Set("close_date_provisional", false, true)
				}
			}
		}, map[string]any{"flags": hygiene.Flags, "at_risk": true})
		if err != nil {
			return err
		}
		// The 🟡 review: gone quiet — still alive? A provisional date, if
		// any, rides the same proposal so confirming it also re-dates.
		review := proposal
		if !hygiene.Provisional {
			review.ExpectedCloseDate = cand.expectedClose.Format(time.DateOnly)
			review.Basis = "deal has gone quiet; confirm it is still alive — set a real date or mark it lost"
		}
		return c.ensureStaged(ctx, cand.id, version, cand.name, review)
	}
	return fmt.Errorf("close-date sweep: no executor for action %q", hygiene.Action)
}

// apply runs one tier's write shape: re-verify the deal is still open
// and live under a row lock, patch it, audit with the exact before/after
// diff (the reversibility the 🟢 tier promises), and emit deal.updated —
// all in one transaction. Returns the row's post-write version so a 🟡
// staging can bind to exactly what the human will see.
func (c *CloseDateCorrector) apply(ctx context.Context, cand closeDateCandidate, correction string, build func(*storekit.Patch), extra map[string]any) (int64, error) {
	var version int64
	err := database.WithWorkspaceTx(ctx, c.pool, func(tx pgx.Tx) error {
		// The candidate scan and this write are separate transactions:
		// a deal closed or archived in between must not be re-dated.
		lock, err := storekit.LockRow(ctx, tx, "deal", cand.id, storekit.LiveOnly)
		if errors.Is(err, apperrors.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM deal WHERE id = $1`, cand.id).Scan(&status); err != nil {
			return err
		}
		if DealStatus(status) != DealOpen {
			return nil
		}
		patch := storekit.NewPatch()
		build(patch)
		if err := patch.ApplyLocked(ctx, tx, lock); err != nil {
			return fmt.Errorf("apply %s patch: %w", correction, err)
		}
		if err := tx.QueryRow(ctx, `SELECT version FROM deal WHERE id = $1`, cand.id).Scan(&version); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "deal", cand.id, patch.Before(), patch.After())
		if err != nil {
			return fmt.Errorf("audit %s: %w", correction, err)
		}
		payload := map[string]any{"close_date_correction": correction}
		for field, v := range patch.After() {
			payload[field] = v
		}
		for k, v := range extra {
			payload[k] = v
		}
		if err := storekit.Emit(ctx, tx, auditID, "deal.updated", "deal", cand.id, payload); err != nil {
			return fmt.Errorf("emit %s: %w", correction, err)
		}
		return nil
	})
	return version, err
}

// ensureStaged stages the 🟡 confirm-the-real-date proposal unless one
// is already pending — the sweep's proposal moves with "today", so an
// identity check on the exact diff would stack duplicates nightly.
func (c *CloseDateCorrector) ensureStaged(ctx context.Context, dealID ids.UUID, targetVersion int64, name string, proposal CloseDateCorrection) error {
	pending, err := c.stager.HasPendingCorrection(ctx, dealID)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	if targetVersion == 0 {
		// The keep-alive path wrote nothing this pass; bind the staging
		// to the row's current version so redemption still detects skew.
		err := database.WithWorkspaceTx(ctx, c.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT version FROM deal WHERE id = $1`, dealID).Scan(&targetVersion)
		})
		if err != nil {
			return err
		}
	}
	summary := fmt.Sprintf("Confirm the real close date for %q (proposed %s)", name, proposal.ExpectedCloseDate)
	return c.stager.StageCorrection(ctx, dealID, targetVersion, summary, proposal)
}

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

// RunCloseDateSweep ticks the corrector on the worker's schedule, the
// same loop shape as the retention evaluator.
func RunCloseDateSweep(ctx context.Context, c *CloseDateCorrector, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.Sweep(ctx); err != nil {
			log.Error("close-date sweep: pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
