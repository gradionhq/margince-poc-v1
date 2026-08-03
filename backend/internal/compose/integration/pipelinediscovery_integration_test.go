// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The dead end, closed end to end: a caller that knows nothing but the tool
// list must be able to create a deal.
//
// This is U2's acceptance criterion, and it is here rather than in a manual
// script because a manual run proves it once. `create_record` for a deal
// REQUIRES pipeline_id and stage_id; before list_pipelines nothing on the
// surface yielded either, so the only two ways to obtain them were to read the
// database or to be told. Both are things an agent cannot do.
//
// It drives compose.NewRegistry — the same constructor the api role uses — so a
// future edit that unregisters the tool, drops a field from its answer, or
// stops the deal mapping from accepting those ids turns this red.

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// toolPipeline is what a caller can see of the answer: only the fields the tool
// advertises are decoded, so a field renamed on the wire fails here rather than
// being read off the Go struct that produced it.
type toolPipeline struct {
	ID        ids.UUID `json:"id"`
	Name      string   `json:"name"`
	IsDefault bool     `json:"is_default"`
	Stages    []struct {
		ID             ids.UUID `json:"id"`
		Name           string   `json:"name"`
		Semantic       string   `json:"semantic"`
		WinProbability int      `json:"win_probability"`
		Position       int      `json:"position"`
	} `json:"stages"`
}

func TestADealCanBeCreatedFromNothingButTheToolSurface(t *testing.T) {
	e := Setup(t)
	DealFixture(t, e) // the workspace's seeded default pipeline
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	out, err := registry.Invoke(ctx, "list_pipelines", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_pipelines: %v", err)
	}
	var answer struct {
		Pipelines []toolPipeline `json:"pipelines"`
	}
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("unreadable list_pipelines answer %s: %v", out, err)
	}
	if len(answer.Pipelines) == 0 {
		t.Fatalf("list_pipelines answered %s — the seeded default pipeline is missing", out)
	}

	// The caller picks the way an agent would: the default pipeline, and the
	// first stage whose semantic says a deal belongs there. Not "the first
	// stage", and not a stage matched by name — semantic is the field that
	// carries the meaning, which is why the tool returns it.
	pipeline := answer.Pipelines[0]
	for _, p := range answer.Pipelines {
		if p.IsDefault {
			pipeline = p
			break
		}
	}
	var open ids.UUID
	for _, stage := range pipeline.Stages {
		if stage.Semantic == "open" {
			open = stage.ID
			break
		}
	}
	if open.IsZero() {
		t.Fatalf("no stage in %q reports semantic=open; stages were %+v — a deal cannot be born",
			pipeline.Name, pipeline.Stages)
	}

	created, err := registry.Invoke(ctx, "create_record", json.RawMessage(
		`{"record_type":"deal","fields":{"name":"Pipeline discovery","pipeline_id":"`+
			pipeline.ID.String()+`","stage_id":"`+open.String()+`","currency":"EUR","amount_minor":250000}}`))
	if err != nil {
		t.Fatalf("create_record deal with ids obtained from list_pipelines: %v", err)
	}

	// The read-back proves the write landed rather than that the call returned:
	// create_record answers with the record it created, so a deal id and the
	// stage it was filed into are both in the payload.
	var record struct {
		RecordType string          `json:"record_type"`
		ID         ids.UUID        `json:"id"`
		Fields     json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(created, &record); err != nil {
		t.Fatalf("unreadable create_record answer %s: %v", created, err)
	}
	if record.RecordType != "deal" || record.ID.IsZero() {
		t.Fatalf("create_record answered %s, want a deal carrying an id", created)
	}
	var fields struct {
		StageID    ids.UUID `json:"stage_id"`
		PipelineID ids.UUID `json:"pipeline_id"`
	}
	if err := json.Unmarshal(record.Fields, &fields); err != nil {
		t.Fatalf("unreadable deal fields %s: %v", record.Fields, err)
	}
	if fields.StageID != open || fields.PipelineID != pipeline.ID {
		t.Errorf("the deal was filed into pipeline %s stage %s, want the pair list_pipelines named (%s / %s)",
			fields.PipelineID, fields.StageID, pipeline.ID, open)
	}
}

// The move, from the same starting point: advance_deal's to_stage_id has the
// same problem create_record's stage_id had, and the same answer.
func TestADealCanBeAdvancedToAStageObtainedFromTheToolSurface(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Advance discovery", pipeline, open, &e.Rep1)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	out, err := registry.Invoke(ctx, "list_pipelines", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_pipelines: %v", err)
	}
	var answer struct {
		Pipelines []toolPipeline `json:"pipelines"`
	}
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("unreadable list_pipelines answer %s: %v", out, err)
	}

	// A LATER open stage: advancing onto a won or lost one is 🟡 and would be
	// staged rather than executed, which is a different assertion.
	var target ids.UUID
	for _, p := range answer.Pipelines {
		if pipeline.UUID != p.ID {
			continue
		}
		for _, stage := range p.Stages {
			if stage.Semantic == "open" && stage.ID != open.UUID {
				target = stage.ID
				break
			}
		}
	}
	if target.IsZero() {
		t.Fatalf("the seeded pipeline offers no second open stage in %s", out)
	}

	advanced, err := registry.Invoke(ctx, "advance_deal", json.RawMessage(
		`{"deal_id":"`+dealID.String()+`","to_stage_id":"`+target.String()+`"}`))
	if err != nil {
		t.Fatalf("advance_deal onto an open stage from list_pipelines: %v", err)
	}
	var record struct {
		Fields struct {
			StageID ids.UUID `json:"stage_id"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(advanced, &record); err != nil {
		t.Fatalf("unreadable advance_deal answer %s: %v", advanced, err)
	}
	if record.Fields.StageID != target {
		t.Errorf("the deal sits in stage %s, want the one list_pipelines named (%s)",
			record.Fields.StageID, target)
	}
}
