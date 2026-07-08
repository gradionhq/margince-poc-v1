// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Morning-Brief HTTP surface end to end (E05): generate → home read →
// acted/dismissed/snoozed marks over the real handler stack. The brief is
// a PERSONAL lens — the home GET returns only the acting rep's own run,
// and a mark on an item that is not in the rep's own runs (another rep's,
// or none) reads as 404 existence-hiding, never 403.

import (
	"net/http"
	"testing"
	"time"
)

func TestMorningBriefHTTPSurface(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	stages := discoverSeededPipeline(t, e)

	// Two deals closing this week clear the §10 honest-short bar on timing
	// alone: one to act on, one to snooze.
	createdID := createDealClosingThisWeek(t, e, stages, "Closing this week")
	snoozableID := createDealClosingThisWeek(t, e, stages, "Also closing, but not today's problem")

	// Before any run, the home read is an honest 404 — no brief yet.
	if status := e.call(t, "GET", "/v1/brief", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET /v1/brief before generate = %d, want 404", status)
	}

	// Generate the brief: ranks the candidate set and persists a run.
	var generated briefResponse
	if status := e.call(t, "POST", "/v1/brief", nil, nil, &generated); status != http.StatusCreated {
		t.Fatalf("POST /v1/brief = %d, want 201", status)
	}
	if generated.Id == "" || generated.CandidateCount < 2 || len(generated.Items) < 2 {
		t.Fatalf("generated brief = %+v, want a run ranking both seeded deals", generated)
	}
	item := findBriefItem(t, generated.Items, createdID)
	toSnooze := findBriefItem(t, generated.Items, snoozableID)
	if len(item.EvidenceIds) == 0 {
		t.Fatalf("brief item = %+v, want the seeded deal with non-empty evidence (evidence-or-omit)", item)
	}

	// The home read re-reads the same persisted run (no re-rank).
	var read briefResponse
	if status := e.call(t, "GET", "/v1/brief", nil, nil, &read); status != http.StatusOK {
		t.Fatalf("GET /v1/brief = %d, want 200", status)
	}
	if read.Id != generated.Id || len(read.Items) != len(generated.Items) {
		t.Fatalf("home read = %+v, want the generated run %+v", read, generated)
	}

	// A mark on an item that is not in the acting rep's runs is 404
	// existence-hiding — the same shape another rep's item would take.
	foreign := "00000000-0000-7000-8000-000000000000"
	if status := e.call(t, "POST", "/v1/brief/items/"+foreign+"/act", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("mark on a non-owned item = %d, want 404 (existence-hiding)", status)
	}

	// The rep's own item marks acted; a second mark is a conflict, never a
	// silent overwrite.
	var acted briefItemResponse
	if status := e.call(t, "POST", "/v1/brief/items/"+item.Id+"/act", nil, nil, &acted); status != http.StatusOK {
		t.Fatalf("mark own item acted = %d, want 200", status)
	}
	if acted.State != "acted" {
		t.Fatalf("marked item state = %q, want acted", acted.State)
	}
	if status := e.call(t, "POST", "/v1/brief/items/"+item.Id+"/dismiss", nil, nil, nil); status != http.StatusConflict {
		t.Fatalf("double mark = %d, want 409", status)
	}

	assertSnoozeHidesItem(t, e, toSnooze)
}

// createDealClosingThisWeek seeds one open deal whose close date clears
// the §10 timing bar, so it ranks into the rep's brief.
func createDealClosingThisWeek(t *testing.T, e *env, stages seededStages, name string) string {
	t.Helper()
	var created struct {
		Id string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name":                name,
		"pipeline_id":         stages.pipelineID,
		"stage_id":            stages.open,
		"expected_close_date": time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02"),
		"source":              "manual",
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create deal %q status = %d", name, status)
	}
	return created.Id
}

// assertSnoozeHidesItem drives the snooze verb (A77/AC-home-6): a snooze
// into the past is refused, a future one lands, and the home read hides
// the item while snoozed_until lies ahead.
func assertSnoozeHidesItem(t *testing.T, e *env, toSnooze briefItemResponse) {
	t.Helper()
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if status := e.call(t, "POST", "/v1/brief/items/"+toSnooze.Id+"/snooze",
		anyMap{"snoozed_until": past}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("snooze into the past = %d, want 422", status)
	}

	until := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	var snoozed briefItemResponse
	if status := e.call(t, "POST", "/v1/brief/items/"+toSnooze.Id+"/snooze",
		anyMap{"snoozed_until": until}, nil, &snoozed); status != http.StatusOK {
		t.Fatalf("snooze = %d, want 200", status)
	}
	if snoozed.State != "snoozed" || snoozed.SnoozedUntil == "" {
		t.Fatalf("snoozed item = %+v, want state snoozed with snoozed_until echoed", snoozed)
	}

	var afterSnooze briefResponse
	if status := e.call(t, "GET", "/v1/brief", nil, nil, &afterSnooze); status != http.StatusOK {
		t.Fatalf("GET /v1/brief after snooze = %d, want 200", status)
	}
	for _, it := range afterSnooze.Items {
		if it.Id == toSnooze.Id {
			t.Fatal("the home read still shows a mid-snooze item — it must hide until snoozed_until passes")
		}
	}
}

// findBriefItem resolves the queue entry for a deal — rank order between
// equally-scored deals is not what this test proves.
func findBriefItem(t *testing.T, items []briefItemResponse, dealID string) briefItemResponse {
	t.Helper()
	for _, item := range items {
		if item.DealId == dealID {
			return item
		}
	}
	t.Fatalf("no brief item ranks deal %s", dealID)
	return briefItemResponse{}
}

type briefResponse struct {
	Id             string              `json:"id"`
	CandidateCount int                 `json:"candidate_count"`
	Items          []briefItemResponse `json:"items"`
}

type briefItemResponse struct {
	Id           string   `json:"id"`
	DealId       string   `json:"deal_id"`
	Rank         int      `json:"rank"`
	EvidenceIds  []string `json:"evidence_ids"`
	State        string   `json:"state"`
	SnoozedUntil string   `json:"snoozed_until"`
}
