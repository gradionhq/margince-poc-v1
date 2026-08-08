// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The load-bearing half of the exact output schemas.
//
// A declared schema nothing checks is a comment, and this surface now declares
// thirty of them. The unit tests prove the DERIVATION is right — that a struct
// tag becomes the wire name a caller reads — and prove the CHECKER is right, on
// documents written by hand. Neither of them can prove the thing a client
// actually depends on: that the bytes a handler produces, against a real
// database, satisfy the schema its tool advertised.
//
// So this suite invokes tools for real and holds each answer to its own schema,
// through the same ResultDefect the dispatcher uses — a second spelling here
// would be a second definition of "conforms", and the one that mattered would be
// the server's.
//
// An empty answer is worth checking and is much of what a fresh workspace
// yields. `{"records":[]}` and `{"records":null}` are one Go value apart and
// only one of them keeps the schema, which is exactly the class of defect a
// hand-built result map used to hide.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// WHY THIS RUNS AS A HUMAN, and what that buys. The confirm-first tier gates
// AGENT principals: a 🟡 call from a passport is staged and answers with an
// approval reference rather than a result, so an agent-driven sweep could only
// ever reach the 🟢 half of the surface. A human principal is admitted by RBAC
// and receives the result itself — which is the document these schemas describe,
// and the same document an agent gets once a human has released the call. So the
// sweep covers the confirm-first tools too.
//
// What it still cannot reach is named rather than implied: the tools whose
// handler needs a seam this lane does not stand up — a live calendar
// (book_meeting), an outbound mail or channel provider (send_email,
// send_message), a crawl runner or model path (enrich), a drafting brain
// (draft_follow_ups_for). Their declared schemas are checked by derivation and
// by the encoder-agreement test in the agents package, but not against a live
// handler.
func TestToolAnswersReachableWithoutApprovalSatisfyTheirSchemas(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	// Every record these reads are about is created through the tool surface,
	// which holds create_record's own answer to its schema on the way — a
	// write's read-back is a result like any other, and it is the one every
	// write tool shares.
	person := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"person","fields":{"full_name":"Schema Conformance"}}`)
	org := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"organization","fields":{"display_name":"Conformance GmbH"}}`)
	lead := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"lead","fields":{"email":"lead@conformance.example"}}`)
	deal := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"deal","fields":{"name":"Conformance renewal","pipeline_id":"`+
			pipeline.String()+`","stage_id":"`+open.String()+`"}}`)
	activity := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"activity","fields":{"kind":"note","body":"to be relinked"}}`)
	// Records the confirm-first sweep consumes, each its own so one tool's write
	// cannot decide whether the next tool's call is even legal.
	promotable := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"lead","fields":{"email":"promote@conformance.example","full_name":"Promo Table"}}`)
	spare := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"person","fields":{"full_name":"To Be Archived"}}`)
	duplicate := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"person","fields":{"full_name":"Schema Conformance (dup)"}}`)
	project := createThroughTheToolSurface(ctx, t, registry,
		`{"record_type":"project","fields":{"name":"Conformance project","organization_id":"`+
			org.String()+`"}}`)

	for _, call := range []struct{ tool, args string }{
		{"list_pipelines", `{}`},
		{"run_report", `{"report":"deals-by-stage"}`},
		{"search_records", `{"q":"Conformance"}`},
		{"search_records", `{"q":"Conformance","record_type":"person","limit":5}`},
		// A query that matches nothing: the empty answer has to keep the shape
		// too, and it is the one a caller is most likely to mis-read.
		{"search_records", `{"q":"nothing here matches this"}`},
		{"read_record", `{"record_type":"person","id":"` + person.String() + `"}`},
		{"read_record", `{"record_type":"deal","id":"` + deal.String() + `"}`},
		{"catch_me_up_on", `{"record_type":"deal","record_id":"` + deal.String() + `"}`},
		{"prep_for_meeting", `{"record_type":"deal","record_id":"` + deal.String() + `"}`},
		{"whats_slipping_this_week", `{}`},
		{"at_risk_relationships", `{}`},
		{"who_knows", `{"person_id":"` + person.String() + `"}`},
		{"account_coverage", `{"deal_id":"` + deal.String() + `"}`},
		{"intro_path_to", `{"organization_id":"` + org.String() + `"}`},
		{"qualify_lead", `{"record_id":"` + lead.String() + `"}`},
		// The passthrough shapes, whose declared schema is a GUARANTEED SUBSET
		// rather than a type this module marshals. They are the ones a unit test
		// cannot check at all: nothing here builds the document, so the only way
		// to know the subset is true is to ask the real handler.
		{"check_availability", `{"from":"2026-01-05T09:00:00Z","to":"2026-01-05T17:00:00Z"}`},
		{"relink_activity", `{"activity_id":"` + activity.String() + `","entity_type":"person","entity_id":"` +
			person.String() + `"}`},
		{"disqualify_lead", `{"lead_id":"` + lead.String() + `"}`},
		{"log_activity", `{"kind":"note","body":"conformance","links":[{"entity_type":"deal","entity_id":"` +
			deal.String() + `"}]}`},
		{"update_record", `{"record_type":"person","id":"` + person.String() +
			`","fields":{"title":"Head of Conformance"}}`},
		{"progress_deal", `{"deal_id":"` + deal.String() + `","to_stage_id":"` + open.String() +
			`","note":"still open"}`},
		{"advance_deal", `{"deal_id":"` + deal.String() + `","to_stage_id":"` + open.String() + `"}`},
		// The confirm-first tools, reachable here because this runs as a human.
		{"promote_lead", `{"lead_id":"` + promotable.String() + `","trigger":"human_qualify"}`},
		{"archive_record", `{"record_type":"person","id":"` + spare.String() + `"}`},
		{"merge_records", `{"record_type":"person","source_id":"` + duplicate.String() +
			`","target_id":"` + person.String() + `"}`},
		{"advance_project_phase", `{"project_id":"` + project.String() + `","to_phase":"pursuing"}`},
	} {
		t.Run(call.tool+" "+call.args, func(t *testing.T) {
			spec, registered := registry.Spec(call.tool)
			if !registered {
				t.Fatalf("%s is not registered, so this call proves nothing", call.tool)
			}
			out, err := registry.Invoke(ctx, call.tool, json.RawMessage(call.args))
			if err != nil {
				t.Fatalf("%s(%s): %v", call.tool, call.args, err)
			}
			if defect := agents.ResultDefect(spec.OutputSchema, out); defect != "" {
				t.Errorf("%s answered %s, which does not keep the schema this server advertises for it: %s",
					call.tool, out, defect)
			}
			// AC-MCP-7: the envelope is asserted against the REAL answer of a
			// real call, not against its declaration. A declared envelope
			// nothing checks is the comment this whole change replaces.
			assertEnvelopePopulated(t, call.tool, out)
		})
	}
}

// createThroughTheToolSurface makes one record and returns its id, holding the
// write's own answer to its declared schema before reading the id out of it.
func createThroughTheToolSurface(ctx context.Context, t *testing.T, registry *agents.Registry, args string) ids.UUID {
	t.Helper()
	spec, registered := registry.Spec("create_record")
	if !registered {
		t.Fatal("create_record is not registered")
	}
	out, err := registry.Invoke(ctx, "create_record", json.RawMessage(args))
	if err != nil {
		t.Fatalf("create_record(%s): %v", args, err)
	}
	if defect := agents.ResultDefect(spec.OutputSchema, out); defect != "" {
		t.Fatalf("create_record answered %s, which does not keep its own schema: %s", out, defect)
	}
	assertEnvelopePopulated(t, "create_record", out)
	var created struct {
		Data struct {
			ID ids.UUID `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		t.Fatalf("unreadable create_record answer %s: %v", out, err)
	}
	return created.Data.ID
}

// A conformance suite that could not fail is the thing it exists to prevent, so
// it is shown failing: a schema deliberately declaring a member no result
// carries has to be reported, against a REAL answer rather than a fixture.
func TestTheConformanceCheckFailsAgainstAMisdeclaredSchema(t *testing.T) {
	e := Setup(t)
	DealFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	out, err := registry.Invoke(ctx, "list_pipelines", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_pipelines: %v", err)
	}
	for name, misdeclared := range map[string]string{
		"a required member no result carries":           `{"type":"object","required":["invented"]}`,
		"an envelope member declared as the wrong type": `{"type":"object","properties":{"trust":{"type":"integer"}}}`,
		"a payload member declared as the wrong type": `{"type":"object","properties":{"data":{"type":"object",` +
			`"properties":{"pipelines":{"type":"string"}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if defect := agents.ResultDefect(json.RawMessage(misdeclared), out); defect == "" {
				t.Error("the misdeclaration was reported as satisfied — this suite cannot see " +
					"the defect it exists to catch")
			}
		})
	}
}
