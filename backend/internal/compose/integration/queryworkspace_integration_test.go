// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// query_workspace as a CLIENT reaches it: through the registered tool, over
// real rows, real RLS and the real row-scope clauses.
//
// The executor's own properties are proven next door (queryplan_integration).
// What is proven HERE is the half only the tool has: a plan's refs become
// records through the datasource seam, the envelope says what they rest on, and
// the two-principal property survives that second read rather than being
// established before it. A hydration step that ignored row scope would pass
// every test in the executor's suite and hand one team's records to another.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The Stuttgart question, end to end: deals of a certain size at an
// organization in one city. It exercises the whole v1 grammar a client can
// reach — an exact predicate, a traversal hop with its own predicate — and
// asserts the thing that distinguishes this PR from the last one: rows come
// back as RECORDS, with the fields a caller can act on, and the hop that
// admitted each one is still attached to it.
func TestQueryWorkspaceAnswersTheStuttgartQuestionAsRecords(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	registry := compose.NewRegistry(q.Pool, compose.SendPath{})

	sealed := invokeQuery(q.admin(), t, registry, `{"plan":{
		"version": "v1", "target": "deal",
		"where": [{"field": "amount_minor", "op": "eq", "value": 100000}],
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}}`)
	answer := queryPayload(t, sealed.Data)

	if len(answer.Rows) != 1 {
		t.Fatalf("got %d rows, want the one Stuttgart deal", len(answer.Rows))
	}
	row := answer.Rows[0]
	if row.Record.ID != f.rep1Deal {
		t.Errorf("row is %s, want the Stuttgart deal %s", row.Record.ID, f.rep1Deal)
	}
	// A ref carries an id; a record carries what the id is FOR. This is the
	// difference the hydration step exists to make, so it is asserted rather
	// than assumed from the row count.
	if len(row.Record.Fields) == 0 || row.Record.Version == 0 {
		t.Errorf("the row is a reference, not a record: fields=%s version=%d", row.Record.Fields, row.Record.Version)
	}
	if len(row.Evidence) != 1 || row.Evidence[0].ID != f.rep1Org ||
		row.Evidence[0].RecordType != "organization" {
		t.Fatalf("the hop that admitted the row did not survive hydration: %+v", row.Evidence)
	}
	if answer.Coverage != agents.CoverageCompleteExact {
		t.Errorf("coverage = %q with notes %+v, want an exact plan to answer completely", answer.Coverage, answer.Notes)
	}
	if answer.ExecutedPlan == "" {
		t.Error("the answer does not say what plan it ran, so a caller cannot check it against what they asked")
	}
	// The hop is a record this answer rests on, so the envelope names it
	// alongside the row. It reaches the caller as a reason to act — the
	// organization's own title — and content the envelope does not account for
	// is content whose trust tier nothing decided.
	if !sealedNames(sealed, f.rep1Org) {
		t.Errorf("the envelope does not name the hop organization %s among the records behind this answer: %+v",
			f.rep1Org, sealed.Evidence)
	}
}

func sealedNames(sealed sealedResult, id ids.UUID) bool {
	for _, evidence := range sealed.Evidence {
		if evidence.RecordID == id {
			return true
		}
	}
	return false
}

// The envelope is not a second description of the answer — it is what the READS
// behind the answer reported. A tool that assembled rows without going through
// the seam would answer with an envelope naming no evidence and claiming no
// freshness, which is exactly what this catches.
func TestQueryWorkspaceSourcesEveryRowItAnswersWith(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	registry := compose.NewRegistry(q.Pool, compose.SendPath{})

	sealed := invokeQuery(q.admin(), t, registry, `{"plan":{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"}]}}`)
	answer := queryPayload(t, sealed.Data)

	if len(answer.Rows) == 0 {
		t.Fatal("the corpus answered no rows — this test would prove nothing about their provenance")
	}
	// Every ROW must be named. The envelope may name MORE than the rows — a
	// traversal sources its hop records too — so this asserts coverage of the
	// rows rather than an equal count, which would break the first time a
	// plan in this test grew a hop.
	for _, row := range answer.Rows {
		if !sealedNames(sealed, row.Record.ID) {
			t.Errorf("the envelope does not name row %s — every row is a read and must be sourced as one",
				row.Record.ID)
		}
	}
	if sealed.Freshness == nil || sealed.Freshness.Authoritative == nil || !*sealed.Freshness.Authoritative {
		t.Errorf("freshness = %+v, want a native corpus reported as authoritative", sealed.Freshness)
	}
	if sealed.Trust == "" {
		t.Error("the answer carries no trust tier, so a client cannot tell workspace content from mirrored content")
	}
}

// The exit criterion, asserted over the WHOLE tool path rather than over the
// executor alone: admission gate, plan execution, hydration, envelope.
//
// The executor is what narrows the rows, and its own suite proves that. What
// this adds is everything downstream of the narrowing — including the envelope,
// which is assembled DURING hydration from the records that were read. A
// correctly scoped row set described by an envelope naming records the caller
// cannot see would leak exactly what the row scope withheld, and nothing in the
// executor's suite can see the envelope.
func TestQueryWorkspaceAnswersTwoPrincipalsFromOneCorpusWithoutLeaking(t *testing.T) {
	q := setupQuery(t)
	f := q.seedFixture(t)
	registry := compose.NewRegistry(q.Pool, compose.SendPath{})
	const plan = `{"plan":{"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"}]}}`

	adminSealed := invokeQuery(q.admin(), t, registry, plan)
	repSealed := invokeQuery(q.teamRep(q.Rep1, q.Team1), t, registry, plan)
	admin, rep := queryPayload(t, adminSealed.Data), queryPayload(t, repSealed.Data)

	adminRows, repRows := rowIDs(admin), rowIDs(rep)
	if len(adminRows) != 3 {
		t.Fatalf("the unbounded reader sees %d of 3 open deals — the corpus is not what the bounded arm is measured against", len(adminRows))
	}
	if !repRows[f.rep1Deal] || !repRows[f.sharedDeal] {
		t.Fatalf("the rep cannot see their own rows: %v", repRows)
	}
	if repRows[f.rep3Deal] {
		t.Fatal("the rep reads another team's deal — hydration widened an answer row scope had already narrowed")
	}
	for id := range repRows {
		if !adminRows[id] {
			t.Fatalf("the rep sees a row the unbounded reader does not: %s", id)
		}
	}
	// The verdict is computed over THEIR answer. A coverage or a note counted
	// over rows they cannot see would state the size of what was withheld.
	if rep.Coverage != agents.CoverageCompleteExact {
		t.Errorf("the rep's coverage = %q with notes %+v — their own answer is exact and complete FOR THEM",
			rep.Coverage, rep.Notes)
	}
	// And the envelope, which the executor's suite cannot see: it names the
	// records this answer rests on, so it must name only records this caller
	// could read.
	for _, row := range rep.Rows {
		if !sealedNames(repSealed, row.Record.ID) {
			t.Errorf("the rep's envelope does not name their own row %s", row.Record.ID)
		}
	}
	// The leak that would matter: a record only the OTHER team can see, named
	// in this caller's envelope. The plan has no hop, so the rows are the only
	// records this answer may rest on.
	if sealedNames(repSealed, f.rep3Deal) {
		t.Errorf("the rep's envelope names %s, another team's deal", f.rep3Deal)
	}
}

// A hop is a READ of the record it lands on. A caller who cannot see the
// organization cannot use it to select deals either — otherwise the hop becomes
// a side channel that answers questions about records the caller was denied.
func TestAHopThroughARecordTheCallerCannotSeeAdmitsNothing(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	registry := compose.NewRegistry(q.Pool, compose.SendPath{})
	const plan = `{"plan":{
		"version": "v1", "target": "deal",
		"where": [{"field": "amount_minor", "op": "eq", "value": 250000}],
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}}`

	admin := queryPayload(t, invokeQuery(q.admin(), t, registry, plan).Data)
	rep := queryPayload(t, invokeQuery(q.teamRep(q.Rep1, q.Team1), t, registry, plan).Data)

	if len(admin.Rows) != 1 {
		t.Fatalf("the unbounded reader sees %d rows through the hop, want the other team's deal", len(admin.Rows))
	}
	if len(rep.Rows) != 0 {
		t.Errorf("the rep reached %d rows through an organization they cannot see", len(rep.Rows))
	}
}

// A refinement is how a caller narrows an answer they already have. Every row
// that survives it must carry the SAME reason it was admitted the first time:
// a refinement that quietly drops evidence leaves the caller holding rows they
// can no longer check.
func TestARefinedQueryPreservesTheProvenanceOfEveryRowItKeeps(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	registry := compose.NewRegistry(q.Pool, compose.SendPath{})

	broad := queryPayload(t, invokeQuery(q.admin(), t, registry, `{"plan":{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"}],
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}}`).Data)
	refined := queryPayload(t, invokeQuery(q.admin(), t, registry, `{"plan":{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"},
		          {"field": "amount_minor", "op": "lte", "value": 100000}],
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}}`).Data)

	if len(broad.Rows) <= len(refined.Rows) || len(refined.Rows) == 0 {
		t.Fatalf("the refinement returned %d of %d rows — it must narrow, and to something",
			len(refined.Rows), len(broad.Rows))
	}
	before := evidenceByRow(broad)
	for _, row := range refined.Rows {
		was, kept := before[row.Record.ID]
		if !kept {
			t.Fatalf("the refinement returned %s, which the broader query did not — it is not a narrowing", row.Record.ID)
		}
		// Non-empty FIRST, then equal. Comparing the two runs alone would be
		// satisfied by a build that dropped evidence from both of them, which is
		// the very defect this test is named for.
		if len(row.Evidence) == 0 {
			t.Fatalf("row %s came back from a plan with a hop carrying no evidence at all", row.Record.ID)
		}
		if len(row.Evidence) != len(was) {
			t.Fatalf("row %s carried %d pieces of evidence and now carries %d",
				row.Record.ID, len(was), len(row.Evidence))
		}
		for i, evidence := range row.Evidence {
			if evidence.ID != was[i].ID || evidence.Relation != was[i].Relation {
				t.Errorf("row %s was admitted by %+v and is now attributed to %+v",
					row.Record.ID, was[i], evidence)
			}
		}
	}
}

// A refused plan is refused THROUGH the tool, and the refusal still says where
// and why.
//
// The refusal shapes are the validator's and are proven there; what is proven
// here is that they survive the tool boundary rather than arriving as a generic
// failure. It asserts the PATH and the CODE, which is the whole of what a
// refusal promises: the caller's own token is deliberately not echoed, because
// a plan is caller-authored text and an unbounded echo of it lands in the same
// run's later prompts. The path is what the caller restates against, and they
// already know what they put there.
//
// The vocabulary that decides two of these three is derived from the live
// catalog, so only a real schema produces the real refusal.
func TestARefusedPlanIsRefusedThroughTheToolAndStillSaysWhereAndWhy(t *testing.T) {
	q := setupQuery(t)
	q.seedFixture(t)
	registry := compose.NewRegistry(q.Pool, compose.SendPath{})

	for _, tc := range []struct {
		name, plan, path, code string
	}{
		{
			name: "a field no table can answer",
			plan: `{"version":"v1","target":"deal","where":[{"field":"profit_margin","op":"eq","value":1}]}`,
			path: "where[0].field", code: "unknown_field",
		},
		{
			name: "a record type the vocabulary does not have",
			plan: `{"version":"v1","target":"invoice"}`,
			path: "target", code: "unknown_target",
		},
		{
			name: "a member the grammar has no place for",
			plan: `{"version":"v1","target":"deal","raw_sql":"select 1"}`,
			path: "raw_sql", code: "unknown_plan_member",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registry.Invoke(q.admin(), "query_workspace", json.RawMessage(`{"plan":`+tc.plan+`}`))
			if err == nil {
				t.Fatal("the plan was answered rather than refused")
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Errorf("the refusal does not locate the fault at %q, so a caller cannot tell which clause to restate: %v",
					tc.path, err)
			}
			if !strings.Contains(err.Error(), tc.code) {
				t.Errorf("the refusal does not carry the %q code, so a caller has only prose to branch on: %v",
					tc.code, err)
			}
			// And it must still be the PLURAL fault form, because that is what
			// the transport keys on to render a clarification. Asserting only
			// the error text would keep passing if the refusal stopped being
			// classifiable — and the agent would then read "the tool failed for
			// an internal reason", with every assertion above still green.
			var faults apperrors.FieldFaults
			if !errors.As(err, &faults) {
				t.Fatalf("the refusal is %T, which the transport renders as an internal fault rather "+
					"than as a clarification", err)
			}
			if len(faults.FieldFaults()) == 0 {
				t.Error("the refusal names no field, so a caller is told something is wrong and not what")
			}
		})
	}
}

// invokeQuery runs one plan as one principal and reads the sealed result back
// the way a client does — through Invoke, so the admission gate, the envelope
// and the hydration all run.
func invokeQuery(ctx context.Context, t *testing.T, registry *agents.Registry, args string) sealedResult {
	t.Helper()
	out, err := registry.Invoke(ctx, "query_workspace", json.RawMessage(args))
	if err != nil {
		t.Fatalf("query_workspace %s\n  → %v", args, err)
	}
	var sealed sealedResult
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatalf("the result is not an envelope: %v (%s)", err, out)
	}
	return sealed
}

func queryPayload(t *testing.T, payload json.RawMessage) agents.QueryWorkspaceResult {
	t.Helper()
	var answer agents.QueryWorkspaceResult
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("unreadable query payload %s: %v", payload, err)
	}
	return answer
}

func rowIDs(answer agents.QueryWorkspaceResult) map[ids.UUID]bool {
	out := map[ids.UUID]bool{}
	for _, row := range answer.Rows {
		out[row.Record.ID] = true
	}
	return out
}

func evidenceByRow(answer agents.QueryWorkspaceResult) map[ids.UUID][]agents.QueryEvidence {
	out := map[ids.UUID][]agents.QueryEvidence{}
	for _, row := range answer.Rows {
		out[row.Record.ID] = row.Evidence
	}
	return out
}
