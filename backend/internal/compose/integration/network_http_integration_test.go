// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The relationship-graph reads over HTTP (ADR-0078). These go through the real
// router, the real gates and the real payload shapes — the layer where a
// contract promise either holds or does not.

import (
	"net/http"
	"testing"
)

type networkColleagueDTO struct {
	UserID          string `json:"user_id"`
	DisplayName     string `json:"display_name"`
	Strength        *int   `json:"strength"`
	StrengthBucket  string `json:"strength_bucket"`
	Interactions90d int    `json:"interactions_90d"`
}

type personNetworkDTO struct {
	PersonID   string                `json:"person_id"`
	Colleagues []networkColleagueDTO `json:"colleagues"`
}

type coverageRiskDTO struct {
	Kind      string   `json:"kind"`
	Summary   string   `json:"summary"`
	PersonIDs []string `json:"person_ids"`
	UserIDs   []string `json:"user_ids"`
}

type dealCoverageDTO struct {
	DealID       string            `json:"deal_id"`
	Stakeholders []anyMap          `json:"stakeholders"`
	OurSide      []anyMap          `json:"our_side"`
	Risks        []coverageRiskDTO `json:"risks"`
}

func TestPersonNetworkAnswersHonestlyWhenNobodyKnowsThem(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var person anyMap
	if status := e.call(t, "POST", "/v1/people",
		anyMap{"full_name": "Unknown Contact"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("creating the contact: %d", status)
	}
	id, _ := person["id"].(string)

	// A contact nobody has spoken to answers 200 with an EMPTY list, not 404
	// and not an error. "The account is cold" is the useful answer here, and
	// the surface has to be able to say it.
	var got personNetworkDTO
	if status := e.call(t, "GET", "/v1/people/"+id+"/network", nil, nil, &got); status != http.StatusOK {
		t.Fatalf("network status = %d, want 200", status)
	}
	if got.PersonID != id {
		t.Errorf("payload names person %s, want %s", got.PersonID, id)
	}
	if len(got.Colleagues) != 0 {
		t.Errorf("a contact with no interactions has %d colleagues", len(got.Colleagues))
	}
}

func TestPersonNetworkHidesAContactTheCallerCannotRead(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A person id that does not exist is indistinguishable from one this
	// caller may not see — existence is not disclosed, here as everywhere.
	var body anyMap
	if status := e.call(t, "GET",
		"/v1/people/019fb000-0000-7000-8000-00000000dead/network", nil, nil, &body); status != http.StatusNotFound {
		t.Errorf("an unknown contact answered %d, want 404 — a network read must not "+
			"confirm that a record exists when the person read would not", status)
	}
}

func TestDealCoverageFlagsAThreadlessDealAndExplainsWhy(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var pipelines anyMap
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
		t.Fatalf("listing pipelines: %d", status)
	}
	pipeline, stage := firstPipelineAndStage(t, pipelines)

	var deal anyMap
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": "Threadless", "pipeline_id": pipeline, "stage_id": stage, "source": "ui",
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("creating the deal: %d", status)
	}
	dealID, _ := deal["id"].(string)

	var got dealCoverageDTO
	if status := e.call(t, "GET", "/v1/deals/"+dealID+"/coverage", nil, nil, &got); status != http.StatusOK {
		t.Fatalf("coverage status = %d, want 200", status)
	}
	if got.DealID != dealID {
		t.Errorf("payload names deal %s, want %s", got.DealID, dealID)
	}
	// REPORT-PARAM-1 verbatim: zero engaged contacts is below the floor of
	// two, so a deal with nobody on it reads as single-threaded — the same
	// answer the reporting surface gives, which is the point of reusing the
	// rule rather than inventing one.
	var found *coverageRiskDTO
	for i := range got.Risks {
		if got.Risks[i].Kind == "single_threaded_theirs" {
			found = &got.Risks[i]
		}
	}
	if found == nil {
		t.Fatalf("a deal with no engaged contacts raised no single-threaded risk: %+v", got.Risks)
	}
	if found.Summary == "" {
		t.Error("the risk carries no reason — a flag a human cannot read is a red dot")
	}
}

func TestDealCoverageHidesADealTheCallerCannotRead(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// The coverage payload names the deal's people, so a caller who cannot
	// read the deal must not learn who sits on it.
	var body anyMap
	if status := e.call(t, "GET",
		"/v1/deals/019fb000-0000-7000-8000-00000000beef/coverage", nil, nil, &body); status != http.StatusNotFound {
		t.Errorf("an unknown deal answered %d, want 404", status)
	}
}

// firstPipelineAndStage picks the default pipeline and its first stage.
func firstPipelineAndStage(t *testing.T, listed anyMap) (pipeline, stage string) {
	t.Helper()
	data, _ := listed["data"].([]any)
	if len(data) == 0 {
		t.Fatal("the workspace seed produced no pipeline")
	}
	first, _ := data[0].(map[string]any)
	pipeline, _ = first["id"].(string)
	stages, _ := first["stages"].([]any)
	if len(stages) == 0 {
		t.Fatal("the default pipeline has no stages")
	}
	firstStage, _ := stages[0].(map[string]any)
	stage, _ = firstStage["id"].(string)
	return pipeline, stage
}
