// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Pipeline/stage config (events.md §5.3b): renames and probability
// changes emit stage.updated, a reorder emits ONE pipeline.updated with
// the position delta, exactly one default pipeline survives promotion,
// and the deal-scoped stakeholder view rides the relationship table.

import (
	"net/http"
	"testing"
)

func TestPipelineStageConfigLifecycle(t *testing.T) {
	e := setup(t)
	e.slug = "cfg-e2e"
	bootstrapWorkspaceSession(t, e, "Cfg E2E", "cfg@fable.test")

	var second struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"is_default"`
	}
	if status := e.call(t, "POST", "/v1/pipelines", anyMap{
		"name": "Partnerships", "stages": []anyMap{{"name": "Scout", "position": 1}},
	}, nil, &second); status != http.StatusCreated {
		t.Fatalf("create pipeline → %d", status)
	}

	// Promoting the new pipeline demotes the seeded default in one tx.
	if status := e.call(t, "PATCH", "/v1/pipelines/"+second.ID, anyMap{"is_default": true}, nil, nil); status != http.StatusOK {
		t.Fatalf("promote default → %d", status)
	}
	var defaults int
	if err := e.owner.QueryRow(t.Context(),
		`SELECT count(*) FROM pipeline WHERE is_default`).Scan(&defaults); err != nil {
		t.Fatal(err)
	}
	if defaults != 1 {
		t.Fatalf("%d default pipelines after promotion, want exactly 1", defaults)
	}

	var stage struct {
		ID             string `json:"id"`
		WinProbability int    `json:"win_probability"`
	}
	if status := e.call(t, "POST", "/v1/stages", anyMap{
		"pipeline_id": second.ID, "name": "Won", "position": 2, "semantic": "won",
	}, nil, &stage); status != http.StatusCreated {
		t.Fatalf("create stage → %d", status)
	}
	// A colliding position is a 409, not a 500.
	if status := e.call(t, "POST", "/v1/stages", anyMap{
		"pipeline_id": second.ID, "name": "Dup", "position": 2,
	}, nil, nil); status != http.StatusConflict {
		t.Fatalf("position collision → %d, want 409", status)
	}
	if stage.WinProbability != 100 {
		t.Fatalf("won stage minted with probability %d, the terminal rule says 100", stage.WinProbability)
	}

	// Rename → stage.updated; reorder → ONE pipeline.updated.
	if status := e.call(t, "PATCH", "/v1/stages/"+stage.ID, anyMap{"name": "Closed Won"}, nil, nil); status != http.StatusOK {
		t.Fatalf("rename stage → %d", status)
	}
	if status := e.call(t, "PATCH", "/v1/stages/"+stage.ID, anyMap{"position": 5}, nil, nil); status != http.StatusOK {
		t.Fatalf("reorder stage → %d", status)
	}
	var stageUpdated, pipelineUpdated int
	if err := e.owner.QueryRow(t.Context(),
		`SELECT count(*) FILTER (WHERE envelope->>'type' = 'stage.updated'),
		        count(*) FILTER (WHERE envelope->>'type' = 'pipeline.updated')
		 FROM event_outbox`).Scan(&stageUpdated, &pipelineUpdated); err != nil {
		t.Fatal(err)
	}
	if stageUpdated != 1 || pipelineUpdated < 2 { // promotion + reorder
		t.Fatalf("config events: stage.updated=%d pipeline.updated=%d, want 1 and ≥2", stageUpdated, pipelineUpdated)
	}

	var stages struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/stages?pipeline_id="+second.ID, nil, nil, &stages); status != http.StatusOK || len(stages.Data) != 2 {
		t.Fatalf("list stages → %d %+v", status, stages)
	}
	if status := e.call(t, "GET", "/v1/stages/"+stage.ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("get stage → %d", status)
	}
}

func TestDealStakeholdersView(t *testing.T) {
	e := setupRelationships(t)
	var pipelines struct {
		Data []struct {
			ID     string `json:"id"`
			Stages []struct {
				ID string `json:"id"`
			} `json:"stages"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
		t.Fatalf("pipelines → %d", status)
	}
	var deal struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": "Stakeholder Deal", "pipeline_id": pipelines.Data[0].ID,
		"stage_id": pipelines.Data[0].Stages[0].ID,
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("create deal → %d", status)
	}
	if status := e.call(t, "POST", "/v1/relationships", anyMap{
		"kind": "deal_stakeholder", "deal_id": deal.ID, "person_id": e.personID,
		"role": "champion", "source": "ui",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("create stakeholder → %d", status)
	}

	var stakeholders struct {
		Data []struct {
			Role string `json:"role"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/deals/"+deal.ID+"/stakeholders", nil, nil, &stakeholders); status != http.StatusOK {
		t.Fatalf("stakeholders → %d", status)
	}
	if len(stakeholders.Data) != 1 || stakeholders.Data[0].Role != "champion" {
		t.Fatalf("stakeholder view: %+v", stakeholders)
	}
	// An unknown deal answers absent, not an empty page.
	if status := e.call(t, "GET", "/v1/deals/00000000-0000-7000-8000-00000000dead/stakeholders", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown deal stakeholders → %d, want 404", status)
	}
}
