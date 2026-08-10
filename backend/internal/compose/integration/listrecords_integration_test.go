// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A filter that reaches no SQL still answers a well-formed page — of the wrong
// rows. That is the one failure the whole list_records path is built against,
// and it is the one no unit test can see: the schema check passes, the envelope
// passes, the seam is called with the filter in hand, and the rows come back
// unnarrowed because nothing bound it to a predicate.
//
// So the assertion here is exclusion, against a real database: two records that
// differ ONLY in the filtered field, and a list that must return one of them.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestListRecordsNarrowsRatherThanAnsweringEveryRow(t *testing.T) {
	e := Setup(t)
	pipeline, open, won := DealFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	inStage := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"deal","fields":{"name":"Narrowed in","pipeline_id":"`+
			pipeline.String()+`","stage_id":"`+open.String()+`"}}`)
	// The second deal starts where every deal must — a deal cannot be created
	// won — and is then moved, so the two differ in exactly the filtered field.
	outOfStage := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"deal","fields":{"name":"Narrowed out","pipeline_id":"`+
			pipeline.String()+`","stage_id":"`+open.String()+`"}}`)
	if _, err := registry.Invoke(ctx, "advance_deal", json.RawMessage(
		`{"deal_id":"`+outOfStage.String()+`","to_stage_id":"`+won.String()+`"}`)); err != nil {
		t.Fatalf("moving the second deal out of the filtered stage: %v", err)
	}

	out, err := registry.Invoke(ctx, "list_records", json.RawMessage(
		`{"record_type":"deal","filters":{"stage_id":"`+open.String()+`"}}`))
	if err != nil {
		t.Fatalf("listing deals in one stage: %v", err)
	}

	if !strings.Contains(string(out), inStage.String()) {
		t.Errorf("the deal in the filtered stage is missing from the answer:\n%s", out)
	}
	if strings.Contains(string(out), outOfStage.String()) {
		t.Errorf("a deal in another stage came back from a stage-filtered list — the filter reached "+
			"no predicate, so the answer is every row wearing the shape of a narrowed one:\n%s", out)
	}
}

// The same proof for the other operand kind this surface binds: an owner-scoped
// list. `owner_id` is the filter every record type carries, so a break in the
// reference binding would widen four enumerations at once.
func TestListRecordsNarrowsByOwner(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	mine := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"person","fields":{"full_name":"Owned By Rep One","owner_id":"`+e.Rep1.String()+`"}}`)
	theirs := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"person","fields":{"full_name":"Owned By Rep Two","owner_id":"`+e.Rep2.String()+`"}}`)

	out, err := registry.Invoke(ctx, "list_records", json.RawMessage(
		`{"record_type":"person","filters":{"owner_id":"`+e.Rep1.String()+`"}}`))
	if err != nil {
		t.Fatalf("listing people by owner: %v", err)
	}

	if !strings.Contains(string(out), mine.String()) {
		t.Errorf("the person owned by the filtered owner is missing from the answer:\n%s", out)
	}
	if strings.Contains(string(out), theirs.String()) {
		t.Errorf("a person owned by someone else came back from an owner-filtered list:\n%s", out)
	}
}
