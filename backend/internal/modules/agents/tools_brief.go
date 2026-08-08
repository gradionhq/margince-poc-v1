// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The read_brief tool (BYO-TOOL-22, 🟢): the morning brief, readable.
//
// A139 settled the question this closes. Every input to the brief — the deals,
// their activities, the relationships behind them — was already agent-readable,
// so withholding the ASSEMBLED answer while granting all of its parts "is a
// distinction the surface cannot honestly explain". The queue itself stays a
// human surface: acting, dismissing and snoozing an item are how a person
// notices what an agent did, and an agent that curates that queue is reviewing
// itself.
//
// WHOSE BRIEF IT IS. The brief is a personal lens, resolved through the acting
// principal's own user id — and a passport carries the granting human's
// (identity mints it as OnBehalfOf). So an agent reads the brief of the person
// it acts for, never a shared one and never another rep's, and that follows
// from the principal rather than from anything this tool does.
//
// WHY THE METERING IS EXPLICIT HERE. Brief items are contract types, not
// datasource.Records, so they do not ride newWireRecord and NOTHING charges for
// them by default. Metered per call, a densely-joined brief would be the
// cheapest bulk read on a surface that charges per record — the exact failure
// A139 names — so this tool charges one read per item it hands over.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// BriefReader answers the acting human's latest PERSISTED brief run.
//
// It never ranks. The contract is explicit that the read re-reads the
// read-model and the home route never blocks on assembly (B-E05.3b), and an
// agent asking what the queue says must not be the thing that changes it.
//
// ONE thing behind this read does write, and it is worth stating rather than
// discovering: the engine resolves snoozes as it reads, so an item whose snooze
// has expired flips back to actionable in the read's own transaction (A77). It
// is the same entry point the human's own home route calls, the flip is decided
// by the clock rather than by the caller, and reading twice changes nothing the
// first read did not — but it does mean an agent's read can be what materializes
// a state the person then sees.
type BriefReader func(ctx context.Context) (ReadBriefResult, error)

// RegisterBriefTool joins read_brief to the surface once a reader exists — the
// same conditional registration the other injected-engine tools take, so an
// installation whose brief engine is unwired serves no brief tool rather than
// one that refuses every call.
func RegisterBriefTool(r *Registry, read BriefReader) {
	if read == nil {
		return
	}
	r.Register(readBrief{read: read})
}

// ReadBriefResult is one persisted brief run, as the surface serves it.
type ReadBriefResult struct {
	BriefID ids.UUID `json:"brief_id"`
	// GeneratedAt is when the run was assembled and AsOf its data cutoff. Both
	// are on the wire because a queue is only as good as its age, and an agent
	// reading a run from yesterday should be able to say so.
	GeneratedAt time.Time `json:"generated_at"`
	AsOf        time.Time `json:"as_of"`
	// CandidateCount is how many deals cleared the honest-short bar, which may
	// exceed the queue: the difference is what the ranking left out.
	//
	// The run's revenue normalization base is deliberately NOT here. It is the
	// workspace-wide value the revenue FACTOR was divided by, and the factor is
	// already served normalized — so the base explains nothing an agent can act
	// on, and it is the one number on the run that describes the workspace
	// rather than this rep's queue.
	CandidateCount int `json:"candidate_count"`
	// Items is never null on the wire. An agent reading `null` has to decide
	// whether it means "nothing is queued" or "the queue was not read".
	Items []BriefItem `json:"items"`
}

// BriefItem is one queue entry: the deal it is about, why it ranked, and what
// the human has already done with it.
type BriefItem struct {
	ItemID ids.UUID `json:"item_id"`
	DealID ids.UUID `json:"deal_id"`
	// Rank is the position in the queue, 1 first.
	Rank      int     `json:"rank"`
	Composite float64 `json:"composite"`
	// Factors is the decomposition the composite reconciles to, each normalized
	// 0..1. It travels WITH the score because the score without it is the
	// mystery number the brief's own contract exists to forbid: an agent that
	// can only say "this ranked first" restates the queue, while one that can
	// say "it ranked first on momentum and warmth" has told the person
	// something. It is the persisted vector, not a re-derivation.
	Factors BriefFactors `json:"factors"`
	// State is the acting human's own queue state — new, acted, dismissed or
	// snoozed — and StateAt when they left it there. An agent reads both to
	// avoid re-raising what a person has already dealt with, and how long ago
	// is part of that judgement; only that person may change either.
	State   string     `json:"state"`
	StateAt *time.Time `json:"state_at,omitempty"`
	// EvidenceIDs are the rows the ranking rests on, so an answer cites rather
	// than restates: the deal itself, and the activity rows behind momentum and
	// warmth. Each is CHARGED — naming a record to an agent is handing that
	// record over, which is the same rule the intent tools' own id-shaped
	// answers are metered under.
	EvidenceIDs []ids.UUID `json:"evidence_ids"`
	// SnoozedUntil is when a snoozed item re-surfaces, and is absent unless the
	// item is snoozed.
	SnoozedUntil *time.Time `json:"snoozed_until,omitempty"`
}

// BriefFactors is the §10.1 factor decomposition, each normalized 0..1.
type BriefFactors struct {
	Winnability float64 `json:"winnability"`
	Revenue     float64 `json:"revenue"`
	Timing      float64 `json:"timing"`
	Momentum    float64 `json:"momentum"`
	Warmth      float64 `json:"warmth"`
}

type readBrief struct {
	read BriefReader
}

func (t readBrief) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "read_brief", Title: "Read the morning brief", Version: toolVersionV1,
		Description:   readBriefCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getMorningBrief",
		// No arguments. The brief a caller may read is the one belonging to the
		// human they act for, and a parameter naming a user would be an
		// invitation to ask for someone else's.
		InputSchema:  schema(`{"type":"object","properties":{},"additionalProperties":false}`),
		OutputSchema: schemaFor[ReadBriefResult](),
	}
}

func (t readBrief) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct{}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	result, err := t.read(ctx)
	if err != nil {
		return nil, err
	}
	// The empty-list promise is kept HERE rather than by each reader. It is a
	// property of the wire this tool serves, and a reader that answers a
	// zero-value run — the shape a future seam is most likely to return for "no
	// queue" — would otherwise serve `null` under a field documented never to
	// be one.
	if result.Items == nil {
		result.Items = []BriefItem{}
	}
	// The queue is CONTENT built out of material this call did not read the
	// provenance of — deals, their activities, the relationships behind them,
	// folded into a score. Left unsaid, the answer would ride out at t0, the
	// highest tier, and an assembly would have RAISED the trust of everything it
	// was assembled from; much of what a ranking rests on is the capture
	// firehose, which is untrusted by default.
	noteDerivedContent(ctx)
	chargeBriefItems(ctx, result.Items)
	return json.Marshal(result)
}

// chargeBriefItems charges every record the queue NAMES, at the point it is
// handed over.
//
// noteEvidence is the accounting the intent tools already use for a record an
// answer names rather than carries — "naming a record to an agent is handing
// that record over" — and it is why draft_follow_ups_for charges both the deal
// it drafted against and the draft it made. A brief names more than one record
// per item: the deal, and the activity rows its momentum and warmth rest on. A
// queue of ten items citing fifteen rows each is a bulk read whatever it costs
// to serve, and metering only the deals would leave the rest of it free.
//
// The deal appears in its own evidence list — the ranking cites it as the
// source of winnability, revenue and timing — so it is skipped there rather
// than charged twice. The remaining rows are the activity rows the ranker
// gathers; TestBriefEvidenceIsTheDealThenItsActivityRows in the brief engine
// holds that construction, so this is a fact about the ranking rather than an
// assumption made here.
func chargeBriefItems(ctx context.Context, items []BriefItem) {
	for _, item := range items {
		noteEvidence(ctx, datasource.EntityDeal, item.DealID)
		for _, evidence := range item.EvidenceIDs {
			if evidence != item.DealID {
				noteEvidence(ctx, datasource.EntityActivity, evidence)
			}
		}
	}
}
