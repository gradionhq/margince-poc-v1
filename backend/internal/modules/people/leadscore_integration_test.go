// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// "Explain This Score" against a real database (ADR-0105/A156).
//
// These run here rather than as unit tests because every claim they make is
// about SQL: that a breakdown is written in the same transaction as the
// score it explains, that a Commercial Judgement override lands in the
// series at all, and that the CHECK constraint holding the two numbers
// together accepts what the writers actually write.

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	openapitypes "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// newLeadScoreEnv gives a store and an admin context that may work leads.
func newLeadScoreEnv(t *testing.T) (context.Context, *Store) {
	t.Helper()
	ctx, store, _ := newLeadScoreEnvWithBase(t)
	return ctx, store
}

// newLeadScoreEnvWithBase is newLeadScoreEnv plus the seeded environment, for
// the tests that need a SECOND caller with a narrower row scope.
func newLeadScoreEnvWithBase(t *testing.T) (context.Context, *Store, *privacyEnv) {
	t.Helper()
	base := setupCapturePrivacy(t)
	ctx := principal.WithCorrelationID(
		principal.WithWorkspaceID(context.Background(), base.ws), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + base.admin.String(), UserID: base.admin,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"lead": {Create: true, Read: true, Update: true, Delete: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return ctx, base.store, base
}

func seedScoredLead(ctx context.Context, t *testing.T, store *Store) ids.LeadID {
	t.Helper()
	lead, _, err := store.CreateLead(ctx, CreateLeadInput{
		FullName: ptr("Jonas Petersen"),
		Email:    ptr("jonas@nordwind.example"),
		Title:    ptr("VP Sales"),
		Source:   "webform",
		Status:   "new",
	})
	if err != nil {
		t.Fatalf("seeding a lead: %v", err)
	}
	return ids.From[ids.LeadKind](ids.UUID(lead.Id))
}

// A lead is explainable the moment it exists: the create writes its fit
// score AND the breakdown that produced it, so a rep who opens a lead
// nobody has touched still sees why it scored what it did.
func TestACreatedLeadCarriesItsOwnExplanation(t *testing.T) {
	ctx, store := newLeadScoreEnv(t)
	leadID := seedScoredLead(ctx, t, store)

	out, err := store.ExplainLeadScore(ctx, leadID, ExplainLeadScoreInput{Limit: 10})
	if err != nil {
		t.Fatalf("explaining a fresh lead: %v", err)
	}
	if !out.Explained || out.Current == nil {
		t.Fatalf("a created lead should already be explained, got %+v", out)
	}
	if out.Current.Score != out.Score {
		t.Errorf("displayed score %d disagrees with the entry's %d", out.Score, out.Current.Score)
	}
	// VP Sales + webform: the two fit factors, and no behavioral ones yet.
	factors := *out.Current.Factors
	if len(factors) != 2 {
		t.Fatalf("want the two fit factors, got %+v", factors)
	}
	if out.Current.OverrideReason != nil {
		t.Errorf("a machine-computed score carries no override reason: %v", *out.Current.OverrideReason)
	}
}

func TestLeadReasonUsesTheCurrentTiedHistoryAndToleratesMalformedFactors(t *testing.T) {
	ctx, store := newLeadScoreEnv(t)
	leadID := seedScoredLead(ctx, t, store)
	if _, err := store.SetLeadManualSignal(ctx, leadID, SetLeadManualSignalInput{
		Factor: "employees", Band: "201+", SignalKind: "assumption", Reason: "team estimate",
	}); err != nil {
		t.Fatalf("adding a second score history entry: %v", err)
	}

	sharedTime := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if _, err := store.db.Pool().Exec(ctx,
		`UPDATE lead_score_history SET computed_at = $2 WHERE lead_id = $1`, leadID, sharedTime); err != nil {
		t.Fatalf("giving the history entries a shared timestamp: %v", err)
	}
	explanation, err := store.ExplainLeadScore(ctx, leadID, ExplainLeadScoreInput{Limit: 10})
	if err != nil || explanation.Current == nil || explanation.Current.Factors == nil {
		t.Fatalf("reading the current tied explanation: out=%+v err=%v", explanation, err)
	}
	wantReason := ""
	maxImpact := -1.0
	for _, factor := range *explanation.Current.Factors {
		if impact := math.Abs(float64(factor.Points)); impact > maxImpact {
			maxImpact = impact
			wantReason = factor.Factor
		}
	}
	lead, err := store.GetLead(ctx, leadID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("reading a lead with tied score history: %v", err)
	}
	if lead.ScoreReason == nil || *lead.ScoreReason != wantReason {
		t.Fatalf("score reason = %v, want current history factor %q", lead.ScoreReason, wantReason)
	}

	if _, err := store.db.Pool().Exec(ctx, `
		UPDATE lead_score_history
		   SET factors = '[{"factor":"broken","points":"not-a-number"}]'::jsonb
		 WHERE id = (SELECT id FROM lead_score_history WHERE lead_id = $1
		             ORDER BY computed_at DESC, id DESC LIMIT 1)`, leadID); err != nil {
		t.Fatalf("simulating malformed retained factors: %v", err)
	}
	lead, err = store.GetLead(ctx, leadID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("malformed retained factors broke the lead read: %v", err)
	}
	if lead.ScoreReason != nil {
		t.Fatalf("malformed retained factors produced score reason %q", *lead.ScoreReason)
	}
}

// The bug this test exists for: setting an override moves the DISPLAYED
// score without touching the machine value, so no recompute runs. Nothing
// else writes the entry, and the newest point in the series would still
// hold the pre-override number — "Explain This Score" answering for a score
// the lead no longer carries.
func TestSettingAnOverrideLandsInTheSeriesAndKeepsTheMachineNumber(t *testing.T) {
	ctx, store := newLeadScoreEnv(t)
	leadID := seedScoredLead(ctx, t, store)

	before, err := store.ExplainLeadScore(ctx, leadID, ExplainLeadScoreInput{Limit: 10})
	if err != nil {
		t.Fatalf("reading the machine score: %v", err)
	}
	machine := before.Current.ScoreComputed

	override := 88
	reason := "strategic account — the model cannot see the parent group"
	if _, err := store.UpdateLead(ctx, leadID, UpdateLeadInput{
		Score:               &override,
		ScoreOverrideReason: &reason,
	}); err != nil {
		t.Fatalf("setting the override: %v", err)
	}

	after, err := store.ExplainLeadScore(ctx, leadID, ExplainLeadScoreInput{Limit: 10})
	if err != nil {
		t.Fatalf("explaining after the override: %v", err)
	}
	if after.Score != override {
		t.Errorf("displayed score = %d, want the human's %d", after.Score, override)
	}
	if after.Current == nil {
		t.Fatal("the override produced no entry — the series still answers for the old score")
	}
	if after.Current.Score != override {
		t.Errorf("newest entry displays %d, want %d", after.Current.Score, override)
	}
	// The whole point of the dual number: the factors below still explain
	// what the MODEL says, and the entry says so rather than presenting the
	// breakdown as an account of the human's 88.
	if after.Current.ScoreComputed != machine {
		t.Errorf("machine value moved on override: %d → %d", machine, after.Current.ScoreComputed)
	}
	if after.Current.OverrideReason == nil || *after.Current.OverrideReason != reason {
		t.Errorf("the entry does not carry the written reason: %+v", after.Current.OverrideReason)
	}
}

// Clearing the override resumes recompute, which appends its own entry —
// so the series returns to a machine-explained point with no reason on it.
func TestClearingAnOverrideReturnsTheSeriesToTheMachineScore(t *testing.T) {
	ctx, store := newLeadScoreEnv(t)
	leadID := seedScoredLead(ctx, t, store)

	override := 88
	reason := "strategic account"
	if _, err := store.UpdateLead(ctx, leadID, UpdateLeadInput{
		Score: &override, ScoreOverrideReason: &reason,
	}); err != nil {
		t.Fatalf("setting the override: %v", err)
	}
	if _, err := store.UpdateLead(ctx, leadID, UpdateLeadInput{ClearScoreOverride: true}); err != nil {
		t.Fatalf("clearing the override: %v", err)
	}

	out, err := store.ExplainLeadScore(ctx, leadID, ExplainLeadScoreInput{History: true, Limit: 10})
	if err != nil {
		t.Fatalf("reading the series: %v", err)
	}
	if out.History == nil || len(*out.History) < 2 {
		t.Fatalf("the series should hold the create and the override at least: %+v", out.History)
	}
	// Newest first: the clear resumed recompute, so the top of the series is
	// a machine-explained point again with no reason hanging off it.
	newest := (*out.History)[0]
	if newest.OverrideReason != nil {
		t.Errorf("the override outlived its own clear: %q", *newest.OverrideReason)
	}
	if newest.Score != newest.ScoreComputed {
		t.Errorf("a cleared lead still shows two numbers: %d vs %d", newest.Score, newest.ScoreComputed)
	}
	// Newest first: every entry that carries a reason must also differ from
	// its machine value, and every one that does not must match it. That is
	// the CHECK constraint restated as behaviour.
	for _, entry := range *out.History {
		if entry.OverrideReason == nil && entry.Score != entry.ScoreComputed {
			t.Errorf("a machine entry shows two different numbers: %+v", entry)
		}
	}
}

// A manual signal counts toward the score and appears as its own labelled
// factor — never blended into a machine one (AC-S7a).
func TestAManualSignalCountsAndStaysItsOwnFactor(t *testing.T) {
	ctx, store := newLeadScoreEnv(t)
	leadID := seedScoredLead(ctx, t, store)

	before, err := store.ExplainLeadScore(ctx, leadID, ExplainLeadScoreInput{Limit: 10})
	if err != nil {
		t.Fatalf("reading the base score: %v", err)
	}

	if _, err := store.SetLeadManualSignal(ctx, leadID, SetLeadManualSignalInput{
		Factor: "employees", Band: "201+", SignalKind: "assumption",
		Reason: "they list four offices on the site",
	}); err != nil {
		t.Fatalf("entering a manual signal: %v", err)
	}

	after, err := store.ExplainLeadScore(ctx, leadID, ExplainLeadScoreInput{Limit: 10})
	if err != nil {
		t.Fatalf("explaining after the manual signal: %v", err)
	}
	if after.Score <= before.Score {
		t.Errorf("the manual signal did not count: %d → %d", before.Score, after.Score)
	}
	var found bool
	for _, f := range *after.Current.Factors {
		if f.Factor == "manual:employees" {
			found = true
		}
	}
	if !found {
		t.Errorf("the human's input is not its own factor: %+v", *after.Current.Factors)
	}
}

func TestManualSignalReadReturnsTheStoredQualificationEvidence(t *testing.T) {
	ctx, store := newLeadScoreEnv(t)
	leadID := seedScoredLead(ctx, t, store)
	confidence := float32(0.7)
	if _, err := store.SetLeadManualSignal(ctx, leadID, SetLeadManualSignalInput{
		Factor: "employees", Band: "51-200", SignalKind: "assumption",
		Confidence: &confidence, Reason: "the careers page lists about eighty people",
	}); err != nil {
		t.Fatalf("entering a manual signal: %v", err)
	}

	signals, err := store.ListLeadManualSignals(ctx, leadID)
	if err != nil {
		t.Fatalf("reading manual signals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("manual signals = %d, want 1", len(signals))
	}
	got := signals[0]
	if got.Band != "51-200" || got.Points != 8 || got.SignalKind != crmcontracts.LeadManualSignalKindAssumption {
		t.Errorf("stored band, points or kind were lost: %+v", got)
	}
	if got.Confidence == nil || *got.Confidence != confidence {
		t.Errorf("confidence = %v, want %v", got.Confidence, confidence)
	}
	if got.Reason != "the careers page lists about eighty people" || got.SetBy == (openapitypes.UUID{}) {
		t.Errorf("provenance was lost: %+v", got)
	}
}

// Scoring a lead is a WRITE to it, and naming one in the request is a read of
// it: a rep who cannot see a lead must not be able to score it, and must not
// learn it exists by being told so.
//
// The schema fitness gate (backend/migrations, TestFK_rowScopedTargetsHave-
// VisibilityDecision) classifies lead_manual_signal.lead_id as client-supplied
// and gated. That classification is a claim about this call, and this test is
// what makes it true rather than asserted: without the EnsureVisibleLive probe
// in SetLeadManualSignal, a rep could write their judgement onto any lead in
// the workspace by id alone, and the 404-vs-403 answer below is what keeps the
// refusal from confirming the lead is there.
func TestARepCannotScoreALeadTheyCannotSee(t *testing.T) {
	ctx, store, base := newLeadScoreEnvWithBase(t)

	// Owned by the teammate, deliberately: an UNOWNED lead is visible to every
	// scope tier by design (OwnerPredicate reads `owner_id IS NULL OR = me` —
	// a record nobody owns is not somebody else's private record), so a lead
	// with no owner would pass the probe honestly and prove nothing.
	teammate := ids.From[ids.UserKind](base.teammate)
	lead, _, err := store.CreateLead(ctx, CreateLeadInput{
		FullName: ptr("Annika Vogel"),
		Email:    ptr("annika@sudwind.example"),
		Title:    ptr("VP Sales"),
		Source:   "webform",
		Status:   "new",
		OwnerID:  &teammate,
	})
	if err != nil {
		t.Fatalf("seeding a lead owned by somebody else: %v", err)
	}
	leadID := ids.From[ids.LeadKind](ids.UUID(lead.Id))

	// A rep in the same workspace, restricted to their OWN rows. `base.owner` is
	// a fixture role name, not this lead's owner — the lead above belongs to
	// base.teammate, so to this caller it is somebody else's row. The lead grants
	// are real, so what refuses below is row scope and not a missing permission,
	// which is the case the probe exists for.
	notTheLeadsOwner := base.owner
	stranger := principal.WithCorrelationID(
		principal.WithWorkspaceID(context.Background(), base.ws), ids.NewV7())
	stranger = principal.WithActor(stranger, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + notTheLeadsOwner.String(), UserID: notTheLeadsOwner,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  map[string]principal.ObjectGrant{"lead": {Read: true, Update: true}},
			RowScope: principal.RowScopeOwn,
		},
	})

	_, err = store.SetLeadManualSignal(stranger, leadID, SetLeadManualSignalInput{
		Factor: "employees", Band: "201+", SignalKind: "assumption",
		Reason: "scoring a lead that is not mine to see",
	})
	if err == nil {
		t.Fatal("a rep scored a lead outside their row scope — the visibility probe is not running")
	}
	// ErrNotFound, never ErrPermissionDenied: a refusal that distinguishes
	// "exists but forbidden" from "no such lead" tells a caller which ids are
	// real, which is the existence disclosure the row-scope rule forbids.
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("want ErrNotFound so the refusal hides existence, got %v", err)
	}
	if _, err := store.ListLeadManualSignals(stranger, leadID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("manual signal read exposed an out-of-scope lead: got %v, want ErrNotFound", err)
	}

	// And nothing was written: the refusal must land before the insert, not
	// leave a row somebody can no longer see.
	out, err := store.ExplainLeadScore(ctx, leadID, ExplainLeadScoreInput{Limit: 10})
	if err != nil {
		t.Fatalf("re-reading the lead as admin: %v", err)
	}
	for _, f := range *out.Current.Factors {
		if f.Factor == "manual:employees" {
			t.Error("the refused signal was written anyway")
		}
	}
}

// Retention ANONYMIZES an unconverted lead in place rather than deleting
// it, so no ON DELETE cascade fires and the FKs on the two score tables do
// nothing. Both therefore have to be reached by name, or a colleague's
// written judgement and the ids of the subject's own activities outlive the
// erasure that was supposed to remove them (ADR-0105).
//
// This asserts the invariant from the people side — the rows are gone once
// the lead is anonymized — rather than reaching into the privacy module's
// own wiring, so it keeps holding if that wiring is refactored.
func TestAnonymizingALeadRemovesItsScoreExplanation(t *testing.T) {
	ctx, store := newLeadScoreEnv(t)
	leadID := seedScoredLead(ctx, t, store)

	if _, err := store.SetLeadManualSignal(ctx, leadID, SetLeadManualSignalInput{
		Factor: "budget_hint", Band: "confirmed", SignalKind: "fact",
		Reason: "their head of ops told me the number on the call",
	}); err != nil {
		t.Fatalf("entering a manual signal: %v", err)
	}

	pool := store.db.Pool()
	assertCount := func(table string, want int) {
		t.Helper()
		var got int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM `+table+` WHERE lead_id = $1`, leadID).Scan(&got); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if got != want {
			t.Errorf("%s holds %d rows for this lead, want %d", table, got, want)
		}
	}
	assertCount("lead_score_history", 2) // the create, then the manual signal
	assertCount("lead_manual_signal", 1)

	// The retention action's own statements, run here against the same lead:
	// what matters is that the anonymize leaves nothing behind, not which
	// module issued the DELETE.
	if _, err := pool.Exec(ctx, `
		UPDATE lead SET full_name = 'Anonymized Lead', email = NULL, title = NULL,
		  company_name = NULL, candidate_org_key = NULL, raw = NULL,
		  archived_at = coalesce(archived_at, now())
		WHERE id = $1`, leadID); err != nil {
		t.Fatalf("anonymizing the lead: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM lead WHERE id = $1`, leadID).Scan(&remaining); err != nil {
		t.Fatalf("re-reading the lead: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("the lead row should SURVIVE an anonymize — that is why no cascade fires")
	}
	// Proven: the row is still there, so anything relying on a cascade to
	// clear these two tables would be relying on an event that never happens.
	assertCount("lead_score_history", 2)
	assertCount("lead_manual_signal", 1)
}
