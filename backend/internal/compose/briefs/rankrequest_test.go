// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package briefs

// What the L2 ranker's exported surface owes its callers. RankRequest,
// ParseRankOrder and BoundToCandidates are published so the certification lane
// can issue the request this ranker issues and read the reply the way this
// ranker reads it; published surface is a promise, and these are the tests that
// hold it.
//
// The promise that matters most here is an absence: this prompt renders only
// machine-made values, which is why it declares no data boundary. The user turn
// is therefore pinned byte-for-byte — a new field cannot be added to it without
// failing this test, and whoever fixes the test has to decide, right there,
// whether what they are adding is something a counterparty wrote.

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// recordingBrain answers with fixed text and keeps every request it was given,
// which is what lets a test compare what the ranker sent against what the
// exported builder builds.
type recordingBrain struct {
	reply string
	err   error
	seen  []model.Request
}

func (b *recordingBrain) Complete(_ context.Context, req model.Request) (model.Response, error) {
	b.seen = append(b.seen, req)
	if b.err != nil {
		return model.Response{}, b.err
	}
	return model.Response{Text: b.reply}, nil
}

// rankFixtureCandidates are two candidates in the deterministic order the §10.1
// fold hands the ranker: composite descending.
func rankFixtureCandidates() []BriefQueueItem {
	return []BriefQueueItem{
		{
			DealID:      uuidAt(1),
			Composite:   0.825,
			Features:    BriefFeatureVector{Winnability: 0.8, Revenue: 0.75, Timing: 1, Momentum: 1, Warmth: 0.47},
			EvidenceIDs: []ids.UUID{uuidAt(101)},
		},
		{
			DealID:      uuidAt(2),
			Composite:   0.185,
			Features:    BriefFeatureVector{Winnability: 0.25, Revenue: 0, Timing: 0.2, Momentum: 0.4, Warmth: 0.1},
			EvidenceIDs: []ids.UUID{uuidAt(102)},
		},
	}
}

// The prompt is ids and numbers, and nothing else. Pinned whole rather than
// probed, because the claim being made is about what is ABSENT: no deal name,
// no note, no subject line, nothing anybody outside this installation wrote.
// A probe for the values would pass while a free-text field was added beside
// them.
func TestRankRequestRendersOnlyMachineMadeValues(t *testing.T) {
	req := RankRequest(rankFixtureCandidates())

	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("the re-order request has %d messages, want the single user turn", len(req.Messages))
	}
	want := "Candidates:\n" +
		"- 00000000-0000-0000-0000-000000000001: winnability=0.80 revenue=0.75 timing=1.00 momentum=1.00 warmth=0.47 (composite=0.825)\n" +
		"- 00000000-0000-0000-0000-000000000002: winnability=0.25 revenue=0.00 timing=0.20 momentum=0.40 warmth=0.10 (composite=0.185)\n"
	if got := req.Messages[0].Content; got != want {
		t.Errorf("the re-order turn reads\n%q\nwant\n%q", got, want)
	}
	if req.System != briefL2System {
		t.Errorf("the re-order request carries a system prompt this ranker does not declare:\n%s", req.System)
	}
	// The absence is the invariant: a boundary sentence here would name a span
	// that does not exist, and its arrival means untrusted text arrived with it.
	if marker, declared := promptfence.MarkerIn(req.System); declared {
		t.Errorf("the re-order prompt declares the data boundary %q, so something untrusted now reaches it", marker)
	}
	if req.ResponseSchema != nil {
		t.Errorf("the re-order request carries a response schema; BoundToCandidates is this site's shape guarantee")
	}
	if req.MaxTokens != ai.ReasoningOutputMaxTokens {
		t.Errorf("MaxTokens = %d, want the structured-output cap %d", req.MaxTokens, ai.ReasoningOutputMaxTokens)
	}
	if req.SecretStripper == nil {
		t.Error("the re-order request leaves the process without the outbound secret stripper")
	}
}

// The exported functions are the ranker's own path, not a second one beside it.
// A copy would stay green through the change that breaks the original, and this
// is the test that makes a copy impossible to introduce quietly.
func TestReorderRunsTheExportedRankPath(t *testing.T) {
	candidates := rankFixtureCandidates()
	// The model promotes the deterministically-lower deal, which is the whole
	// point of the L2 pass.
	brain := &recordingBrain{reply: `{"order":["` + uuidAt(2).String() + `","` + uuidAt(1).String() + `"]}`}

	got := briefL2Ranker{brain: brain, log: discardBriefLog()}.reorder(context.Background(), candidates)

	if len(brain.seen) != 1 {
		t.Fatalf("the ranker issued %d requests, want the one this site sends", len(brain.seen))
	}
	if !reflect.DeepEqual(brain.seen[0], RankRequest(candidates)) {
		t.Errorf("the request the ranker issued is not the one RankRequest builds:\n%+v", brain.seen[0])
	}
	order, err := ParseRankOrder(brain.reply)
	if err != nil {
		t.Fatalf("reading the reply the ranker was given: %v", err)
	}
	if want := BoundToCandidates(order, candidates); !reflect.DeepEqual(got, want) {
		t.Errorf("the ranker returned %v, want BoundToCandidates' %v", queueDeals(got), queueDeals(want))
	}
}

// A reply this site cannot read is its only refusal, and the refusal degrades
// to the deterministic order rather than failing the rep's morning.
func TestParseRankOrderIsTheOnlyRefusal(t *testing.T) {
	if _, err := ParseRankOrder("I have re-ranked them for you."); err == nil {
		t.Error("prose parsed as an ordered id list")
	}
	order, err := ParseRankOrder("```json\n{\"order\":[\"" + uuidAt(3).String() + "\"]}\n```")
	if err != nil {
		t.Fatalf("a fenced reply is one the product unfences: %v", err)
	}
	if len(order) != 1 || order[0] != uuidAt(3) {
		t.Errorf("parsed order = %v, want the single fenced id", order)
	}

	candidates := rankFixtureCandidates()
	unreadable := &recordingBrain{reply: "I have re-ranked them for you."}
	got := briefL2Ranker{brain: unreadable, log: discardBriefLog()}.reorder(context.Background(), candidates)
	if !reflect.DeepEqual(got, candidates) {
		t.Errorf("an unreadable reply yielded %v, want the deterministic order %v",
			queueDeals(got), queueDeals(candidates))
	}
	unavailable := &recordingBrain{err: errors.New("the model is unreachable")}
	if got := (briefL2Ranker{brain: unavailable, log: discardBriefLog()}).reorder(context.Background(), candidates); !reflect.DeepEqual(got, candidates) {
		t.Errorf("an unavailable model yielded %v, want the deterministic order %v",
			queueDeals(got), queueDeals(candidates))
	}
}

// discardBriefLog keeps the fallback's warning out of the test output; what the
// fallback DID is asserted above, which is the part that matters.
func discardBriefLog() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
