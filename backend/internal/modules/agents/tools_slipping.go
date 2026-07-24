// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The pipeline-risk intent tools (interfaces.md §2.2): a salesperson asks
// "what's slipping?" and gets a RANKED, evidence-carrying set of at-risk
// deals — never a row dump — and can batch-draft follow-ups over the same
// set without anything leaving the workspace. Both compose over injected
// seams: the module never reads deal rows itself, and every returned item
// carries the evidence that grounds it — a deal whose risk cannot be
// evidenced from its own fields is absent, not guessed.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// SlippingDeal is one candidate at-risk deal as the lister saw it: the
// raw flags plus the fields that evidence them. The TOOL decides what is
// presentable — a flag without its grounding field is dropped here.
type SlippingDeal struct {
	DealID            ids.UUID
	Name              string
	AmountMinor       *int64
	Currency          *string
	Stalled           bool
	CloseOverdue      bool
	LastActivityAt    *time.Time
	CreatedAt         time.Time
	ExpectedCloseDate *time.Time
}

// SlippingLister serves the row-scoped candidate set (formulas §8:
// stalled deals plus overdue close dates); compose implements it over
// the deals module's list path so RBAC and row scope apply unchanged.
type SlippingLister func(ctx context.Context) ([]SlippingDeal, error)

// FollowUpDrafter drafts one follow-up for a slipping deal and persists
// it as a draft activity on the deal's timeline — a proposal, never a
// send. Compose implements it over the same deterministic draft voice
// draft_email uses and the same provider write path every tool rides.
type FollowUpDrafter func(ctx context.Context, deal SlippingDeal) (draftActivityID ids.UUID, summary string, err error)

// RegisterSlippingTools wires the pipeline-risk intents. No lister, no
// tools — a surface that cannot ground does not pretend to; the drafting
// tool additionally needs somewhere for its drafts to land.
func RegisterSlippingTools(r *Registry, list SlippingLister, draft FollowUpDrafter) {
	if list == nil {
		return
	}
	r.Register(whatsSlippingThisWeek{list: list})
	if draft != nil {
		r.Register(draftFollowUpsFor{list: list, draft: draft})
	}
}

// --- whats_slipping_this_week (🟢 read) ---

type whatsSlippingThisWeek struct {
	list SlippingLister
}

func (t whatsSlippingThisWeek) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "whats_slipping_this_week", Version: "1.0.0",
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listDeals",
		InputSchema: schema(`{"type":"object","properties":{
			"limit":{"type":"integer","minimum":1,"maximum":50,"description":"Cap the ranked set; omit for the full evidenced set"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t whatsSlippingThisWeek) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Limit int `json:"limit"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	candidates, err := t.list(ctx)
	if err != nil {
		return nil, err
	}
	ranked := rankSlipping(candidates)
	if args.Limit > 0 && len(ranked) > args.Limit {
		ranked = ranked[:args.Limit]
	}
	items := make([]map[string]any, 0, len(ranked))
	for i, it := range ranked {
		items = append(items, it.wire(i+1))
	}
	return json.Marshal(map[string]any{"deals": items})
}

// --- draft_follow_ups_for (🟢 draft — proposes, never sends) ---

type draftFollowUpsFor struct {
	list  SlippingLister
	draft FollowUpDrafter
}

func (t draftFollowUpsFor) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "draft_follow_ups_for", Version: "1.0.0",
		RequiredScope: principal.ScopeDraft, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listDeals + draftEmail + logActivity",
		InputSchema: schema(`{"type":"object","required":["segment"],"properties":{
			"segment":{"type":"string","enum":["slipping"],"description":"The deal set to draft follow-ups for; drafts land on each deal's timeline and are NEVER sent"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t draftFollowUpsFor) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Segment string `json:"segment"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if args.Segment != "slipping" {
		return nil, &BadArgsError{Cause: fmt.Errorf("segment %q is not a known deal segment (want \"slipping\")", args.Segment)}
	}
	candidates, err := t.list(ctx)
	if err != nil {
		return nil, err
	}
	// Draft only over the evidenced set: a deal that would not appear in
	// whats_slipping_this_week gets no follow-up either.
	ranked := rankSlipping(candidates)
	drafts := make([]map[string]any, 0, len(ranked))
	for _, it := range ranked {
		activityID, summary, err := t.draft(ctx, it.deal)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, map[string]any{
			"deal_id":           it.deal.DealID,
			"draft_activity_id": activityID,
			"summary":           summary,
			"evidence":          it.evidence,
		})
	}
	return json.Marshal(map[string]any{"segment": args.Segment, "drafts": drafts})
}

// --- the shared ranking + evidence gate ---

type slippingItem struct {
	deal      SlippingDeal
	idleSince *time.Time
	evidence  []map[string]string
}

func (it slippingItem) wire(rank int) map[string]any {
	out := map[string]any{
		"rank":     rank,
		"deal_id":  it.deal.DealID,
		"name":     it.deal.Name,
		"evidence": it.evidence,
	}
	if it.deal.AmountMinor != nil {
		out["amount_minor"] = *it.deal.AmountMinor
	}
	if it.deal.Currency != nil {
		out["currency"] = *it.deal.Currency
	}
	return out
}

// rankSlipping applies the no-guess gate and the deterministic order.
// Evidence rule: a stalled claim must ground on the idle-since timestamp
// (last_activity_at, else created_at), an overdue claim on the close
// date; a candidate whose flags survive neither is dropped. Order: idle
// longest first, then amount descending, then id — stable and clock-free.
func rankSlipping(candidates []SlippingDeal) []slippingItem {
	items := make([]slippingItem, 0, len(candidates))
	for _, d := range candidates {
		it := slippingItem{deal: d, idleSince: idleSince(d)}
		if d.Stalled && it.idleSince != nil {
			source := "deal.last_activity_at"
			if d.LastActivityAt == nil {
				source = "deal.created_at"
			}
			it.evidence = append(it.evidence, map[string]string{
				"source":  source,
				"snippet": "no recorded activity since " + it.idleSince.UTC().Format("2006-01-02"),
			})
		}
		if d.CloseOverdue && d.ExpectedCloseDate != nil {
			it.evidence = append(it.evidence, map[string]string{
				"source":  "deal.expected_close_date",
				"snippet": "expected close " + d.ExpectedCloseDate.UTC().Format("2006-01-02") + " is past due",
			})
		}
		if len(it.evidence) == 0 {
			continue
		}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch {
		case a.idleSince != nil && b.idleSince != nil && !a.idleSince.Equal(*b.idleSince):
			return a.idleSince.Before(*b.idleSince)
		case (a.idleSince != nil) != (b.idleSince != nil):
			return a.idleSince != nil
		}
		if av, bv := amountOrZero(a.deal), amountOrZero(b.deal); av != bv {
			return av > bv
		}
		return a.deal.DealID.String() < b.deal.DealID.String()
	})
	return items
}

// idleSince is the same idle base IsStalled uses (formulas §8.1):
// last_activity_at when recorded, else created_at. A candidate with
// neither has no idle claim to make.
func idleSince(d SlippingDeal) *time.Time {
	if d.LastActivityAt != nil {
		return d.LastActivityAt
	}
	if !d.CreatedAt.IsZero() {
		created := d.CreatedAt
		return &created
	}
	return nil
}

func amountOrZero(d SlippingDeal) int64 {
	if d.AmountMinor == nil {
		return 0
	}
	return *d.AmountMinor
}
