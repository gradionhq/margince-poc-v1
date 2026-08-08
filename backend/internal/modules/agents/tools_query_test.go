// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// A plan a caller left out is named as the ARGUMENT that is missing. Handing
// nothing to the grammar would answer with a refusal about a malformed plan,
// sending the caller to read the vocabulary for a mistake that was never in it.
func TestQueryWorkspaceNamesTheMissingPlanArgument(t *testing.T) {
	for _, args := range []string{`{}`, `{"plan":null}`} {
		t.Run(args, func(t *testing.T) {
			tool := queryWorkspace{p: &queryProbeProvider{}, run: unreachedRunner(t)}
			_, err := tool.Handle(t.Context(), json.RawMessage(args))
			if err == nil {
				t.Fatal("a call with no plan was accepted")
			}
			if !strings.Contains(err.Error(), "plan") {
				t.Errorf("the refusal never names `plan`: %v", err)
			}
		})
	}
}

// The tool does not interpret the plan — it carries it. An empty object is a
// document the caller wrote, so it reaches the grammar, which is the only thing
// that can say which members it is missing. Interpreting it here would put a
// second, quieter validator in front of the published one.
func TestAPlanDocumentReachesTheGrammarVerbatim(t *testing.T) {
	const written = `{"version":"v1","target":"deal","where":[{"field":"status","op":"eq","value":"open"}]}`
	var seen json.RawMessage
	tool := queryWorkspace{p: &queryProbeProvider{}, run: func(_ context.Context, plan json.RawMessage) (QueryAnswer, error) {
		seen = plan
		return QueryAnswer{Coverage: CoverageCompleteExact}, nil
	}}

	for _, plan := range []string{`{}`, written} {
		if _, err := tool.Handle(t.Context(), json.RawMessage(`{"plan":`+plan+`}`)); err != nil {
			t.Fatalf("handling %s: %v", plan, err)
		}
		if string(seen) != plan {
			t.Errorf("the grammar was given %s, want the caller's own %s", seen, plan)
		}
	}
}

// Rows do not become records by being marshalled — every one is READ back
// through the datasource seam, which is where the trust tier is stamped, the
// envelope's freshness and evidence are collected, and the caller's RBAC and
// row scope are re-applied. A tool that assembled rows itself would answer with
// an envelope claiming nothing had been read.
func TestEveryAdmittedRefIsReadBackThroughTheSeam(t *testing.T) {
	first, second, org := ids.NewV7(), ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		first:  recordAt(datasource.EntityDeal, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		second: recordAt(datasource.EntityDeal, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), true),
		org:    recordAt(datasource.EntityOrganization, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	answer := QueryAnswer{
		Refs: []QueryRef{
			{Type: "deal", ID: first, Score: 0.9, Evidence: []QueryEvidence{{
				Relation: "organization_id", RecordType: "organization", ID: org, Title: "Kärcher",
			}}},
			{Type: "deal", ID: second},
		},
		Coverage: CoverageRankedSemantic, Limit: 25, Narrative: "Deals ranked by similarity.",
	}

	result := handleQuery(t, provider, answer)

	// Three: the two rows, and the hop record behind the first. The hop is a
	// record the answer rests on, so it is read like one.
	if len(provider.read) != 3 {
		t.Fatalf("the seam was asked for %d records, want 3 — one per admitted ref plus the hop", len(provider.read))
	}
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(result.Rows))
	}
	if result.Rows[0].Record.ID != first || result.Rows[0].Score != 0.9 {
		t.Errorf("row 0 = %+v, want the first ref with its rank score", result.Rows[0])
	}
	if len(result.Rows[0].Evidence) != 1 || result.Rows[0].Evidence[0].Title != "Kärcher" {
		t.Errorf("the hop that admitted row 0 did not survive hydration: %+v", result.Rows[0].Evidence)
	}
	if result.Coverage != CoverageRankedSemantic || result.ExecutedPlan != "Deals ranked by similarity." {
		t.Errorf("coverage/narrative = %q/%q, want them carried verbatim", result.Coverage, result.ExecutedPlan)
	}
}

// A page of rows sharing one hop reads that hop ONCE. Without the cache, 200
// deals at one organization would read the same organization 200 times over.
func TestRowsSharingAHopReadItOnce(t *testing.T) {
	org := ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		org: recordAt(datasource.EntityOrganization, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	answer := QueryAnswer{Coverage: CoverageCompleteExact, Limit: 25}
	for range 3 {
		id := ids.NewV7()
		provider.records[id] = recordAt(datasource.EntityDeal, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true)
		answer.Refs = append(answer.Refs, QueryRef{Type: "deal", ID: id, Evidence: []QueryEvidence{{
			Relation: "organization_id", RecordType: "organization", ID: org, Title: "Kärcher",
		}}})
	}

	result := handleQuery(t, provider, answer)

	if len(result.Rows) != 3 {
		t.Fatalf("got %d rows, want the 3 that share a hop", len(result.Rows))
	}
	var hopReads int
	for _, ref := range provider.read {
		if ref.ID == org {
			hopReads++
		}
	}
	if hopReads != 1 {
		t.Errorf("the shared hop was read %d times, want once", hopReads)
	}
}

// A hop that can no longer be read takes its row with it. Serving the row alone
// would tell the caller a deal sits at an organization they may not know
// exists — the disclosure the hop's own row scope refused at selection time.
func TestARowWhoseHopBecameUnreadableIsDropped(t *testing.T) {
	deal, org := ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{
		records: map[ids.UUID]datasource.Record{
			deal: recordAt(datasource.EntityDeal, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		},
		fail: map[ids.UUID]error{org: apperrors.ErrPermissionDenied},
	}

	result := handleQuery(t, provider, QueryAnswer{
		Refs: []QueryRef{{Type: "deal", ID: deal, Evidence: []QueryEvidence{{
			Relation: "organization_id", RecordType: "organization", ID: org, Title: "Kärcher",
		}}}},
		Coverage: CoverageCompleteExact, Limit: 25,
	})

	if len(result.Rows) != 0 {
		t.Fatalf("got %d rows, want the row dropped with its unreadable hop", len(result.Rows))
	}
	if result.Coverage != CoveragePartialDegraded || !hasNote(result.Notes, CodeRowUnreadable) {
		t.Errorf("coverage=%q notes=%+v, want a degraded answer that says a record was dropped",
			result.Coverage, result.Notes)
	}
}

// A mirror-backed hop is labelled where the caller reads it. Evidence is a
// reason to act, so it carries the same trust label the record it names does.
func TestAMirrorBackedHopIsMarkedExternal(t *testing.T) {
	deal, org := ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		deal: recordAt(datasource.EntityDeal, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		org:  recordAt(datasource.EntityOrganization, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), false),
	}}

	result := handleQuery(t, provider, QueryAnswer{
		Refs: []QueryRef{{Type: "deal", ID: deal, Evidence: []QueryEvidence{{
			Relation: "organization_id", RecordType: "organization", ID: org, Title: "Kärcher",
		}}}},
		Coverage: CoverageCompleteExact, Limit: 25,
	})

	if len(result.Rows) != 1 {
		t.Fatalf("got %d rows, want the deal with its mirror-backed hop", len(result.Rows))
	}
	if got := result.Rows[0].Evidence[0].TrustTier; got != "external" {
		t.Errorf("the hop's trust_tier = %q, want %q — its title is content and reads as a reason to act", got, "external")
	}
}

// How many records a caller may not read is the SIZE of what was withheld, and
// stating it is the side channel existence-hiding exists to close. That it
// happened is what the caller needs.
func TestTheDroppedRowNoteNeverStatesHowMany(t *testing.T) {
	kept := ids.NewV7()
	provider := &queryProbeProvider{
		records: map[ids.UUID]datasource.Record{
			kept: recordAt(datasource.EntityDeal, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		},
		fail: map[ids.UUID]error{},
	}
	answer := QueryAnswer{Refs: []QueryRef{{Type: "deal", ID: kept}}, Coverage: CoverageCompleteExact, Limit: 25}
	for range 3 {
		gone := ids.NewV7()
		provider.fail[gone] = apperrors.ErrPermissionDenied
		answer.Refs = append(answer.Refs, QueryRef{Type: "deal", ID: gone})
	}

	result := handleQuery(t, provider, answer)

	if len(result.Rows) != 1 || !hasNote(result.Notes, CodeRowUnreadable) {
		t.Fatalf("rows=%d notes=%+v, want one row and the dropped-record note", len(result.Rows), result.Notes)
	}
	for _, note := range result.Notes {
		if strings.ContainsAny(note.Detail, "0123456789") {
			t.Errorf("the note states a number: %q — the fact of a drop may be reported, its size may not", note.Detail)
		}
	}
}

// A mirror-backed record taints the row it becomes, at the one place that taint
// is applied. Reaching the seam is not enough — the answer has to CARRY what
// the seam said about where the record came from.
func TestAMirrorBackedRowIsMarkedExternal(t *testing.T) {
	mirrored := ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		mirrored: recordAt(datasource.EntityOrganization, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), false),
	}}

	result := handleQuery(t, provider, QueryAnswer{
		Refs:     []QueryRef{{Type: "organization", ID: mirrored}},
		Coverage: CoverageCompleteExact, Limit: 25,
	})

	if got := result.Rows[0].Record.TrustTier; got != "external" {
		t.Errorf("trust_tier = %q, want %q for a record the mirror answered", got, "external")
	}
}

// The world moves between selection and hydration. A row that can no longer be
// read is dropped — and SAID SO, because a short answer still labelled
// complete_exact is the silent narrowing this whole feature exists to prevent.
func TestARowThatCannotBeReadBackIsDroppedAndSaidSo(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"archived between the two reads", apperrors.ErrNotFound},
		{"authority narrowed between the two reads", apperrors.ErrPermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, gone := ids.NewV7(), ids.NewV7()
			provider := &queryProbeProvider{
				records: map[ids.UUID]datasource.Record{
					kept: recordAt(datasource.EntityDeal, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
				},
				fail: map[ids.UUID]error{gone: fmt.Errorf("reading the deal: %w", tc.err)},
			}

			result := handleQuery(t, provider, QueryAnswer{
				Refs:     []QueryRef{{Type: "deal", ID: kept}, {Type: "deal", ID: gone}},
				Coverage: CoverageCompleteExact, Limit: 25,
			})

			if len(result.Rows) != 1 || result.Rows[0].Record.ID != kept {
				t.Fatalf("got %d rows, want only the readable one", len(result.Rows))
			}
			if result.Coverage != CoveragePartialDegraded {
				t.Errorf("coverage = %q, want %q — an answer missing a matched row is not complete",
					result.Coverage, CoveragePartialDegraded)
			}
			if !hasNote(result.Notes, CodeRowUnreadable) {
				t.Errorf("the dropped row left no note: %+v", result.Notes)
			}
		})
	}
}

// A failure to REACH a verdict is not a verdict. Reporting an unreachable
// database as a partial answer would describe an infrastructure fault as a
// property of the caller's data, and the caller would act on the rows that did
// come back.
func TestAFailureToReachAVerdictIsNotReportedAsAPartialAnswer(t *testing.T) {
	unreachable := ids.NewV7()
	provider := &queryProbeProvider{fail: map[ids.UUID]error{
		unreachable: errors.New("dial tcp: connection refused"),
	}}
	tool := queryWorkspace{p: provider, run: func(context.Context, json.RawMessage) (QueryAnswer, error) {
		return QueryAnswer{
			Refs:     []QueryRef{{Type: "deal", ID: unreachable}},
			Coverage: CoverageCompleteExact,
		}, nil
	}}

	if _, err := tool.Handle(t.Context(), json.RawMessage(`{"plan":{"version":"v1","target":"deal"}}`)); err == nil {
		t.Fatal("an unreachable store was answered as a partial result")
	}
}

// The coverage vocabulary is CLOSED, and the handler holds it closed. A class
// the tool does not publish reaching a client is how a word whose meaning is
// frozen acquires a fourth meaning nobody ratified.
func TestQueryWorkspaceRefusesACoverageClassItDoesNotPublish(t *testing.T) {
	tool := queryWorkspace{p: &queryProbeProvider{}, run: func(context.Context, json.RawMessage) (QueryAnswer, error) {
		return QueryAnswer{Coverage: "exhaustive"}, nil
	}}

	_, err := tool.Handle(t.Context(), json.RawMessage(`{"plan":{"version":"v1","target":"deal"}}`))
	if err == nil {
		t.Fatal("an unpublished coverage class was served to a client")
	}
	if !strings.Contains(err.Error(), "exhaustive") {
		t.Errorf("the failure does not name the class it refused: %v", err)
	}
}

// `notes` and `evidence` are never null on the wire. An agent reading `null`
// has to decide whether it means "none" or "not computed", and only one of
// those is ever true here.
func TestAnEmptyAnswerCarriesEmptyListsRatherThanNulls(t *testing.T) {
	tool := queryWorkspace{p: &queryProbeProvider{}, run: func(context.Context, json.RawMessage) (QueryAnswer, error) {
		return QueryAnswer{Coverage: CoverageCompleteExact, Limit: 25}, nil
	}}

	raw, err := tool.Handle(t.Context(), json.RawMessage(`{"plan":{"version":"v1","target":"deal"}}`))
	if err != nil {
		t.Fatalf("handling an empty answer: %v", err)
	}
	for _, member := range []string{`"rows":[]`, `"notes":[]`} {
		if !strings.Contains(string(raw), member) {
			t.Errorf("the empty answer does not carry %s:\n%s", member, raw)
		}
	}

	// And a row-BEARING answer, because `evidence` is per row and an empty
	// answer has no row to carry one. It is checked in the raw bytes: decoded
	// into the struct, a nil slice and an empty one are the same value, so the
	// assertion that matters can only be made against the wire.
	withRow := ids.NewV7()
	rowed := queryWorkspace{
		p: &queryProbeProvider{records: map[ids.UUID]datasource.Record{
			withRow: recordAt(datasource.EntityDeal, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		}},
		run: func(context.Context, json.RawMessage) (QueryAnswer, error) {
			return QueryAnswer{
				Refs:     []QueryRef{{Type: "deal", ID: withRow}},
				Coverage: CoverageCompleteExact, Limit: 25,
			}, nil
		},
	}
	raw, err = rowed.Handle(t.Context(), json.RawMessage(`{"plan":{"version":"v1","target":"deal"}}`))
	if err != nil {
		t.Fatalf("handling a plan with no traversal: %v", err)
	}
	if !strings.Contains(string(raw), `"evidence":[]`) {
		t.Errorf("a row from a plan with no hop carries null evidence rather than an empty list:\n%s", raw)
	}
}

// The description is the only thing a model reads before choosing this tool,
// and the plan it then writes is only answerable if it read the vocabulary
// first. Both the pointer and the three classes have to survive rendering.
func TestTheCopyPointsAtThePublishedVocabularyAndItsClasses(t *testing.T) {
	described := queryWorkspaceCopy.render()
	if !strings.Contains(described, "margince://schema/query") {
		t.Error("the description never names the resource that publishes the grammar")
	}
	for _, class := range (queryWorkspace{}).CoverageClasses() {
		if !strings.Contains(described, class) {
			t.Errorf("the description never says what %q means, so a caller cannot act on it", class)
		}
	}
}

// --- probes ---

// handleQuery drives one answer through the tool and decodes what it put on the
// wire, so every assertion is about the SERVED shape rather than about an
// intermediate the client never sees.
func handleQuery(t *testing.T, provider datasource.SystemOfRecordProvider, answer QueryAnswer) QueryWorkspaceResult {
	t.Helper()
	tool := queryWorkspace{p: provider, run: func(context.Context, json.RawMessage) (QueryAnswer, error) {
		return answer, nil
	}}
	raw, err := tool.Handle(t.Context(), json.RawMessage(`{"plan":{"version":"v1","target":"deal"}}`))
	if err != nil {
		t.Fatalf("handling the plan: %v", err)
	}
	var result QueryWorkspaceResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("the result is not the shape this tool declares: %v", err)
	}
	return result
}

func hasNote(notes []QueryNote, code string) bool {
	for _, note := range notes {
		if note.Code == code {
			return true
		}
	}
	return false
}

// unreachedRunner fails the test if the plan reaches the executor, which is how
// an argument refusal proves it happened BEFORE the work rather than instead of
// reporting it.
func unreachedRunner(t *testing.T) QueryRunner {
	t.Helper()
	return func(context.Context, json.RawMessage) (QueryAnswer, error) {
		t.Error("a call with no plan reached the executor")
		return QueryAnswer{}, nil
	}
}

// queryProbeProvider answers Read from a fixed set, and records what it was
// asked for. Only Read is implemented — the embedded nil interface is the
// tree's established way of saying that anything else is outside this probe's
// contract, and a call to one panics rather than passing quietly.
type queryProbeProvider struct {
	datasource.SystemOfRecordProvider
	records map[ids.UUID]datasource.Record
	fail    map[ids.UUID]error
	read    []datasource.EntityRef
}

func (p *queryProbeProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	p.read = append(p.read, ref)
	if err, ok := p.fail[ref.ID]; ok {
		return datasource.Record{}, err
	}
	record, ok := p.records[ref.ID]
	if !ok {
		return datasource.Record{}, apperrors.ErrNotFound
	}
	record.Ref = ref
	return record, nil
}

// A query is charged PER RECORD, not per call — including the hop records its
// evidence rests on.
//
// This is the property A139 calls the load-bearing half: a densely-joined
// answer is the cheapest bulk read on a surface that charges per call, and
// query_workspace is the densest read this surface has. It comes out of routing
// every record through newWireRecord rather than out of anything this tool
// does, which is exactly why it is asserted here — a future rewrite that
// assembled rows itself would serve them for free.
func TestAQueryIsChargedPerRecordIncludingItsHops(t *testing.T) {
	org := ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		org: recordAt(datasource.EntityOrganization, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	answer := QueryAnswer{Coverage: CoverageCompleteExact, Limit: 25}
	for range 3 {
		id := ids.NewV7()
		provider.records[id] = recordAt(datasource.EntityDeal, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true)
		answer.Refs = append(answer.Refs, QueryRef{Type: "deal", ID: id, Evidence: []QueryEvidence{{
			Relation: "organization_id", RecordType: "organization", ID: org, Title: "Kärcher",
		}}})
	}
	tool := queryWorkspace{p: provider, run: func(context.Context, json.RawMessage) (QueryAnswer, error) {
		return answer, nil
	}}
	registry, charger, ctx := chargingRegistry(t, tool)

	if _, err := registry.Invoke(ctx, "query_workspace", json.RawMessage(`{"plan":{"version":"v1","target":"deal"}}`)); err != nil {
		t.Fatalf("invoking query_workspace: %v", err)
	}

	// Three rows and the one organization they were all admitted through: the
	// hop is served content too, so it is counted once, not three times and
	// not zero.
	if charger.charged != 4 {
		t.Errorf("charged %d records for 3 rows sharing 1 hop, want 4", charger.charged)
	}
	if charger.calls != 1 {
		t.Errorf("the bound was consulted %d times, want one charge for the whole answer", charger.calls)
	}
}
