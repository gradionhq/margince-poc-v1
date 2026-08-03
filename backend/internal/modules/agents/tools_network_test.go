// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The relationship-graph tools. They are thin over injected seams, and the
// things worth pinning are the decisions they make around the seam: what an
// empty answer means, what an unregistered seam means, and whether the tier
// matches what the tool actually does.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

func TestNetworkToolsAreReadTierAndNeedOnlyReadScope(t *testing.T) {
	// They name people and change nothing. A tool that reported a warm intro
	// under a write tier would ask a human to approve a question.
	for _, spec := range []mcp.ToolSpec{
		whoKnowsTool{}.Spec(), accountCoverageTool{}.Spec(),
		introPathTool{}.Spec(), atRiskTool{}.Spec(),
	} {
		if spec.Tier != mcp.TierAutoExecute {
			t.Errorf("%s is tier %v, want auto-execute — it reads", spec.Name, spec.Tier)
		}
		if spec.RequiredScope != principal.ScopeRead {
			t.Errorf("%s requires %v, want read scope", spec.Name, spec.RequiredScope)
		}
	}
}

func TestAnAbsentSeamRegistersNoTool(t *testing.T) {
	// A surface that cannot ground its answer does not pretend to: a role
	// wired without the reader must not advertise a tool that always errors.
	r := NewRegistry(nil, nil)
	RegisterNetworkTools(r, nil, nil, nil, nil)
	for _, name := range []string{"who_knows", "account_coverage", "intro_path_to", "at_risk_relationships"} {
		if _, found := r.Spec(name); found {
			t.Errorf("%s registered with no seam behind it", name)
		}
	}
}

func TestNobodyKnowsThemIsAnAnswerNotAnError(t *testing.T) {
	// "The account is cold" is true, useful, and exactly what a rep needs to
	// hear. Returning an error would make the model narrate a malfunction.
	tool := whoKnowsTool{list: func(context.Context, ids.UUID) ([]KnownColleague, error) {
		return nil, nil
	}}
	out, err := tool.Handle(context.Background(), json.RawMessage(`{"person_id":"`+ids.NewV7().String()+`"}`))
	if err != nil {
		t.Fatalf("an empty network answered an error: %v", err)
	}
	var payload struct {
		Colleagues []KnownColleague `json:"colleagues"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unreadable payload: %v", err)
	}
	if len(payload.Colleagues) != 0 {
		t.Errorf("expected an empty colleague list, got %d", len(payload.Colleagues))
	}
}

func TestWhoKnowsRefusesAMalformedPersonID(t *testing.T) {
	// The seam is never reached with a bad id: a tool that forwarded garbage
	// would turn a typo into a database error.
	reached := false
	tool := whoKnowsTool{list: func(context.Context, ids.UUID) ([]KnownColleague, error) {
		reached = true
		return nil, nil
	}}
	if _, err := tool.Handle(context.Background(), json.RawMessage(`{"person_id":"not-a-uuid"}`)); err == nil {
		t.Error("a malformed person_id was accepted")
	}
	if reached {
		t.Error("the seam was called with an unparsed id")
	}
}

func TestTheSeamsErrorReachesTheCaller(t *testing.T) {
	// A denied read must surface as a failure, not as an empty answer — an
	// agent told "nobody knows them" when it was actually refused would report
	// a cold account instead of a permission problem.
	denied := errors.New("permission denied")
	tool := whoKnowsTool{list: func(context.Context, ids.UUID) ([]KnownColleague, error) {
		return nil, denied
	}}
	_, err := tool.Handle(context.Background(), json.RawMessage(`{"person_id":"`+ids.NewV7().String()+`"}`))
	if !errors.Is(err, denied) {
		t.Errorf("the seam's error became %v; a refusal must not read as an empty network", err)
	}
}

func TestNoWarmRouteIsAnAnswerNotAnError(t *testing.T) {
	// Same rule as who_knows, and it matters more here: "nobody here can get
	// you in" is the finding that says go cold-outbound, and an error would
	// make the model report a malfunction instead.
	tool := introPathTool{list: func(context.Context, ids.UUID) ([]IntroRoute, bool, error) {
		return nil, false, nil
	}}
	out, err := tool.Handle(context.Background(), json.RawMessage(`{"organization_id":"`+ids.NewV7().String()+`"}`))
	if err != nil {
		t.Fatalf("an account with no warm route answered an error: %v", err)
	}
	var payload struct {
		Routes []IntroRoute `json:"routes"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unreadable payload: %v", err)
	}
	if payload.Routes == nil {
		t.Error("routes came back null; a model reads null as unknown, not as nobody")
	}
	if len(payload.Routes) != 0 {
		t.Errorf("expected no routes, got %d", len(payload.Routes))
	}
}

func TestAnIntroRouteNamesBothTheColleagueAndTheContact(t *testing.T) {
	// A route that named only the colleague leaves a rep asking "an intro to
	// whom" — the pair is the answer.
	colleague, contact := ids.NewV7(), ids.NewV7()
	tool := introPathTool{list: func(context.Context, ids.UUID) ([]IntroRoute, bool, error) {
		return []IntroRoute{{
			UserID: colleague, DisplayName: "Anna Weber",
			PersonID: contact, PersonName: "Jonas Bach",
			StrengthBucket: "strong", Interactions90d: 12,
		}}, false, nil
	}}
	out, err := tool.Handle(context.Background(), json.RawMessage(`{"organization_id":"`+ids.NewV7().String()+`"}`))
	if err != nil {
		t.Fatalf("intro path: %v", err)
	}
	var payload struct {
		Routes []IntroRoute `json:"routes"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unreadable payload: %v", err)
	}
	if len(payload.Routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(payload.Routes))
	}
	if payload.Routes[0].UserID != colleague || payload.Routes[0].PersonID != contact {
		t.Errorf("the route lost one of its two ends: %+v", payload.Routes[0])
	}
	if payload.Routes[0].PersonName == "" {
		t.Error("the route names the contact by id alone; a rep cannot act on a uuid")
	}
}

func TestTheAtRiskSweepReportsThatItWasCappedRatherThanLookingClean(t *testing.T) {
	// A capped sweep presented as a complete one is how a model comes to tell a
	// sales lead their pipeline is fine when it read a quarter of it.
	tool := atRiskTool{list: func(context.Context) (AtRiskReport, error) {
		return AtRiskReport{Deals: nil, DealsScanned: 25, Truncated: true}, nil
	}}
	out, err := tool.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("at-risk sweep: %v", err)
	}
	var got AtRiskReport
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable payload: %v", err)
	}
	if !got.Truncated {
		t.Error("the sweep hit its cap and the answer does not say so")
	}
	if got.DealsScanned != 25 {
		t.Errorf("deals_scanned = %d, want 25 — the reach of the sweep is part of the answer", got.DealsScanned)
	}
	if got.Deals == nil {
		t.Error("deals came back null; an empty sweep is an empty list")
	}
}

func TestCoverageForwardsTheFindingsWithTheirEvidence(t *testing.T) {
	// A risk kind without its ids is a red dot nobody can act on.
	deal := ids.NewV7()
	person := ids.NewV7()
	tool := accountCoverageTool{read: func(context.Context, ids.UUID) (DealCoverageAnswer, error) {
		return DealCoverageAnswer{
			DealID: deal,
			Risks: []CoverageRisk{{
				Kind: "single_threaded_theirs", Summary: "one relationship",
				PersonIDs: []ids.UUID{person},
			}},
		}, nil
	}}
	out, err := tool.Handle(context.Background(), json.RawMessage(`{"deal_id":"`+deal.String()+`"}`))
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	var got DealCoverageAnswer
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable payload: %v", err)
	}
	if len(got.Risks) != 1 || got.Risks[0].Kind != "single_threaded_theirs" {
		t.Fatalf("risks did not survive the round trip: %+v", got.Risks)
	}
	if len(got.Risks[0].PersonIDs) != 1 || got.Risks[0].PersonIDs[0] != person {
		t.Errorf("the finding lost the record behind it: %+v", got.Risks[0])
	}
}
