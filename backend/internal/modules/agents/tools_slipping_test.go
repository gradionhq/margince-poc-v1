// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Golden-fixture specs for the pipeline-risk intents: the output is a
// ranked set where EVERY item carries the evidence that grounds it, and
// a candidate whose risk flags cannot be evidenced from its own fields
// is absent — dropped, never guessed.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func fixedDate(day int) time.Time {
	return time.Date(2026, time.June, day, 0, 0, 0, 0, time.UTC)
}

func int64Ptr(v int64) *int64 { return &v }

// slippingFixture: two evidenced deals (one idle longer, one richer with
// both stalled and overdue evidence) and two flagged-but-ungrounded ones
// the no-guess gate must drop.
func slippingFixture() (older, richer, flagNoIdle, overdueNoDate SlippingDeal) {
	olderIdle := fixedDate(1)
	richerIdle := fixedDate(10)
	closeDate := fixedDate(15)
	older = SlippingDeal{
		DealID: ids.MustParse("00000000-0000-7000-8000-00000000000a"), Name: "Acme renewal",
		AmountMinor: int64Ptr(100_00), Stalled: true, LastActivityAt: &olderIdle, CreatedAt: fixedDate(1),
	}
	richer = SlippingDeal{
		DealID: ids.MustParse("00000000-0000-7000-8000-00000000000b"), Name: "Globex expansion",
		AmountMinor: int64Ptr(900_00), Stalled: true, CloseOverdue: true,
		LastActivityAt: &richerIdle, CreatedAt: fixedDate(2), ExpectedCloseDate: &closeDate,
	}
	flagNoIdle = SlippingDeal{
		DealID: ids.MustParse("00000000-0000-7000-8000-00000000000c"), Name: "Ghost deal",
		Stalled: true, // no last_activity_at and a zero created_at: nothing grounds the claim
	}
	overdueNoDate = SlippingDeal{
		DealID: ids.MustParse("00000000-0000-7000-8000-00000000000d"), Name: "Dateless deal",
		CloseOverdue: true, CreatedAt: fixedDate(3), // flagged overdue with no close date to show
	}
	return older, richer, flagNoIdle, overdueNoDate
}

type slippingWire struct {
	Deals []struct {
		Rank        int    `json:"rank"`
		DealID      string `json:"deal_id"`
		Name        string `json:"name"`
		AmountMinor *int64 `json:"amount_minor"`
		Evidence    []struct {
			Source  string `json:"source"`
			Snippet string `json:"snippet"`
		} `json:"evidence"`
	} `json:"deals"`
}

func TestWhatsSlippingReturnsRankedEvidencedSetAndDropsTheUngrounded(t *testing.T) {
	older, richer, flagNoIdle, overdueNoDate := slippingFixture()
	tool := whatsSlippingThisWeek{list: func(context.Context) ([]SlippingDeal, error) {
		return []SlippingDeal{richer, flagNoIdle, older, overdueNoDate}, nil
	}}

	raw, err := tool.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var out slippingWire
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}

	if len(out.Deals) != 2 {
		t.Fatalf("got %d deals, want 2 — the ungrounded candidates must be absent, not guessed", len(out.Deals))
	}
	if out.Deals[0].DealID != older.DealID.String() || out.Deals[1].DealID != richer.DealID.String() {
		t.Fatalf("rank order = [%s %s], want longest-idle first [%s %s]",
			out.Deals[0].DealID, out.Deals[1].DealID, older.DealID, richer.DealID)
	}
	for _, d := range out.Deals {
		if d.Rank == 0 || len(d.Evidence) == 0 {
			t.Fatalf("deal %s (rank %d) has %d evidence entries — every returned item must be grounded", d.DealID, d.Rank, len(d.Evidence))
		}
		for _, ev := range d.Evidence {
			if ev.Source == "" || ev.Snippet == "" {
				t.Fatalf("deal %s carries empty evidence %+v", d.DealID, ev)
			}
		}
	}
	// The richer deal is doubly at risk: both claims present, each grounded.
	if got := len(out.Deals[1].Evidence); got != 2 {
		t.Fatalf("stalled+overdue deal carries %d evidence entries, want 2 (idle-since and past-due close)", got)
	}
}

func TestWhatsSlippingLimitCapsTheRankedSet(t *testing.T) {
	older, richer, _, _ := slippingFixture()
	tool := whatsSlippingThisWeek{list: func(context.Context) ([]SlippingDeal, error) {
		return []SlippingDeal{richer, older}, nil
	}}
	raw, err := tool.Handle(context.Background(), json.RawMessage(`{"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var out slippingWire
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Deals) != 1 || out.Deals[0].DealID != older.DealID.String() {
		t.Fatalf("limit=1 must keep the top-ranked deal, got %+v", out.Deals)
	}
}

func TestDraftFollowUpsForDraftsOnlyTheEvidencedSegmentAndNeverSends(t *testing.T) {
	older, _, flagNoIdle, _ := slippingFixture()
	var drafted []ids.UUID
	draftID := ids.MustParse("00000000-0000-7000-8000-0000000000ff")
	tool := draftFollowUpsFor{
		list: func(context.Context) ([]SlippingDeal, error) {
			return []SlippingDeal{older, flagNoIdle}, nil
		},
		draft: func(_ context.Context, deal SlippingDeal) (ids.UUID, string, error) {
			drafted = append(drafted, deal.DealID)
			return draftID, "Re: " + deal.Name, nil
		},
	}

	raw, err := tool.Handle(context.Background(), json.RawMessage(`{"segment":"slipping"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Segment string `json:"segment"`
		Drafts  []struct {
			DealID          string `json:"deal_id"`
			DraftActivityID string `json:"draft_activity_id"`
			Summary         string `json:"summary"`
			Evidence        []struct {
				Source  string `json:"source"`
				Snippet string `json:"snippet"`
			} `json:"evidence"`
		} `json:"drafts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}

	if len(drafted) != 1 || drafted[0] != older.DealID {
		t.Fatalf("drafter ran for %v, want exactly the evidenced deal %s — an ungrounded deal gets no draft", drafted, older.DealID)
	}
	if len(out.Drafts) != 1 {
		t.Fatalf("got %d drafts, want 1", len(out.Drafts))
	}
	d := out.Drafts[0]
	if d.DealID != older.DealID.String() || d.DraftActivityID != draftID.String() || d.Summary != "Re: Acme renewal" {
		t.Fatalf("draft item %+v does not carry deal, draft activity and summary", d)
	}
	if len(d.Evidence) == 0 {
		t.Fatal("a batch draft must carry the evidence that put its deal in the segment")
	}
}

func TestDraftFollowUpsForRefusesAnUnknownSegment(t *testing.T) {
	tool := draftFollowUpsFor{
		list: func(context.Context) ([]SlippingDeal, error) {
			t.Fatal("the lister must not run for a segment the tool does not serve")
			return nil, nil
		},
		draft: func(context.Context, SlippingDeal) (ids.UUID, string, error) {
			t.Fatal("nothing may be drafted for an unknown segment")
			return ids.Nil, "", nil
		},
	}
	var bad *BadArgsError
	if _, err := tool.Handle(context.Background(), json.RawMessage(`{"segment":"everything"}`)); !errors.As(err, &bad) {
		t.Fatalf("unknown segment → %v, want BadArgsError", err)
	}
}

func TestRegisterSlippingToolsWithoutAListerRegistersNothing(t *testing.T) {
	r := NewRegistry(nil, nil)
	RegisterSlippingTools(r, nil, nil)
	if got := len(r.Specs()); got != 0 {
		t.Fatalf("a surface without a lister must stay empty, got %d tools", got)
	}
}
