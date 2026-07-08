// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Morning-Brief HTTP surface end to end (E05): generate → home read →
// acted/dismissed marks over the real handler stack. The brief is a
// PERSONAL lens — the home GET returns only the acting rep's own run, and
// a mark on an item that is not in the rep's own runs (another rep's, or
// none) reads as 404 existence-hiding, never 403.

import (
	"net/http"
	"testing"
	"time"
)

func TestMorningBriefHTTPSurface(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	stages := discoverSeededPipeline(t, e)

	// A deal closing this week clears the §10 honest-short bar on timing
	// alone, so the rep's brief has at least one ranked item to read.
	closeSoon := time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	var created struct {
		Id string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name":                "Closing this week",
		"pipeline_id":         stages.pipelineID,
		"stage_id":            stages.open,
		"expected_close_date": closeSoon,
		"source":              "manual",
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create deal status = %d", status)
	}

	// Before any run, the home read is an honest 404 — no brief yet.
	if status := e.call(t, "GET", "/v1/brief", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET /v1/brief before generate = %d, want 404", status)
	}

	// Generate the brief: ranks the candidate set and persists a run.
	var generated briefResponse
	if status := e.call(t, "POST", "/v1/brief", nil, nil, &generated); status != http.StatusCreated {
		t.Fatalf("POST /v1/brief = %d, want 201", status)
	}
	if generated.Id == "" || generated.CandidateCount < 1 || len(generated.Items) < 1 {
		t.Fatalf("generated brief = %+v, want a run with at least one candidate item", generated)
	}
	item := generated.Items[0]
	if item.Id == "" || item.DealId != created.Id || len(item.EvidenceIds) == 0 {
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
}

type briefResponse struct {
	Id             string              `json:"id"`
	CandidateCount int                 `json:"candidate_count"`
	Items          []briefItemResponse `json:"items"`
}

type briefItemResponse struct {
	Id          string   `json:"id"`
	DealId      string   `json:"deal_id"`
	Rank        int      `json:"rank"`
	EvidenceIds []string `json:"evidence_ids"`
	State       string   `json:"state"`
}
