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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
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

	// The brief is a persisted read-model, so the lane assembles one for this
	// rep before reading it: read_brief never ranks, and an unassembled brief
	// answers not-found rather than an empty queue.
	snapshotBriefRun(ctx, t, e.Pool)

	calls := []struct{ tool, args string }{
		{"list_pipelines", `{}`},
		{"read_brief", `{}`},
		// An enumeration, narrowed and unnarrowed. The narrowed one is the
		// answer worth holding to the shape: a filter that reached no SQL still
		// returns a well-formed page, of the wrong rows.
		{"list_records", `{"record_type":"deal","filters":{"pipeline_id":"` + pipeline.String() + `"}}`},
		{"list_records", `{"record_type":"person","limit":5}`},
		{"run_report", `{"report":"deals-by-stage"}`},
		{"search_records", `{"q":"Conformance"}`},
		{"search_records", `{"q":"Conformance","record_type":"person","limit":5}`},
		// A query that matches nothing: the empty answer has to keep the shape
		// too, and it is the one a caller is most likely to mis-read.
		{"search_records", `{"q":"nothing here matches this"}`},
		{"read_record", `{"record_type":"person","id":"` + person.String() + `"}`},
		// A plan that matches rows and one that matches none. The empty answer
		// is the one worth holding to the shape: `coverage` is the field a
		// caller reads before believing a short result, and it is not omitempty
		// precisely so an absent one cannot read as a complete one.
		{"query_workspace", `{"plan":{"version":"v1","target":"deal","where":[{"field":"status","op":"eq","value":"open"}]}}`},
		{"query_workspace", `{"plan":{"version":"v1","target":"deal","where":[{"field":"name","op":"eq","value":"nothing here matches this"}]}}`},
		{"read_record", `{"record_type":"deal","id":"` + deal.String() + `"}`},
		// A ranked sweep that finds rows and one that finds none. The empty page
		// is the one worth pinning: it still carries `coverage` and `notes`, and
		// `notes` is not omitempty precisely so `null` cannot read as "nothing to
		// report" on a page that was in fact degraded.
		{"search_context", `{"query":"Conformance","record_types":["person"]}`},
		{"search_context", `{"query":"nothing here matches this"}`},
		// A payload that resolves and one that resolves to nothing. The second is
		// the answer a caller acts on by CREATING a record, so its shape is the
		// one a mis-read costs the most.
		{"resolve_entities", `{"candidates":[{"kind":"person","ref":"a","name":"Conformance"}]}`},
		{"resolve_entities", `{"candidates":[{"kind":"organization","emails":["nobody@nowhere.example"]}]}`},
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
	}
	assertEveryRegisteredToolIsAccountedFor(t, registry, calls)

	for _, call := range calls {
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

// unreachableInThisLane names the tools this sweep CANNOT invoke, and why. Each
// needs a seam the lane does not stand up — a live calendar, an outbound mail or
// channel provider, a crawl runner, a drafting brain — so a call would exercise
// a stub rather than a handler.
//
// It is a waiver rather than prose because the census below reads it: a tool
// listed here and then made reachable fails as loudly as one that was never
// covered, so the list cannot quietly outlive its reason.
var unreachableInThisLane = gatekit.Waive(map[string]string{
	"book_meeting":         "needs a live calendar provider",
	"send_email":           "needs an outbound mail provider",
	"send_message":         "needs an outbound channel provider",
	"draft_email":          "needs a drafting model path",
	"draft_follow_ups_for": "needs a drafting model path",
	"enrich":               "needs a crawl runner and a model path",
})

// assertEveryRegisteredToolIsAccountedFor is what makes AC-MCP-7's "every
// registered tool" true rather than aspirational.
//
// The sweep's own table cannot say it: a tool added tomorrow is simply absent
// from it, and an absent tool is indistinguishable from a passing one. So the
// census is derived from the REGISTRY — every spec is either invoked below or
// named as unreachable with its reason, and nothing may be both.
func assertEveryRegisteredToolIsAccountedFor(t *testing.T, registry *agents.Registry, calls []struct{ tool, args string }) {
	t.Helper()
	// create_record is exercised by the fixture setup rather than by the table:
	// every record the calls below read was made through it, and
	// createThroughTheToolSurface holds each of those answers to its schema and
	// its envelope on the way past.
	invoked := map[string]bool{"create_record": true}
	for _, call := range calls {
		invoked[call.tool] = true
	}
	for _, spec := range registry.Specs() {
		excused := unreachableInThisLane.Waived(t, spec.Name)
		switch {
		case invoked[spec.Name] && excused:
			t.Errorf("%s is both invoked here and excused as unreachable — one of the two is stale", spec.Name)
		case !invoked[spec.Name] && !excused:
			t.Errorf("%s is registered but never invoked here, so nothing holds its result to the schema "+
				"or the envelope. Add a call above, or name the seam it needs in unreachableInThisLane.", spec.Name)
		}
	}
	// And the other direction: an entry no registered tool reached is a reason
	// describing a tool that is gone, which reads as covered while certifying
	// nothing.
	unreachableInThisLane.AssertAllMatched(t)
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

// snapshotBriefRun assembles one morning brief for the acting rep, the way the
// human home surface does.
//
// read_brief re-reads a persisted run and never ranks — that is the contract's
// own rule, not a limitation of this lane — so without a run the sweep would be
// certifying a not-found instead of an answer.
func snapshotBriefRun(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	engine := briefs.NewBriefEngine(pool, people.NewStore(pool))
	// A fixed instant. The run only has to EXIST for read_brief to have
	// something to re-read, and ranking against the wall clock would make what
	// this lane certifies depend on the day it ran.
	if _, err := engine.SnapshotRun(ctx, time.Date(2026, 8, 8, 6, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("assembling a brief run for the acting rep: %v", err)
	}
}
