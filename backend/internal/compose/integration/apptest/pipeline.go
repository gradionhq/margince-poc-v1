// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package apptest

import (
	"net/http"
	"testing"
)

// The seeded pipeline, and the one scenario every suite that needs a closed deal
// starts from. They live here rather than beside the end-to-end suite that first
// needed them because they are keyed on AppEnv: a suite package split out of
// internal/compose/integration can import this package, and nothing at all from
// that package's _test.go files.

// SeededStages is the seeded default pipeline's stage vocabulary a scenario
// advances deals through.
type SeededStages struct {
	PipelineID string
	Open       string
	Won        string
	Lost       string
}

// DiscoverSeededPipeline asserts the bootstrap seeded exactly one default
// pipeline with its six stages and resolves the semantic stage ids.
func DiscoverSeededPipeline(t *testing.T, e *AppEnv) SeededStages {
	t.Helper()
	var pipelines struct {
		Data []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"is_default"`
			Stages    []struct {
				ID       string `json:"id"`
				Semantic string `json:"semantic"`
			} `json:"stages"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
		t.Fatalf("pipelines status = %d", status)
	}
	if len(pipelines.Data) != 1 || !pipelines.Data[0].IsDefault || len(pipelines.Data[0].Stages) != 6 {
		t.Fatalf("seeded pipeline shape wrong: %+v", pipelines.Data)
	}
	stages := SeededStages{PipelineID: pipelines.Data[0].ID}
	for _, s := range pipelines.Data[0].Stages {
		switch s.Semantic {
		case "won":
			stages.Won = s.ID
		case "lost":
			stages.Lost = s.ID
		case "open":
			if stages.Open == "" {
				stages.Open = s.ID
			}
		}
	}
	return stages
}

// ExerciseDealToWon creates the organization + deal, asserts losing without a
// reason is refused, and closes the deal as won. Returns the deal id.
func ExerciseDealToWon(t *testing.T, e *AppEnv, stages SeededStages) string {
	t.Helper()
	var org AnyMap
	status := e.Call(t, "POST", "/v1/organizations", AnyMap{
		"display_name": "Acme GmbH",
		"source":       "ui",
		"domains":      []AnyMap{{"domain": "acme.example", "is_primary": true}},
	}, nil, &org)
	if status != http.StatusCreated {
		t.Fatalf("create org = %d %v", status, org)
	}

	var deal AnyMap
	status = e.Call(t, "POST", "/v1/deals", AnyMap{
		"name":            "Acme rollout",
		"amount_minor":    250_000_00,
		"currency":        "EUR",
		"pipeline_id":     stages.PipelineID,
		"stage_id":        stages.Open,
		"organization_id": org["id"],
		"source":          "ui",
	}, nil, &deal)
	if status != http.StatusCreated {
		t.Fatalf("create deal = %d %v", status, deal)
	}
	dealID, ok := deal["id"].(string)
	if !ok {
		t.Fatalf("the created deal carries no string id: %v", deal)
	}

	// Losing without a reason is refused (deal_lost_reason).
	var lostErr AnyMap
	status = e.Call(t, "POST", "/v1/deals/"+dealID+"/advance", AnyMap{"to_stage_id": stages.Lost}, nil, &lostErr)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("lost without reason = %d %v, want 422", status, lostErr)
	}

	status = e.Call(t, "POST", "/v1/deals/"+dealID+"/advance", AnyMap{"to_stage_id": stages.Won}, nil, &deal)
	if status != http.StatusOK || deal["status"] != "won" || deal["closed_at"] == nil {
		t.Fatalf("advance to won = %d %v", status, deal)
	}
	return dealID
}
