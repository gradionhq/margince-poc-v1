// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// A ranked sweep is never exhaustive, and the class set is where that is
// enforced rather than remembered: if complete_exact were declarable here, a
// handler could answer it and a caller would read a page as a whole match set.
func TestSearchContextCannotAnswerAnExhaustiveCoverage(t *testing.T) {
	classes := searchContext{}.CoverageClasses()
	if slices.Contains(classes, CoverageCompleteExact) {
		t.Fatalf("search_context publishes %v, which includes the one class a ranked page can never be", classes)
	}
	if !slices.Contains(classes, CoverageRankedSemantic) || !slices.Contains(classes, CoveragePartialDegraded) {
		t.Fatalf("search_context publishes %v, want both classes it can actually answer", classes)
	}
}

// An empty query is named as the argument that is wrong. Passing it to the
// retriever would answer with an empty page, which a caller reads as "there is
// nothing like that here" — a wrong answer where a refusal was owed.
func TestSearchContextRefusesAnEmptyQuery(t *testing.T) {
	for _, args := range []string{`{}`, `{"query":""}`} {
		t.Run(args, func(t *testing.T) {
			tool := searchContext{p: &queryProbeProvider{}, retriever: unreachedRetriever{t: t}}
			_, err := tool.Handle(t.Context(), json.RawMessage(args))
			if err == nil {
				t.Fatal("a call with no query was accepted")
			}
			if !strings.Contains(err.Error(), "query") {
				t.Errorf("the refusal never names `query`: %v", err)
			}
		})
	}
}

// Every hit is READ BACK through the datasource seam. The retrieval lanes are
// row-scoped, but they are a different read from this one — and only the seam
// stamps the trust tier, collects the envelope's evidence and re-applies this
// caller's object RBAC to the record itself.
func TestEverySearchHitIsReadBackThroughTheSeam(t *testing.T) {
	first, second := ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		first:  recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		second: recordAt(datasource.EntityDeal, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), false),
	}}
	found := retrieval.Result{SemanticRanking: true, Hits: []retrieval.Hit{
		{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: first}, Score: 0.91,
			Evidence: []retrieval.Evidence{{Source: "person:" + first.String(), Snippet: "pilot stalled after Q2"}}},
		{Ref: datasource.EntityRef{Type: datasource.EntityDeal, ID: second}, Score: 0.4},
	}}

	result := handleContextSearch(t, provider, found)

	if len(provider.read) != 2 {
		t.Fatalf("the seam was asked for %d records, want one per hit", len(provider.read))
	}
	if len(result.Hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(result.Hits))
	}
	if result.Hits[0].Record.ID != first || result.Hits[0].Score != 0.91 {
		t.Errorf("hit 0 = %+v, want the first ref with its rank score", result.Hits[0])
	}
	if len(result.Hits[0].Excerpts) != 1 || result.Hits[0].Excerpts[0].Snippet != "pilot stalled after Q2" {
		t.Errorf("the excerpt that ranked hit 0 did not survive hydration: %+v", result.Hits[0].Excerpts)
	}
	// The mirror-backed record's tier came off the seam read, not off the hit.
	if result.Hits[1].Record.TrustTier != "external" {
		t.Errorf("hit 1 trust tier = %q, want the tier the seam stamped", result.Hits[1].Record.TrustTier)
	}
	if result.Coverage != CoverageRankedSemantic {
		t.Errorf("coverage = %q, want the ordinary ranked answer", result.Coverage)
	}
	if len(result.Notes) != 0 {
		t.Errorf("an undegraded answer carries notes: %+v", result.Notes)
	}
}

// A hit the caller may not read is DROPPED, the answer says a record was
// withheld, and it never says how many. A count is the size of the hidden set,
// which is exactly what existence-hiding refuses to disclose.
func TestAnUnreadableHitIsDroppedAndCounted_ToNobody(t *testing.T) {
	readable, hidden := ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{
		records: map[ids.UUID]datasource.Record{
			readable: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		},
		fail: map[ids.UUID]error{hidden: apperrors.ErrPermissionDenied},
	}
	found := retrieval.Result{SemanticRanking: true, Hits: []retrieval.Hit{
		{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: hidden}, Score: 0.99},
		{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: readable}, Score: 0.5},
	}}

	result := handleContextSearch(t, provider, found)

	if len(result.Hits) != 1 || result.Hits[0].Record.ID != readable {
		t.Fatalf("hits = %+v, want only the one the caller may read", result.Hits)
	}
	if result.Coverage != CoveragePartialDegraded {
		t.Errorf("coverage = %q, want partial_degraded once a ranked record was withheld", result.Coverage)
	}
	if !hasNote(result.Notes, CodeRowUnreadable) {
		t.Fatalf("nothing told the caller a record was withheld: %+v", result.Notes)
	}
	for _, note := range result.Notes {
		if strings.ContainsAny(note.Detail, "0123456789") {
			t.Errorf("the withheld note carries a number, which sizes the hidden set: %q", note.Detail)
		}
	}
}

// A withheld hit must not be CHARGED. The bound counts records the agent was
// given, and charging for one it was refused would both overcount the caller
// and put a record in the envelope's evidence that the answer does not contain.
func TestAWithheldHitIsNotChargedAgainstTheReadBound(t *testing.T) {
	readable, hidden := ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{
		records: map[ids.UUID]datasource.Record{
			readable: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		},
		fail: map[ids.UUID]error{hidden: apperrors.ErrNotFound},
	}
	tool := searchContext{p: provider, retriever: fixedRetriever{result: retrieval.Result{
		SemanticRanking: true,
		Hits: []retrieval.Hit{
			{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: hidden}},
			{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: readable}},
		},
	}}}
	registry, charger, ctx := chargingRegistry(t, tool)

	if _, err := registry.Invoke(ctx, "search_context", json.RawMessage(`{"query":"churn after a pilot"}`)); err != nil {
		t.Fatalf("invoking search_context: %v", err)
	}

	if charger.charged != 1 {
		t.Errorf("charged %d records for a page of 2 with 1 withheld, want 1", charger.charged)
	}
}

// A ranked sweep is a bulk read and is charged per record, which is the
// property A139 calls load-bearing: metered per CALL, this is the cheapest way
// to read the workspace.
func TestASearchIsChargedPerRecord(t *testing.T) {
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{}}
	found := retrieval.Result{SemanticRanking: true}
	for range 4 {
		id := ids.NewV7()
		provider.records[id] = recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true)
		found.Hits = append(found.Hits, retrieval.Hit{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: id}})
	}
	registry, charger, ctx := chargingRegistry(t, searchContext{p: provider, retriever: fixedRetriever{result: found}})

	if _, err := registry.Invoke(ctx, "search_context", json.RawMessage(`{"query":"anything"}`)); err != nil {
		t.Fatalf("invoking search_context: %v", err)
	}

	if charger.charged != 4 {
		t.Errorf("charged %d for a 4-record page, want 4 — a page must not cost one", charger.charged)
	}
	if charger.calls != 1 {
		t.Errorf("the bound was consulted %d times, want one charge for the whole answer", charger.calls)
	}
}

// A lexically-ranked page SAYS it is lexical. This is the quietest wrong answer
// this tool could give: a caller asking for likeness would read a word-overlap
// list as a meaning-ranked one, and nothing about the page looks wrong.
func TestALexicallyRankedPageSaysSo(t *testing.T) {
	id := ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		id: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	found := retrieval.Result{SemanticRanking: false, Hits: []retrieval.Hit{
		{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: id}},
	}}

	result := handleContextSearch(t, provider, found)

	if result.Coverage != CoveragePartialDegraded {
		t.Errorf("coverage = %q, want partial_degraded on a lexical fallback", result.Coverage)
	}
	if !hasNote(result.Notes, CodeSemanticRankingDegraded) {
		t.Fatalf("the answer never says the ranking fell back: %+v", result.Notes)
	}
	// The hits still come back: a degraded ranking is a weaker answer, never a
	// withheld one.
	if len(result.Hits) != 1 {
		t.Errorf("got %d hits on a degraded ranking, want the lexical page served", len(result.Hits))
	}
}

// The caller's record types reach the retriever, so BOTH hybrid lanes are
// narrowed. Filtering a global page afterwards would spend it on types the
// caller never named.
func TestTheRequestedRecordTypesReachTheRetriever(t *testing.T) {
	probe := &recordingContextRetriever{}
	tool := searchContext{p: &queryProbeProvider{}, retriever: probe}

	if _, err := tool.Handle(t.Context(), json.RawMessage(
		`{"query":"renewal risk","record_types":["deal","organization"],"limit":3}`)); err != nil {
		t.Fatalf("handling the search: %v", err)
	}

	want := []datasource.EntityType{datasource.EntityDeal, datasource.EntityOrganization}
	if !slices.Equal(probe.seen.EntityTypes, want) {
		t.Errorf("the retriever was asked for %v, want the caller's own %v", probe.seen.EntityTypes, want)
	}
	if probe.seen.Text != "renewal risk" || probe.seen.Limit != 3 {
		t.Errorf("query = %+v, want the caller's text and limit carried through", probe.seen)
	}
}

// The published maximum is the one that binds. The seam clamps too, but to its
// own ceiling — and this tool's limit is the unit its caller's read bound is
// spent in, so the number in its schema has to be the number it means.
func TestTheSearchLimitIsResolvedAgainstThePublishedCeiling(t *testing.T) {
	for _, tc := range []struct{ asked, want int }{
		{asked: 0, want: contextSearchDefaultLimit},
		{asked: -4, want: contextSearchDefaultLimit},
		{asked: 3, want: 3},
		{asked: 500, want: contextSearchMaxLimit},
	} {
		if got := contextSearchLimit(tc.asked); got != tc.want {
			t.Errorf("contextSearchLimit(%d) = %d, want %d", tc.asked, got, tc.want)
		}
	}
}

// An excerpt with no text is dropped rather than served empty. The rule on this
// surface is evidence-or-omit, and an empty snippet is a claim to grounding
// that carries none.
func TestAnEmptyExcerptIsNotServedAsEvidence(t *testing.T) {
	id := ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		id: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	found := retrieval.Result{SemanticRanking: true, Hits: []retrieval.Hit{{
		Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: id},
		Evidence: []retrieval.Evidence{
			{Source: "person:" + id.String(), Snippet: ""},
			{Source: "gmail:msg-1", Snippet: "they mentioned the pilot"},
		},
	}}}

	result := handleContextSearch(t, provider, found)

	if len(result.Hits[0].Excerpts) != 1 || result.Hits[0].Excerpts[0].Source != "gmail:msg-1" {
		t.Errorf("excerpts = %+v, want only the one that carries text", result.Hits[0].Excerpts)
	}
}

// A store that cannot be reached is an ERROR, not a partial page. Reporting an
// infrastructure fault as "some records were withheld" would describe it as a
// property of the caller's data, and they would act on the hits that did come.
func TestAnUnreachableStoreFailsTheSearchRatherThanShorteningIt(t *testing.T) {
	id := ids.NewV7()
	provider := &queryProbeProvider{fail: map[ids.UUID]error{id: errors.New("the pool is exhausted")}}
	tool := searchContext{p: provider, retriever: fixedRetriever{result: retrieval.Result{
		SemanticRanking: true,
		Hits:            []retrieval.Hit{{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: id}}},
	}}}

	if _, err := tool.Handle(t.Context(), json.RawMessage(`{"query":"anything"}`)); err == nil {
		t.Fatal("an unreachable store answered a page instead of failing")
	}
}

// An installation with no retriever serves NO search tool, rather than one that
// refuses every call. A tool a client can see and never use is worse than an
// absent one: the catalog is what a model plans against.
func TestNoRetrieverRegistersNoSearchTool(t *testing.T) {
	r := NewRegistry(nil, nil)
	RegisterContextSearchTool(r, &queryProbeProvider{}, nil)

	for _, spec := range r.Specs() {
		if spec.Name == "search_context" {
			t.Fatal("search_context was registered over an absent retriever")
		}
	}
}

// --- helpers ---

func handleContextSearch(t *testing.T, provider datasource.SystemOfRecordProvider, found retrieval.Result) SearchContextResult {
	t.Helper()
	tool := searchContext{p: provider, retriever: fixedRetriever{result: found}}
	raw, err := tool.Handle(t.Context(), json.RawMessage(`{"query":"churn after a pilot"}`))
	if err != nil {
		t.Fatalf("handling the search: %v", err)
	}
	var result SearchContextResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("the result is not the shape this tool declares: %v", err)
	}
	return result
}

// fixedRetriever answers one prepared page. AssembleContext is never reached by
// search_context, and says so by failing rather than by returning nothing.
type fixedRetriever struct{ result retrieval.Result }

func (r fixedRetriever) Search(context.Context, retrieval.Query) (retrieval.Result, error) {
	return r.result, nil
}

func (fixedRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	return retrieval.Context{}, errors.New("search_context must not assemble a context")
}

// recordingContextRetriever keeps the query it was handed, so a test can assert
// what actually crossed the seam rather than what the tool was asked for.
type recordingContextRetriever struct{ seen retrieval.Query }

func (r *recordingContextRetriever) Search(_ context.Context, q retrieval.Query) (retrieval.Result, error) {
	r.seen = q
	return retrieval.Result{SemanticRanking: true}, nil
}

func (*recordingContextRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	return retrieval.Context{}, errors.New("search_context must not assemble a context")
}

// unreachedRetriever fails the test if a refused call reaches the seam anyway.
type unreachedRetriever struct{ t *testing.T }

func (r unreachedRetriever) Search(context.Context, retrieval.Query) (retrieval.Result, error) {
	r.t.Error("the retriever was reached by a call that should have been refused")
	return retrieval.Result{}, nil
}

func (r unreachedRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	r.t.Error("the retriever was reached by a call that should have been refused")
	return retrieval.Context{}, nil
}
