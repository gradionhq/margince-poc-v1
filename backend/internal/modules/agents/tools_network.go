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
	// DaysSinceTouch is set on going-cold and absent elsewhere. A pointer
	// rather than a plain int because a zero would read as "touched today" on
	// every finding that says nothing about recency.
	DaysSinceTouch *int `json:"days_since_touch,omitempty"`
}

// IntroRoute is one warm way into an account: a colleague, the contact they
// know there, and how well.
type IntroRoute struct {
	UserID      ids.UUID `json:"user_id"`
	DisplayName string   `json:"display_name"`
	// PersonID and PersonName are the CONTACT the route goes through. An intro
	// suggestion that named only the colleague would leave a rep to ask "an
	// intro to whom" — the pair is the answer, not the colleague alone.
	PersonID        ids.UUID `json:"person_id"`
	PersonName      string   `json:"person_name"`
	Strength        *int     `json:"strength,omitempty"`
	StrengthBucket  string   `json:"strength_bucket"`
	Interactions90d int      `json:"interactions_90d"`
}

// IntroPathLister answers "who here can get me into this account", warmest
// route first. Compose implements it as the fixed two-hop join ADR-0021 pins:
// colleague → contact (the interaction projection) → account (employment).
//
// The bool reports that the CANDIDATE set was cut before ranking, so the
// answer may not contain the warmest route that exists.
type IntroPathLister func(ctx context.Context, orgID ids.UUID) (routes []IntroRoute, candidatesTruncated bool, err error)

// AtRiskDeal is one deal the coverage rules have something to say about.
type AtRiskDeal struct {
	DealID ids.UUID       `json:"deal_id"`
	Name   string         `json:"name"`
	Risks  []CoverageRisk `json:"risks"`
}

// AtRiskReport is the whole answer, INCLUDING how far the scan reached.
//
// DealsScanned and Truncated are not decoration. The scan is capped, and a
// capped answer presented as a complete one is how a model comes to tell a
// sales lead their pipeline is clean when it looked at a quarter of it.
type AtRiskReport struct {
	Deals        []AtRiskDeal `json:"deals"`
	DealsScanned int          `json:"deals_scanned"`
	Truncated    bool         `json:"truncated"`
}

// AtRiskLister answers "which of my relationships are in trouble" over the
// caller's own open deals, under their row scope.
type AtRiskLister func(ctx context.Context) (AtRiskReport, error)

// RegisterNetworkTools wires the relationship-graph intents. A seam that is
// absent registers no tool: a surface that cannot ground its answer does not
// pretend to.
func RegisterNetworkTools(r *Registry, whoKnows WhoKnowsLister, coverage CoverageReader, intro IntroPathLister, atRisk AtRiskLister) {
	if whoKnows != nil {
		r.Register(whoKnowsTool{list: whoKnows})
	}
	if coverage != nil {
		r.Register(accountCoverageTool{read: coverage})
	}
	if intro != nil {
		r.Register(introPathTool{list: intro})
	}
	if atRisk != nil {
		r.Register(atRiskTool{list: atRisk})
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
	if colleagues == nil {
		// An empty LIST, not a null. The documented shape is an array, and a
		// model handed null reads it as "unknown" rather than "nobody".
		colleagues = []KnownColleague{}
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

// --- intro_path_to (🟢 read) ---

type introPathTool struct{ list IntroPathLister }

func (t introPathTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "intro_path_to", Version: toolVersionV1,
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getOrganizationGraph",
		InputSchema: schema(`{"type":"object","properties":{
			"organization_id":{"type":"string","format":"uuid","description":"The account to find a warm route into"}},
			"required":["organization_id"],"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t introPathTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		OrganizationID string `json:"organization_id"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	orgID, err := ids.Parse(args.OrganizationID)
	if err != nil {
		return nil, err
	}
	routes, truncated, err := t.list(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if routes == nil {
		// An empty LIST, not a null: "nobody here has a way in" is the answer
		// that says the account is cold, and it is a useful one. A model handed
		// null reads it as "unknown" and hedges.
		routes = []IntroRoute{}
	}
	return json.Marshal(map[string]any{
		"organization_id": orgID, "routes": routes,
		// Warmth is computed AFTER the read, so an account with more contacts
		// than the fetch bound contributes only the first slice of them and the
		// genuinely warmest route can fall outside it. Saying so is the "no
		// silent caps" rule: a ranked list presented as complete is how a model
		// tells a rep that nobody warmer exists.
		"candidates_truncated": truncated,
	})
}

// --- at_risk_relationships (🟢 read) ---

type atRiskTool struct{ list AtRiskLister }

func (t atRiskTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "at_risk_relationships", Version: toolVersionV1,
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listDeals + getDealCoverage",
		// No arguments. The question is about the caller's own book, and the
		// row scope already decides what that is — an owner or team filter here
		// would be a second, weaker spelling of the same rule.
		InputSchema:  schema(`{"type":"object","properties":{},"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t atRiskTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct{}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	report, err := t.list(ctx)
	if err != nil {
		return nil, err
	}
	if report.Deals == nil {
		report.Deals = []AtRiskDeal{}
	}
	return json.Marshal(report)
}
