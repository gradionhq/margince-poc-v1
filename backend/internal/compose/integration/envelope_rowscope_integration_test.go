// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// AC-MCP-8: an empty result and a withheld result are different answers.
//
// This is the one behavioural claim in the v1 envelope, and it is here rather
// than in a unit test because the thing that has to be true is a property of two
// principals over ONE corpus — the same rows, the same query, two answers that
// must not read the same and must not disclose how they differ. A unit test with
// a stubbed provider would be asserting the arrangement.
//
// The failure it exists to prevent is specific: an agent tells a person a record
// does not exist, when it does and they simply may not see it. And the fix must
// not become the disclosure it replaces — the fact of filtering rides the
// envelope, the SIZE of what was filtered never does.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestABoundedCallerIsToldTheAnswerIsBoundedAndNeverHowMuch(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})

	// One corpus: three people Rep1 owns, none of them Rep3's and none of them
	// on Rep3's team. Three rather than one, because a count that leaked would
	// be indistinguishable from a boolean at one row.
	for _, name := range []string{"Withheld Alpha", "Withheld Beta", "Withheld Gamma"} {
		e.SeedPerson(t, name, &e.Rep1)
	}

	const query = `{"q":"Withheld","record_type":"person"}`
	bounded := invokeForEnvelope(e.As(e.Rep3, []ids.UUID{e.Team2}, RepPerms), t, registry, query)
	unbounded := invokeForEnvelope(e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms), t, registry, query)

	// The two answers must differ, and this is the difference that matters: the
	// bounded caller sees nothing AND is told that it is looking through a
	// bound, so "nothing I can see" and "nothing exists" stop rendering alike.
	if got := recordCount(t, bounded.Data); got != 0 {
		t.Fatalf("the bounded caller read %d of another team's people — this suite is not testing what it claims", got)
	}
	if got := recordCount(t, unbounded.Data); got != 3 {
		t.Fatalf("the unbounded caller read %d people, want the 3 seeded — the corpus is not what the bounded arm was denied", got)
	}
	if !carriesWarning(bounded, "row_scope_filtered") {
		t.Errorf("the bounded caller's empty answer carries no row_scope_filtered warning: %v — "+
			"an agent reading it will report that no such person exists", bounded.Warnings)
	}
	if carriesWarning(unbounded, "row_scope_filtered") {
		t.Error("the unbounded caller's answer claims filtering, so no answer on this surface can ever mean 'nothing exists'")
	}

	// And the half that makes it safe: nothing in the bounded answer says how
	// many rows the bound removed. A count is precisely the side channel
	// existence-hiding closes, so every part of the answer that could CARRY one
	// is read — the payload, the evidence list and the warning text together, not
	// the warning alone. The two fields left out are the ones whose digits are
	// the server's own and say nothing about the corpus: the trace id it minted
	// and the version the tool declares.
	answering, err := json.Marshal(struct {
		Data     json.RawMessage `json:"data"`
		Evidence any             `json:"evidence"`
		Warnings any             `json:"warnings"`
	}{Data: bounded.Data, Evidence: bounded.Evidence, Warnings: bounded.Warnings})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(answering), "0123456789") {
		t.Errorf("the withheld answer carries a number: %s — the fact of filtering may ride the envelope, its size may not",
			answering)
	}
}

// invokeForEnvelope runs search_records as one principal and reads the sealed
// result back the way a client does.
func invokeForEnvelope(ctx context.Context, t *testing.T, registry *agents.Registry, args string) sealedResult {
	t.Helper()
	out, err := registry.Invoke(ctx, "search_records", json.RawMessage(args))
	if err != nil {
		t.Fatalf("search_records: %v", err)
	}
	var sealed sealedResult
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatalf("the result is not an envelope: %v (%s)", err, out)
	}
	return sealed
}

func carriesWarning(sealed sealedResult, code string) bool {
	for _, warning := range sealed.Warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func recordCount(t *testing.T, payload json.RawMessage) int {
	t.Helper()
	var answer struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("unreadable search payload %s: %v", payload, err)
	}
	return len(answer.Records)
}
