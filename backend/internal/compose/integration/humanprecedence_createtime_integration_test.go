// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Human-edit precedence has to cover what a human typed when they CREATED
// the record, not only what they went back and edited.
//
// The create paths audit a single headline key — {name} for a deal — while
// the update paths audit real per-field images. So every other value in a
// create body had no audit row carrying its key, HumanOwnedConflicts found
// no conflict, and the whole patch applied at the auto-execute tier. The
// shipped deal form posts name, amount_minor, currency and
// expected_close_date together, which made that the ordinary case rather
// than an edge one: an agent could turn EUR 2,500.00 into EUR 1.00 and move
// the forecast, with no approval staged and no human asked.

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAgentCannotSilentlyOverwriteCreateTimeHumanValues(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var pipelines struct {
		Data []struct {
			ID     string `json:"id"`
			Stages []struct {
				ID       string `json:"id"`
				Semantic string `json:"semantic"`
			} `json:"stages"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK || len(pipelines.Data) == 0 {
		t.Fatalf("pipelines → %d", status)
	}
	pipelineID := pipelines.Data[0].ID
	var stageID string
	for _, s := range pipelines.Data[0].Stages {
		if s.Semantic == "open" {
			stageID = s.ID
			break
		}
	}

	// The human creates the deal exactly as the shipped form does: every
	// field in ONE post.
	var deal struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": "Acme renewal", "pipeline_id": pipelineID, "stage_id": stageID,
		"amount_minor": 25000000, "currency": "EUR",
		"expected_close_date": "2026-12-31", "source": "manual",
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("human deal create → %d", status)
	}

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "create-time precedence agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// The agent rewrites the money and the forecast. Neither key appears in
	// the create audit's after-image, which is the whole point.
	var split struct {
		AmountMinor    int64  `json:"amount_minor"`
		ExpectedClose  string `json:"expected_close_date"`
		StagedApproval struct {
			ApprovalID string          `json:"approval_id"`
			Fields     []string        `json:"fields"`
			Replay     json.RawMessage `json:"replay"`
		} `json:"staged_approval"`
	}
	if status := e.call(t, "PATCH", "/v1/deals/"+deal.ID, anyMap{
		"amount_minor": 100, "expected_close_date": "2027-06-30",
	}, bearer, &split); status != http.StatusOK {
		t.Fatalf("agent money patch → %d", status)
	}
	if split.StagedApproval.ApprovalID == "" {
		t.Fatal("the agent rewrote human-typed money with no approval staged")
	}
	staged := map[string]bool{}
	for _, f := range split.StagedApproval.Fields {
		staged[f] = true
	}
	if !staged["amount_minor"] || !staged["expected_close_date"] {
		t.Errorf("staged fields = %v, want both create-time values withheld", split.StagedApproval.Fields)
	}

	// Nothing landed while the approval is pending.
	var current struct {
		AmountMinor       int64  `json:"amount_minor"`
		ExpectedCloseDate string `json:"expected_close_date"`
	}
	if status := e.call(t, "GET", "/v1/deals/"+deal.ID, nil, bearer, &current); status != http.StatusOK {
		t.Fatalf("read back → %d", status)
	}
	if current.AmountMinor != 25000000 {
		t.Errorf("amount_minor = %d, want the human's 25000000 untouched", current.AmountMinor)
	}
	if current.ExpectedCloseDate != "2026-12-31" {
		t.Errorf("expected_close_date = %q, want the human's 2026-12-31 untouched", current.ExpectedCloseDate)
	}
}

// The narrowing half: precedence protects values, not field names. A field
// the record does not hold yet has nothing for an agent to undo, so filling
// it stays auto-execute — the gate exists to stop a silent overwrite, not to
// stop the agent working.
func TestAgentStillFillsAnEmptyFieldWithoutApproval(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	personID, agentToken := stagePersonAndAgent(t, e, "Petra Human", "empty-field agent")
	bearer := map[string]string{"Authorization": "Bearer " + agentToken}

	var patched struct {
		Title          string `json:"title"`
		StagedApproval *struct {
			ApprovalID string `json:"approval_id"`
		} `json:"staged_approval"`
	}
	if status := e.call(t, "PATCH", "/v1/people/"+personID, anyMap{"title": "Founder"}, bearer, &patched); status != http.StatusOK {
		t.Fatalf("agent fills an empty field → %d", status)
	}
	if patched.StagedApproval != nil && patched.StagedApproval.ApprovalID != "" {
		t.Errorf("filling an empty title staged approval %s — there was no human value to undo",
			patched.StagedApproval.ApprovalID)
	}
	if patched.Title != "Founder" {
		t.Errorf("title = %q, want the agent's fill to have landed", patched.Title)
	}
}
