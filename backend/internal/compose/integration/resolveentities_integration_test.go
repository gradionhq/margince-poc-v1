// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// resolve_entities as a CLIENT reaches it: through the registered tool, over
// real rows, real RLS and the real row-scope clauses.
//
// The ladder's own properties are proven in the people module, and the
// translation rules are unit-tested there. What only this lane can prove is the
// half the tool owns and the half a unit test cannot fake: the match ladder is
// workspace-wide, so the ONLY thing standing between one team's records and
// another team's agent is the read-back through the datasource seam. A
// hydration step that skipped it would pass every test in the people module and
// answer a rep with a colleague's record.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// resolveFixture is one person per team, each with an address only they hold.
type resolveFixture struct {
	rep1Person, rep3Person ids.UUID
}

func seedResolveFixture(t *testing.T, e *queryEnv) resolveFixture {
	t.Helper()
	var f resolveFixture
	f.rep1Person = e.Seed(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, $3, 'Anna Weber', 'manual', 'human:x')`, e.Rep1)
	f.rep3Person = e.Seed(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, $3, 'Bernd Kruse', 'manual', 'human:x')`, e.Rep3)
	seedEmail(t, e, f.rep1Person, "anna@acme.example")
	seedEmail(t, e, f.rep3Person, "bernd@logistik.example")
	return f
}

func seedEmail(t *testing.T, e *queryEnv, person ids.UUID, address string) {
	t.Helper()
	if _, err := e.Owner.Exec(context.Background(),
		`INSERT INTO person_email (id, workspace_id, person_id, email, email_type, is_primary, source, captured_by)
		 VALUES ($1, $2, $3, $4, 'work', true, 'manual', 'human:x')`,
		ids.NewV7(), e.WS, person, address); err != nil {
		t.Fatalf("seeding %s: %v", address, err)
	}
}

// The positive control: an address a caller may see resolves to that record, as
// a RECORD — fields and a version, not a bare id.
func TestResolveEntitiesAnswersARecordForAnAddressTheCallerHolds(t *testing.T) {
	e := setupQuery(t)
	f := seedResolveFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})

	sealed := invokeResolve(e.admin(), t, registry,
		`{"candidates":[{"kind":"person","ref":"card","emails":["anna@acme.example"]}]}`)
	answer := resolvePayload(t, sealed.Data)

	if len(answer.Candidates) != 1 {
		t.Fatalf("got %d answers for one candidate", len(answer.Candidates))
	}
	got := answer.Candidates[0]
	if got.Ref != "card" || got.Decision != agents.ResolveDecisionMatched {
		t.Fatalf("answer = %+v, want the caller's label and a match", got)
	}
	if len(got.Matches) != 1 || got.Matches[0].Record.ID != f.rep1Person {
		t.Fatalf("matches = %+v, want the seeded person %s", got.Matches, f.rep1Person)
	}
	if len(got.Matches[0].Record.Fields) == 0 || got.Matches[0].Record.Version == 0 {
		t.Errorf("the match is a reference, not a record: fields=%s version=%d",
			got.Matches[0].Record.Fields, got.Matches[0].Record.Version)
	}
	// Every record served is a read, and the envelope is what the reads
	// reported. A tool that answered ids without going through the seam would
	// name nothing here.
	if !sealedNames(sealed, f.rep1Person) {
		t.Errorf("the envelope does not name the resolved record: %+v", sealed.Evidence)
	}
}

// THE PROPERTY THIS FEATURE RESTS ON. The ladder finds the other team's person
// by address — it is workspace-wide and must be, or the same payload would
// create a duplicate for one rep and not another. The seam read is what turns
// that into an answer this caller is entitled to, and the answer must be the
// same word a genuine miss gets.
func TestAnAddressOutsideTheCallersScopeResolvesToNothingItCanTellApart(t *testing.T) {
	e := setupQuery(t)
	seedResolveFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	rep1 := e.teamRep(e.Rep1, e.Team1)

	withheld := resolvePayload(t, invokeResolve(rep1, t, registry,
		`{"candidates":[{"kind":"person","emails":["bernd@logistik.example"]}]}`).Data)
	absent := resolvePayload(t, invokeResolve(rep1, t, registry,
		`{"candidates":[{"kind":"person","emails":["nobody@nowhere.example"]}]}`).Data)

	if got := withheld.Candidates[0].Decision; got != agents.ResolveDecisionUnresolved {
		t.Fatalf("a colleague's record answered %q — the caller can now probe for records they may not read", got)
	}
	if len(withheld.Candidates[0].Matches) != 0 {
		t.Fatalf("a record outside the caller's row scope was served: %+v", withheld.Candidates[0].Matches)
	}
	if withheld.Candidates[0].Decision != absent.Candidates[0].Decision {
		t.Errorf("withheld answers %q and absent answers %q; the pair is an oracle",
			withheld.Candidates[0].Decision, absent.Candidates[0].Decision)
	}
}

// The visibility caveat rides EVERY answer, with no count. Silence would leave a
// caller believing `unresolved` proves nothing exists, and creating the
// duplicate this tool exists to prevent — while raising it only when something
// WAS withheld would make its presence the disclosure.
func TestTheNarrowedAnswerSaysSoWithoutSizingTheHiddenSet(t *testing.T) {
	e := setupQuery(t)
	seedResolveFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})

	rep1 := e.teamRep(e.Rep1, e.Team1)
	withheld := invokeResolve(rep1, t, registry,
		`{"candidates":[{"kind":"person","emails":["bernd@logistik.example"]}]}`)
	absent := invokeResolve(rep1, t, registry,
		`{"candidates":[{"kind":"person","emails":["nobody@nowhere.example"]}]}`)

	for _, sealed := range []sealedResult{withheld, absent} {
		warned := false
		for _, w := range sealed.Warnings {
			if w.Code != agents.CodeResolutionBoundedByVisibility {
				continue
			}
			warned = true
			if strings.ContainsAny(w.Message, "0123456789") {
				t.Errorf("the caveat sizes what it is about: %q", w.Message)
			}
		}
		if !warned {
			t.Errorf("nothing told the caller their answer is bounded by what they may read: %+v", sealed.Warnings)
		}
	}
	// And the two calls carry the SAME warnings, which is the property: a caveat
	// that appeared only on the withheld one would be the per-address oracle
	// answering `unresolved` exists to close.
	if len(withheld.Warnings) != len(absent.Warnings) {
		t.Errorf("a withheld match carries %d warnings and a genuine miss %d — the difference is the leak",
			len(withheld.Warnings), len(absent.Warnings))
	}
}

// Every record served is SOURCED — it appears in the envelope's evidence, which
// is what the seam read reported.
//
// This proves the read path, NOT the charge: compose.NewRegistry wires no
// ReadCharger (only the api role's Server does), so nothing here would notice a
// metering change. The charging properties are unit-tested against a counting
// charger, and saying so is the point — a comment claiming this test holds them
// would be a gate nobody has.
func TestResolveEntitiesSourcesEveryRecordItServes(t *testing.T) {
	e := setupQuery(t)
	f := seedResolveFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})

	sealed := invokeResolve(e.admin(), t, registry, `{"candidates":[
		{"kind":"person","emails":["anna@acme.example"]},
		{"kind":"person","emails":["bernd@logistik.example"]}]}`)
	answer := resolvePayload(t, sealed.Data)

	if len(answer.Candidates) != 2 {
		t.Fatalf("got %d answers for two candidates", len(answer.Candidates))
	}
	for _, id := range []ids.UUID{f.rep1Person, f.rep3Person} {
		if !sealedNames(sealed, id) {
			t.Errorf("the envelope does not name %s — every record served is a read and is sourced as one", id)
		}
	}
}

func invokeResolve(ctx context.Context, t *testing.T, registry *agents.Registry, args string) sealedResult {
	t.Helper()
	out, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(args))
	if err != nil {
		t.Fatalf("resolve_entities %s\n  → %v", args, err)
	}
	var sealed sealedResult
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatalf("the result is not an envelope: %v (%s)", err, out)
	}
	return sealed
}

func resolvePayload(t *testing.T, payload json.RawMessage) agents.ResolveEntitiesResult {
	t.Helper()
	var answer agents.ResolveEntitiesResult
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("unreadable resolve payload %s: %v", payload, err)
	}
	return answer
}
