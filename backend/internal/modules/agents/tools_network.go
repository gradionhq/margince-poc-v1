// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The relationship-graph intent tools (ADR-0078): a rep asks "who here knows
// them" or "how is this deal covered" and gets a ranked, evidence-carrying
// answer rather than a row dump.
//
// Like every tool in this module they compose over injected seams — agents
// never reads a record table itself, so RBAC, row scope and capture privacy
// apply exactly as they do on the HTTP path, and there is no second
// enforcement to drift from the first.
//
// Both are 🟢 read-tier. They propose nothing and change nothing; naming a
// colleague who could make an introduction is information, not an action.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// KnownColleague is one of our people's relationship with one contact, as the
// seam reports it: who, how warm, and the counts that ground the warmth.
type KnownColleague struct {
	UserID      ids.UUID `json:"user_id"`
	DisplayName string   `json:"display_name"`
	// Strength is nil when the band is "none" — never spoken is not a score
	// of zero, and rendering it as one would tell a rep a relationship decayed
	// when none ever existed.
	Strength        *int   `json:"strength,omitempty"`
	StrengthBucket  string `json:"strength_bucket"`
	Interactions90d int    `json:"interactions_90d"`
}

// WhoKnowsLister answers "which colleagues know this contact", warmest first.
// Compose implements it over the interaction projection through the same
// row-scoped read the HTTP surface uses.
type WhoKnowsLister func(ctx context.Context, personID ids.UUID) ([]KnownColleague, error)

// CoverageReader answers "how is this deal covered, and what is wrong with
// it". Compose implements it over compose/network.
type CoverageReader func(ctx context.Context, dealID ids.UUID) (DealCoverageAnswer, error)

// DealCoverageAnswer is the coverage picture in the shape a model consumes:
// the seats, who carries them, and the findings with their evidence.
type DealCoverageAnswer struct {
	DealID       ids.UUID         `json:"deal_id"`
	Stakeholders []CoverageSeat   `json:"stakeholders"`
	OurSide      []KnownColleague `json:"our_side"`
	Risks        []CoverageRisk   `json:"risks"`
}

// CoverageSeat is one stakeholder and whether the seat is a relationship.
type CoverageSeat struct {
	PersonID ids.UUID `json:"person_id"`
	Role     string   `json:"role"`
	Engaged  bool     `json:"engaged"`
}

// CoverageRisk is one finding. Kind names the RULE, so a model explaining the
// flag quotes a definition rather than inventing a rationale for it.
type CoverageRisk struct {
	Kind      string     `json:"kind"`
	Summary   string     `json:"summary"`
	PersonIDs []ids.UUID `json:"person_ids,omitempty"`
	UserIDs   []ids.UUID `json:"user_ids,omitempty"`
}

// RegisterNetworkTools wires the relationship-graph intents. A seam that is
// absent registers no tool: a surface that cannot ground its answer does not
// pretend to.
func RegisterNetworkTools(r *Registry, whoKnows WhoKnowsLister, coverage CoverageReader) {
	if whoKnows != nil {
		r.Register(whoKnowsTool{list: whoKnows})
	}
	if coverage != nil {
		r.Register(accountCoverageTool{read: coverage})
	}
}

// --- who_knows (🟢 read) ---

type whoKnowsTool struct{ list WhoKnowsLister }

func (t whoKnowsTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "who_knows", Version: toolVersionV1,
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getPersonNetwork",
		InputSchema: schema(`{"type":"object","properties":{
			"person_id":{"type":"string","format":"uuid","description":"The contact to ask about"}},
			"required":["person_id"],"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t whoKnowsTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		PersonID string `json:"person_id"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	personID, err := ids.Parse(args.PersonID)
	if err != nil {
		return nil, err
	}
	colleagues, err := t.list(ctx, personID)
	if err != nil {
		return nil, err
	}
	// An empty answer is returned as an empty list, not an error. "Nobody here
	// knows them" is a true and useful answer to this question — it is the
	// answer that says the account is cold — and turning it into a failure
	// would make the model narrate a problem instead of a fact.
	return json.Marshal(map[string]any{
		"person_id": personID, "colleagues": colleagues,
	})
}

// --- account_coverage (🟢 read) ---

type accountCoverageTool struct{ read CoverageReader }

func (t accountCoverageTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "account_coverage", Version: toolVersionV1,
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getDealCoverage",
		InputSchema: schema(`{"type":"object","properties":{
			"deal_id":{"type":"string","format":"uuid","description":"The deal to assess"}},
			"required":["deal_id"],"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t accountCoverageTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		DealID string `json:"deal_id"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	dealID, err := ids.Parse(args.DealID)
	if err != nil {
		return nil, err
	}
	answer, err := t.read(ctx, dealID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(answer)
}
