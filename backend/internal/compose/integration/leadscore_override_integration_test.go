// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Commercial Judgement lead-score override (formulas §3.1, AC-S1,
// A68/ADR-0053): a human score is sticky. It demands a written reason
// (score without one is a 422), suppresses the §3 recompute so a later
// activity never overwrites the human value — the machine value is
// retained in score_computed instead — and, when the reason is cleared,
// recompute resumes and score tracks the machine value again.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// TestLeadScoreOverrideColumnsAndRLS pins the additive migration: the two
// override columns exist and lead still carries FORCE row-level security
// (ADD COLUMN never relaxes it).
func TestLeadScoreOverrideColumnsAndRLS(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()

	var forced bool
	if err := e.owner.QueryRow(ctx,
		`SELECT relforcerowsecurity FROM pg_class WHERE relname = 'lead'`).Scan(&forced); err != nil {
		t.Fatal(err)
	}
	if !forced {
		t.Fatal("lead lost FORCE ROW LEVEL SECURITY after the override migration")
	}

	var cols int
	if err := e.owner.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		   WHERE table_name = 'lead' AND column_name IN ('score_override_reason', 'score_computed')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 2 {
		t.Fatalf("override columns present = %d, want 2", cols)
	}
}

func TestLeadScoreOverrideIsSticky(t *testing.T) {
	e := setupSearch(t)
	engine := compose.NewWorkflowEngine(e.Pool)
	ctx := e.asFullUser()
	store := people.NewStore(e.Pool)
	activityStore := activities.NewStore(e.Pool)

	// A working lead: decision-maker title (+15) from a high-intent source
	// (+8) → machine fit is 23. Seeded at score 0 so a resumed recompute
	// demonstrably rebuilds it.
	leadID := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, title, status, source, score, captured_by)
	                     VALUES ($1, $2, 'Vera VP', 'VP Sales', 'working', 'inbound', 0, 'human:x')`)

	// (1) A human score with no reason is rejected (AC-S1).
	if _, err := store.UpdateLead(ctx, leadIDOf(leadID), people.UpdateLeadInput{Score: intp(90)}); err == nil {
		t.Fatal("score without a reason was accepted; want ScoreOverrideReasonRequiredError")
	} else {
		var want *people.ScoreOverrideReasonRequiredError
		if !errors.As(err, &want) {
			t.Fatalf("score without reason → %v, want ScoreOverrideReasonRequiredError", err)
		}
	}

	// (2) A human score WITH a reason persists both and retains the prior
	// machine value (0) in score_computed.
	overridden, err := store.UpdateLead(ctx, leadIDOf(leadID), people.UpdateLeadInput{
		Score: intp(90), ScoreOverrideReason: strp("strategic account — board-level sponsor"),
	})
	if err != nil {
		t.Fatalf("setting the override: %v", err)
	}
	if overridden.Score != 90 || overridden.ScoreOverrideReason == nil ||
		*overridden.ScoreOverrideReason != "strategic account — board-level sponsor" {
		t.Fatalf("override not persisted: score=%d reason=%v", overridden.Score, overridden.ScoreOverrideReason)
	}
	if overridden.ScoreComputed == nil || *overridden.ScoreComputed != 0 {
		t.Fatalf("prior machine value not retained: score_computed=%v, want 0", overridden.ScoreComputed)
	}

	// A subsequent activity-driven recompute must NOT move the sticky score;
	// it updates the retained machine value only (fit 23 + fresh reply 25 = 48).
	dispatchActivityCaptured(t, engine, e.WS, ids.NewV7(), logInboundReply(t, ctx, activityStore, leadID))
	afterEvent, err := store.GetLead(ctx, leadIDOf(leadID), storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if afterEvent.Score != 90 {
		t.Fatalf("recompute overwrote the sticky score: %d, want 90", afterEvent.Score)
	}
	if afterEvent.ScoreComputed == nil || *afterEvent.ScoreComputed != 48 {
		t.Fatalf("machine value not tracked in score_computed: %v, want 48", afterEvent.ScoreComputed)
	}

	// (3) Clearing the reason (explicit empty string) resumes recompute:
	// score tracks the machine value and both override columns go null.
	cleared, err := store.UpdateLead(ctx, leadIDOf(leadID), people.UpdateLeadInput{ScoreOverrideReason: strp("")})
	if err != nil {
		t.Fatalf("clearing the override: %v", err)
	}
	if cleared.ScoreOverrideReason != nil {
		t.Fatalf("override reason not cleared: %v", cleared.ScoreOverrideReason)
	}
	if cleared.ScoreComputed != nil {
		t.Fatalf("score_computed not cleared on resume: %v", cleared.ScoreComputed)
	}
	if cleared.Score != 48 {
		t.Fatalf("score did not track the machine value on resume: %d, want 48", cleared.Score)
	}
}

// logInboundReply logs one lead-linked inbound email — the fresh-reply
// signal (+25) the recompute scenarios feed the workflow lane.
func logInboundReply(t *testing.T, ctx context.Context, activityStore *activities.Store, leadID ids.UUID) ids.UUID {
	t.Helper()
	subject := "Re: your offer"
	direction := "inbound"
	occurred := time.Now().UTC().Add(-1 * time.Minute)
	reply, _, err := activityStore.LogActivity(ctx, activities.LogActivityInput{
		Kind: "email", Subject: &subject, Direction: &direction, OccurredAt: &occurred,
		Links:  []activities.ActivityLinkInput{{EntityType: "lead", EntityID: leadID}},
		Source: "manual",
	})
	if err != nil {
		t.Fatalf("logging the lead-linked reply: %v", err)
	}
	return ids.UUID(reply.Id)
}

// TestLeadScoreOverrideRejectsMissingReasonOverHTTP proves the wire
// contract: a human PATCH setting score with no reason answers 422 with
// the validation-error field shape.
func TestLeadScoreOverrideRejectsMissingReasonOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var lead struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/leads", anyMap{"full_name": "Otto Lead", "source": "manual"}, nil, &lead); status != http.StatusCreated {
		t.Fatalf("create lead → %d", status)
	}

	var problem struct {
		Code    string `json:"code"`
		Details struct {
			Errors []struct {
				Field string `json:"field"`
				Code  string `json:"code"`
			} `json:"errors"`
		} `json:"details"`
	}
	if status := e.call(t, "PATCH", "/v1/leads/"+lead.ID, anyMap{"score": 80}, nil, &problem); status != http.StatusUnprocessableEntity {
		t.Fatalf("score without reason → %d, want 422", status)
	}
	if problem.Code != "validation_error" ||
		len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "score_override_reason" {
		t.Fatalf("422 shape wrong: %+v", problem)
	}
}
