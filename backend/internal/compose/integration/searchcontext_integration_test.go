// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// search_context as a CLIENT reaches it: through the registered tool, over real
// rows, real RLS and the real row-scope clauses.
//
// Both retrieval lanes are already row-scoped, so the two-principal case here is
// belt AND braces — which is the point. The hydration read is a SECOND read, and
// a hit that survived ranking can still fail it; a tool that skipped it would
// serve a record whose trust tier nothing stamped, charge nothing for the page,
// and answer with an envelope claiming it had read nothing.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type contextFixture struct {
	rep1Person, rep3Person ids.UUID
}

func seedContextFixture(t *testing.T, e *queryEnv) contextFixture {
	t.Helper()
	return contextFixture{
		rep1Person: e.Seed(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, 'Annegret Turbinenbau', 'manual', 'human:x')`, e.Rep1),
		rep3Person: e.Seed(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, 'Bernhard Turbinenbau', 'manual', 'human:x')`, e.Rep3),
	}
}

// The positive control: a ranked hit comes back as a RECORD, with the excerpt
// that ranked it, and the envelope names it.
func TestSearchContextAnswersRankedRecordsWithTheirExcerpts(t *testing.T) {
	e := setupQuery(t)
	f := seedContextFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})

	sealed := invokeContextSearch(e.admin(), t, registry, `{"query":"Turbinenbau","record_types":["person"]}`)
	answer := contextPayload(t, sealed.Data)

	if len(answer.Hits) != 2 {
		t.Fatalf("got %d hits, want both Turbinenbau people: %+v", len(answer.Hits), answer)
	}
	for _, hit := range answer.Hits {
		if len(hit.Record.Fields) == 0 || hit.Record.Version == 0 {
			t.Errorf("hit %s is a reference, not a record: fields=%s version=%d",
				hit.Record.ID, hit.Record.Fields, hit.Record.Version)
		}
		if len(hit.Excerpts) == 0 {
			t.Errorf("hit %s carries no excerpt, so nothing says why it ranked", hit.Record.ID)
		}
		if !sealedNames(sealed, hit.Record.ID) {
			t.Errorf("the envelope does not name hit %s — every hit is a read and is sourced as one", hit.Record.ID)
		}
	}
	if !contextHas(answer, f.rep1Person) || !contextHas(answer, f.rep3Person) {
		t.Errorf("hits = %+v, want both seeded people for an admin", answer.Hits)
	}
}

// The two-principal property, at the hydration step. A rep sees strictly their
// own half, and the answer never says how much it left out.
func TestSearchContextNarrowsToTheCallersRowScope(t *testing.T) {
	e := setupQuery(t)
	f := seedContextFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})

	answer := contextPayload(t, invokeContextSearch(e.teamRep(e.Rep1, e.Team1), t, registry,
		`{"query":"Turbinenbau","record_types":["person"]}`).Data)

	if contextHas(answer, f.rep3Person) {
		t.Fatalf("a rep was served the other team's person %s", f.rep3Person)
	}
	if !contextHas(answer, f.rep1Person) {
		t.Fatalf("the rep's own person is missing — the narrowing went too far: %+v", answer.Hits)
	}
	// Nothing says how much was left out. Both retrieval lanes are row-scoped, so
	// the narrowing happens before this tool sees a hit at all — there is no
	// note to carry a count, and the assertion is that none appears.
	for _, note := range answer.Notes {
		if note.Code == agents.CodeRowUnreadable {
			t.Errorf("a row-scope narrowing surfaced as a hydration drop: %+v", note)
		}
	}
}

// The registry this test drives binds no embed lane, which is a real deployment
// posture — and the answer has to SAY it ranked lexically. A page that looks
// identical to a semantic one is the failure this marker exists to prevent.
func TestSearchContextReportsAnUnboundEmbedLane(t *testing.T) {
	e := setupQuery(t)
	seedContextFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})

	answer := contextPayload(t, invokeContextSearch(e.admin(), t, registry,
		`{"query":"Turbinenbau","record_types":["person"]}`).Data)

	if answer.Coverage != agents.CoveragePartialDegraded {
		t.Errorf("coverage = %q with no embed lane bound, want partial_degraded", answer.Coverage)
	}
	degraded := false
	for _, note := range answer.Notes {
		if note.Code == agents.CodeSemanticRankingDegraded {
			degraded = true
		}
	}
	if !degraded {
		t.Errorf("notes = %+v, want the lexical fallback named", answer.Notes)
	}
}

func contextHas(answer agents.SearchContextResult, id ids.UUID) bool {
	for _, hit := range answer.Hits {
		if hit.Record.ID == id {
			return true
		}
	}
	return false
}

func invokeContextSearch(ctx context.Context, t *testing.T, registry *agents.Registry, args string) sealedResult {
	t.Helper()
	out, err := registry.Invoke(ctx, "search_context", json.RawMessage(args))
	if err != nil {
		t.Fatalf("search_context %s\n  → %v", args, err)
	}
	var sealed sealedResult
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatalf("the result is not an envelope: %v (%s)", err, out)
	}
	return sealed
}

func contextPayload(t *testing.T, payload json.RawMessage) agents.SearchContextResult {
	t.Helper()
	var answer agents.SearchContextResult
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("unreadable search_context payload %s: %v", payload, err)
	}
	return answer
}
