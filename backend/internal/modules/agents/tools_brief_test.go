// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The brief is charged per RECORD it names, not per item and not per call.
//
// Its items are contract types rather than datasource.Records, so they ride no
// chokepoint and nothing charges for them by default — which is exactly how a
// densely-joined queue becomes the cheapest bulk read on a surface that charges
// per record (A139). This is the tool that check exists to catch, so the charge
// is asserted here rather than assumed.
func TestTheBriefIsChargedPerItem(t *testing.T) {
	registry, charger, ctx := chargingRegistry(t, readBrief{read: briefOf(3)})

	if _, err := registry.Invoke(ctx, "read_brief", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("invoking read_brief: %v", err)
	}

	// Three deals and the two activity rows each item's ranking cites: a brief
	// names more than one record per item, and metering only the deals would
	// hand the rest of the queue over free.
	if charger.reads() != 9 {
		t.Errorf("charged %d for 3 items naming 3 deals and 6 activity rows, want 9 — metered per "+
			"item, the brief is the cheapest bulk read on the surface", charger.reads())
	}
}

// An empty queue costs nothing. A charge for an answer carrying no record would
// spend a caller's window on the absence of one.
func TestAnEmptyBriefChargesNothing(t *testing.T) {
	registry, charger, ctx := chargingRegistry(t, readBrief{read: briefOf(0)})

	if _, err := registry.Invoke(ctx, "read_brief", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("invoking read_brief: %v", err)
	}

	if charger.reads() != 0 {
		t.Errorf("charged %d for an empty queue, want 0", charger.reads())
	}
}

// The run reaches the caller as the engine reported it: the ranking, the state
// the human left, and the evidence each item rests on. A tool that dropped the
// state would have an agent re-raise what a person already dismissed.
func TestTheServedBriefCarriesTheRunTheEngineAnswered(t *testing.T) {
	tool := readBrief{read: briefOf(2)}

	raw, err := tool.Handle(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("reading the brief: %v", err)
	}
	var result ReadBriefResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("the result is not the shape this tool declares: %v", err)
	}

	if result.CandidateCount != 7 {
		t.Errorf("candidate_count = %d, wanted the run's own 7 — it is what the ranking left out",
			result.CandidateCount)
	}
	if len(result.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(result.Items))
	}
	if result.Items[0].Rank != 1 || result.Items[0].State != "new" {
		t.Errorf("item 0 = %+v, want the first-ranked item with its own queue state", result.Items[0])
	}
	if len(result.Items[0].EvidenceIDs) != 3 {
		t.Errorf("item 0 lost the evidence its ranking rests on: %+v", result.Items[0])
	}
}

// `items` is never null on the wire. An agent reading `null` has to decide
// whether it means "nothing is queued" or "the queue was not read", and only
// one of those is ever true here.
func TestAnEmptyBriefCarriesAnEmptyListRatherThanNull(t *testing.T) {
	raw, err := readBrief{read: briefOf(0)}.Handle(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("reading an empty brief: %v", err)
	}
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Errorf("an empty queue serves null items:\n%s", raw)
	}
}

// A reader that answers a zero-value run still serves an empty LIST. The
// promise belongs to the wire this tool serves, not to whichever seam is
// behind it — and a zero-value result is the shape a seam answering "no queue"
// is most likely to reach for.
func TestAZeroValueRunStillServesAnEmptyList(t *testing.T) {
	tool := readBrief{read: func(context.Context) (ReadBriefResult, error) {
		return ReadBriefResult{}, nil
	}}

	raw, err := tool.Handle(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("reading a zero-value run: %v", err)
	}
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Errorf("a zero-value run serves null items:\n%s", raw)
	}
}

// The tool takes no arguments, and an argument sent anyway is refused rather
// than ignored — a caller asking for someone else's brief must be told the ask
// has no meaning here, not handed their own and left believing otherwise.
func TestTheBriefRefusesAnArgumentItDoesNotDeclare(t *testing.T) {
	reached := false
	tool := readBrief{read: func(context.Context) (ReadBriefResult, error) {
		reached = true
		return ReadBriefResult{}, nil
	}}

	if _, err := tool.Handle(t.Context(), json.RawMessage(`{"user_id":"someone-else"}`)); err == nil {
		t.Fatal("an undeclared argument was accepted")
	}
	if reached {
		t.Error("the call reached the engine before its arguments were refused")
	}
}

// A failure to READ the queue is a failure, not an empty queue. Answering an
// unreachable store with no items would tell a caller their day is clear.
func TestAnUnreadableBriefIsNotServedAsAnEmptyOne(t *testing.T) {
	tool := readBrief{read: func(context.Context) (ReadBriefResult, error) {
		return ReadBriefResult{}, errors.New("dial tcp: connection refused")
	}}

	if _, err := tool.Handle(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("an unreachable brief engine was answered as an empty queue")
	}
}

// An installation whose brief engine is unwired serves no brief tool, rather
// than one that refuses every call — the same conditional registration the
// other injected-engine tools take.
func TestNoBriefEngineMeansNoBriefTool(t *testing.T) {
	registry := NewRegistry(nil, nil)
	RegisterBriefTool(registry, nil)
	if _, registered := registry.Spec("read_brief"); registered {
		t.Error("read_brief is advertised with no engine behind it, so every call would refuse")
	}
}

// briefOf answers a run of n ranked items, each naming its own deal plus the
// two activity rows its ranking cites — the shape the ranker actually builds.
func briefOf(n int) BriefReader {
	return func(context.Context) (ReadBriefResult, error) {
		run := ReadBriefResult{
			BriefID:        ids.NewV7(),
			GeneratedAt:    time.Date(2026, 8, 8, 6, 0, 0, 0, time.UTC),
			AsOf:           time.Date(2026, 8, 8, 5, 0, 0, 0, time.UTC),
			CandidateCount: 7,
			Items:          make([]BriefItem, 0, n),
		}
		for i := range n {
			// The deal appears in its own evidence, exactly as the ranker
			// builds it, so a test cannot pass a shape production never
			// produces — and the deal must not be charged twice for it.
			deal := ids.NewV7()
			run.Items = append(run.Items, BriefItem{
				ItemID: ids.NewV7(), DealID: deal, Rank: i + 1,
				Composite: 0.9, State: "new",
				EvidenceIDs: []ids.UUID{deal, ids.NewV7(), ids.NewV7()},
			})
		}
		return run, nil
	}
}
