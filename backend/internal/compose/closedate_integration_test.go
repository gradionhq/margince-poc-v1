// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Close-date hygiene over real migrated Postgres (formulas §11/§12,
// B-E09.19/.20): the write layer rejects a past close date on an open
// deal at source; the forecast drops flagged deals out of
// Commit/Best-case; and the nightly corrector applies the A6 tiers —
// after a run, no open deal is left claiming a past close date
// (INV-CLOSE-PAST), and a provisional replacement stays excluded until
// a human confirms it through the approvals inbox.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/reporting"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// closeDateEnv wraps integration.Env with a two-open-stage pipeline whose
// probabilities pin the §11 tier judgments (20% early, 60% late).
type closeDateEnv struct {
	*integration.Env
	owner     *pgx.Conn
	pipeline  ids.UUID
	early     ids.UUID // 20%, position 0
	late      ids.UUID // 60%, position 1
	corrector *deals.CloseDateCorrector
	svc       *approvals.Service
}

func setupCloseDate(t *testing.T) *closeDateEnv {
	t.Helper()
	e := &closeDateEnv{Env: integration.Setup(t), owner: integration.OwnerConn(t)}
	e.pipeline = integration.SeedRow(t, e.owner,
		`INSERT INTO pipeline (id, workspace_id, name, is_default, position) VALUES ($1, $2, 'Hygiene', true, 0)`, e.WS)
	ctx := context.Background()
	for _, stage := range []struct {
		id          *ids.UUID
		position    int
		probability int
	}{{&e.early, 0, 20}, {&e.late, 1, 60}} {
		*stage.id = ids.NewV7()
		if _, err := e.owner.Exec(ctx,
			`INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
			 VALUES ($1, $2, $3, $4, $5, 'open', $6)`,
			*stage.id, e.WS, e.pipeline, fmt.Sprintf("Stage %d", stage.position), stage.position, stage.probability); err != nil {
			t.Fatal(err)
		}
	}
	quiet := slog.New(slog.NewTextHandler(os.Stderr, nil))
	e.svc = approvals.NewService(e.Pool)
	e.svc.WithEffect(deals.CloseDateCorrectionKind, closeDateConfirmEffect(e.svc, deals.NewStore(e.Pool)))
	e.corrector = deals.NewCloseDateCorrector(e.Pool, closeDateStager{svc: e.svc}, quiet)
	return e
}

// seedSweepDeal plants one deal through the owner connection — exactly
// the aged/migrated rows the write layer never saw and the nightly run
// must still clean.
func (e *closeDateEnv) seedSweepDeal(t *testing.T, name string, stage ids.UUID, category *string, closeInDays *int, lastActivityDaysAgo int) ids.UUID {
	t.Helper()
	var expectedClose *time.Time
	if closeInDays != nil {
		v := today().AddDate(0, 0, *closeInDays)
		expectedClose = &v
	}
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency,
		                   forecast_category, expected_close_date, last_activity_at, created_at, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, 10000, 'EUR', $6, $7,
		         now() - make_interval(days => $8), now() - interval '120 days', 'manual', 'human:x')`,
		id, e.WS, name, e.pipeline, stage, category, expectedClose, lastActivityDaysAgo); err != nil {
		t.Fatalf("seeding deal %q: %v", name, err)
	}
	return id
}

type sweptDeal struct {
	expectedClose *time.Time
	provisional   bool
	forecastCat   *string
}

func (e *closeDateEnv) readSwept(t *testing.T, id ids.UUID) sweptDeal {
	t.Helper()
	var d sweptDeal
	if err := e.owner.QueryRow(context.Background(),
		`SELECT expected_close_date, close_date_provisional, forecast_category FROM deal WHERE id = $1`,
		id).Scan(&d.expectedClose, &d.provisional, &d.forecastCat); err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *closeDateEnv) pendingCorrections(t *testing.T, dealID ids.UUID) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM approval WHERE kind = 'close_date_correction' AND target_entity_id = $1 AND status = 'pending'`,
		dealID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *closeDateEnv) runForecastReport(t *testing.T, ctx context.Context, body string) reportResultWire {
	t.Helper()
	handlers := reporting.NewHandlers(reporting.New(e.Pool, schemaFields))
	req := httptest.NewRequest(http.MethodPost, "/v1/reports/forecast", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	handlers.RunReport(rec, req, "forecast")
	var result reportResultWire
	decodeWire(t, rec, http.StatusOK, &result)
	return result
}

func today() time.Time {
	y, m, d := time.Now().UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func intp(v int) *int { return &v }

// --- B-E09.19(a): the write layer rejects the invalid state at source ---

func TestCloseDatePastRejectedOnOpenDealWrites(t *testing.T) {
	e := setupCloseDate(t)
	admin := e.Admin()
	yesterday := today().AddDate(0, 0, -1)
	tomorrow := today().AddDate(0, 0, 1)

	_, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Born invalid", PipelineID: ids.From[ids.PipelineKind](e.pipeline), StageID: ids.From[ids.StageKind](e.early), Source: "manual",
		ExpectedClose: &yesterday,
	})
	var pastClose *deals.PastCloseDateError
	if !errors.As(err, &pastClose) {
		t.Fatalf("create with a past close date → %v, want PastCloseDateError", err)
	}

	closingToday := today()
	d, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Closing today is fine", PipelineID: ids.From[ids.PipelineKind](e.pipeline), StageID: ids.From[ids.StageKind](e.early), Source: "manual",
		ExpectedClose: &closingToday,
	})
	if err != nil {
		t.Fatalf("create closing today: %v (overdue is strict <)", err)
	}

	if _, err := e.Deals.UpdateDeal(admin, ids.From[ids.DealKind](ids.UUID(d.Id)), deals.UpdateDealInput{ExpectedClose: &yesterday}); !errors.As(err, &pastClose) {
		t.Fatalf("update to a past close date → %v, want PastCloseDateError", err)
	}
	if _, err := e.Deals.UpdateDeal(admin, ids.From[ids.DealKind](ids.UUID(d.Id)), deals.UpdateDealInput{ExpectedClose: &tomorrow}); err != nil {
		t.Fatalf("update to tomorrow: %v", err)
	}
}

// --- B-E09.19(b): flagged deals drop out of Commit/Best-case (AC-F9) ---

func TestForecastExcludesFlaggedDealsFromCommitAndBestCase(t *testing.T) {
	e := setupCloseDate(t)

	e.seedSweepDeal(t, "Healthy commit", e.late, stringp("commit"), intp(30), 3)
	e.seedSweepDeal(t, "Overdue commit", e.late, stringp("commit"), intp(-10), 3)
	e.seedSweepDeal(t, "Dateless commit", e.late, stringp("commit"), nil, 3)
	provisional := e.seedSweepDeal(t, "Provisional best case", e.late, stringp("best_case"), intp(30), 3)
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE deal SET close_date_provisional = true WHERE id = $1`, provisional); err != nil {
		t.Fatal(err)
	}

	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	result := e.runForecastReport(t, ctx, `{"group_by":["forecast_category"]}`)

	counts := map[string]int64{}
	for _, row := range result.Rows {
		key, _ := row["forecast_category"].(string)
		counts[key] = wireInt(t, row, "deals")
	}
	if counts["commit"] != 1 {
		t.Errorf("commit deals = %d, want only the healthy one", counts["commit"])
	}
	if counts["best_case"] != 0 {
		t.Errorf("best_case deals = %d, want 0 — the provisional date must stay excluded", counts["best_case"])
	}
	if counts["slipped"] != 3 {
		t.Errorf("slipped deals = %d, want the overdue + dateless + provisional trio", counts["slipped"])
	}

	// The filter rides the same expression: asking for commit returns the
	// healthy deal alone, so a drill-through can never resurrect a
	// flagged deal into the number it was excluded from.
	filtered := e.runForecastReport(t, ctx, `{"filters":{"forecast_category":"commit"},"group_by":["forecast_category"]}`)
	if len(filtered.Rows) != 1 || wireInt(t, filtered.Rows[0], "deals") != 1 {
		t.Fatalf("filter commit → %+v, want exactly the one healthy deal", filtered.Rows)
	}
}

// --- B-E09.20: the A6 tiers ---

func TestCloseDateSweepAutoRollsClearOverdueActiveDeal(t *testing.T) {
	e := setupCloseDate(t)
	// Early stage (20%), plainly overdue, touched 3 days ago, no forecast
	// override → the §11 worked example's 🟢 case. Two open stages remain
	// from position 0, velocity falls back to 14 → today + 28.
	id := e.seedSweepDeal(t, "Slipped but alive", e.early, nil, intp(-12), 3)

	if err := e.corrector.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	swept := e.readSwept(t, id)
	want := today().AddDate(0, 0, 2*deals.CloseDateStageDays)
	if swept.expectedClose == nil || !swept.expectedClose.Equal(want) {
		t.Errorf("auto-rolled date = %v, want %s (2 stages × 14-day fallback)", swept.expectedClose, want.Format(time.DateOnly))
	}
	if swept.provisional {
		t.Error("🟢 auto-apply is final — the date must not be provisional")
	}
	if got := e.pendingCorrections(t, id); got != 0 {
		t.Errorf("🟢 tier staged %d approvals, want none (the rep is informed, not asked)", got)
	}

	// Reversibility: the audit row carries the exact before/after images.
	var before, after string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT before::text, after::text FROM audit_log
		 WHERE entity_type = 'deal' AND entity_id = $1 AND action = 'update'
		 ORDER BY occurred_at DESC LIMIT 1`, id).Scan(&before, &after); err != nil {
		t.Fatalf("no audit row for the overnight change: %v", err)
	}
	if !strings.Contains(before, "expected_close_date") || !strings.Contains(after, want.Format(time.DateOnly)) {
		t.Errorf("audit diff (before %s, after %s) does not carry the rollback images", before, after)
	}
}

func TestCloseDateSweepStagesProvisionalForForecastBearingDeal(t *testing.T) {
	e := setupCloseDate(t)
	// Explicit commit + late stage: overdue, active — never auto-final.
	id := e.seedSweepDeal(t, "Commit slipped", e.late, stringp("commit"), intp(-10), 3)

	if err := e.corrector.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	swept := e.readSwept(t, id)
	if swept.expectedClose == nil || swept.expectedClose.Before(today()) {
		t.Fatalf("provisional date = %v — INV-CLOSE-PAST must hold immediately", swept.expectedClose)
	}
	if !swept.provisional {
		t.Error("🟡 replacement must be provisional until a human confirms")
	}
	if swept.forecastCat == nil || *swept.forecastCat != "commit" {
		t.Errorf("forecast_category = %v, want the untouched commit override (the number moves by exclusion, not by edit)", swept.forecastCat)
	}
	if got := e.pendingCorrections(t, id); got != 1 {
		t.Fatalf("pending close_date_correction approvals = %d, want 1", got)
	}

	// Excluded from Commit while provisional (AC-F9): the commit filter
	// matches nothing, so the aggregate has no group row at all.
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	result := e.runForecastReport(t, ctx, `{"filters":{"forecast_category":"commit"}}`)
	if len(result.Rows) != 0 {
		t.Errorf("commit rows while provisional = %+v, want none", result.Rows)
	}

	// A second nightly run must not stack a duplicate proposal.
	if err := e.corrector.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.pendingCorrections(t, id); got != 1 {
		t.Errorf("after a second sweep, pending approvals = %d, want still 1", got)
	}
}

func TestCloseDateConfirmAppliesTheDateAndClearsProvisional(t *testing.T) {
	e := setupCloseDate(t)
	id := e.seedSweepDeal(t, "Confirm me", e.late, stringp("commit"), intp(-10), 3)
	if err := e.corrector.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	var approvalID ids.ApprovalID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT id FROM approval WHERE kind = 'close_date_correction' AND target_entity_id = $1 AND status = 'pending'`,
		id).Scan(&approvalID); err != nil {
		t.Fatalf("no staged correction to decide: %v", err)
	}

	human := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	if _, err := e.svc.Decide(human, approvalID, true, nil); err != nil {
		t.Fatalf("approve + effect: %v", err)
	}

	swept := e.readSwept(t, id)
	if swept.provisional {
		t.Error("confirmation must clear close_date_provisional")
	}
	if swept.expectedClose == nil || swept.expectedClose.Before(today()) {
		t.Errorf("confirmed date = %v, want the proposed future date", swept.expectedClose)
	}

	// Confirmed: the deal counts in Commit again.
	result := e.runForecastReport(t, human, `{"filters":{"forecast_category":"commit"}}`)
	if len(result.Rows) != 1 || wireInt(t, result.Rows[0], "deals") != 1 {
		t.Errorf("commit total after confirm = %+v, want the deal back", result.Rows)
	}
}

func TestCloseDateSweepDowngradesQuietDealWithoutRedatingForward(t *testing.T) {
	e := setupCloseDate(t)
	// Quiet 90 days, commit override, date still future but inside the
	// stalled window (unrealistic_stale) → 🔻: one forecast notch down,
	// the date untouched — the zombie guard.
	id := e.seedSweepDeal(t, "Gone quiet", e.late, stringp("commit"), intp(30), 90)
	originalDate := today().AddDate(0, 0, 30)

	if err := e.corrector.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	swept := e.readSwept(t, id)
	if swept.forecastCat == nil || *swept.forecastCat != "best_case" {
		t.Errorf("forecast_category = %v, want best_case (one notch down from commit)", swept.forecastCat)
	}
	if swept.expectedClose == nil || !swept.expectedClose.Equal(originalDate) {
		t.Errorf("date = %v, want the original %s — a quiet deal is never re-dated forward", swept.expectedClose, originalDate.Format(time.DateOnly))
	}
	if swept.provisional {
		t.Error("a future-dated quiet deal needs no provisional replacement")
	}
	if got := e.pendingCorrections(t, id); got != 1 {
		t.Errorf("🔻 must surface the gone-quiet review: pending = %d, want 1", got)
	}
}

func TestCloseDateSweepDowngradesQuietOverdueDealWithProvisionalDate(t *testing.T) {
	e := setupCloseDate(t)
	// Quiet AND overdue: the invariant forces a replacement date, but it
	// lands provisional and the category still notches down.
	id := e.seedSweepDeal(t, "Quiet and overdue", e.late, stringp("best_case"), intp(-20), 90)

	if err := e.corrector.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	swept := e.readSwept(t, id)
	if swept.forecastCat == nil || *swept.forecastCat != "pipeline" {
		t.Errorf("forecast_category = %v, want pipeline (one notch down from best_case)", swept.forecastCat)
	}
	if swept.expectedClose == nil || swept.expectedClose.Before(today()) {
		t.Errorf("date = %v — the invariant still demands a non-past date", swept.expectedClose)
	}
	if !swept.provisional {
		t.Error("the forced replacement on a quiet deal must be provisional, never an optimistic re-date")
	}
}

// AC-F9 / §12 rule 5, the hard invariant: whatever mix of tiers the
// night starts with, no open deal survives the run with a past close
// date — while closed deals keep their historical dates untouched.
func TestCloseDateSweepLeavesNoOpenDealWithPastCloseDate(t *testing.T) {
	e := setupCloseDate(t)
	e.seedSweepDeal(t, "Auto tier", e.early, nil, intp(-12), 3)
	e.seedSweepDeal(t, "Provisional tier", e.late, stringp("commit"), intp(-10), 3)
	e.seedSweepDeal(t, "Downgrade tier", e.late, stringp("commit"), intp(-20), 90)
	e.seedSweepDeal(t, "Dateless", e.late, stringp("commit"), nil, 3)
	won := e.seedSweepDeal(t, "Won long ago", e.late, nil, intp(-100), 3)
	// Amountless close: the deal_closed_fx CHECK only demands a frozen
	// rate when a closed deal carries money, which is beside this point.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE deal SET status = 'won', closed_at = now() - interval '100 days',
		   amount_minor = NULL, currency = NULL WHERE id = $1`, won); err != nil {
		t.Fatal(err)
	}

	if err := e.corrector.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	var openPast int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM deal WHERE workspace_id = $1 AND status = 'open' AND archived_at IS NULL
		   AND expected_close_date < current_date`, e.WS).Scan(&openPast); err != nil {
		t.Fatal(err)
	}
	if openPast != 0 {
		t.Errorf("%d open deal(s) survived the nightly run with a past close date — INV-CLOSE-PAST broken", openPast)
	}
	var openMissing int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM deal WHERE workspace_id = $1 AND status = 'open' AND archived_at IS NULL
		   AND expected_close_date IS NULL`, e.WS).Scan(&openMissing); err != nil {
		t.Fatal(err)
	}
	if openMissing != 0 {
		t.Errorf("%d open deal(s) still dateless after the run", openMissing)
	}
	if got := e.readSwept(t, won); got.expectedClose == nil || !got.expectedClose.Before(today()) {
		t.Errorf("the won deal's historical date changed to %v — closed deals are never flagged", got.expectedClose)
	}
}

// A deal explicitly asked to wait is not "gone dark" (§11 edge case):
// the wait suppresses the 🔻 quiet branch, but its past date still takes
// the 🟡 provisional path — a paused deal must not claim a past date.
func TestCloseDateSweepWaitUntilSuppressesDowngradeButNotOverdue(t *testing.T) {
	e := setupCloseDate(t)
	id := e.seedSweepDeal(t, "Paused politely", e.early, nil, intp(-5), 90)
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE deal SET wait_until = current_date + 60 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	if err := e.corrector.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	swept := e.readSwept(t, id)
	if swept.forecastCat != nil {
		t.Errorf("forecast_category = %v, want untouched NULL — the wait suppresses the downgrade", swept.forecastCat)
	}
	if swept.expectedClose == nil || swept.expectedClose.Before(today()) || !swept.provisional {
		t.Errorf("(date, provisional) = (%v, %v) — the past date must be replaced provisionally", swept.expectedClose, swept.provisional)
	}
}
