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
	// State is the acting human's own queue state — new, acted, dismissed or
	// snoozed. An agent reads it to avoid re-raising what a person has already
	// dealt with; only that person may change it.
	State string `json:"state"`
	// EvidenceIDs are the rows the ranking rests on, so an answer cites rather
	// than restates. They are references, not content: reading one is its own
	// call through read_record, and charges its own record then.
	EvidenceIDs []ids.UUID `json:"evidence_ids"`
	// SnoozedUntil is when a snoozed item re-surfaces, and is absent unless the
	// item is snoozed.
	SnoozedUntil *time.Time `json:"snoozed_until,omitempty"`
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
	// One charge per item, at the point the items are handed over. noteEvidence
	// is the same accounting the intent tools use for a record they NAME rather
	// than carry: naming a deal to an agent is handing that deal over, and a
	// queue of them is a bulk read however few bytes it costs to serve.
	for _, item := range result.Items {
		noteEvidence(ctx, datasource.EntityDeal, item.DealID)
	}
	return json.Marshal(result)
}
