// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the reopen rule.
//
// A passport is a REST Bearer credential, governed exactly like MCP (ADR-0055),
// so `POST /v1/deals/{id}/advance` reaches the SAME dynamic tier resolver the
// MCP tool does — through a different builder. When only the tool's builder
// learned to read the deal's current stage, this door went on judging a move by
// its destination alone, and an agent could still reopen a won deal here.
//
// That is review-loop rule 1 in one sentence: the invariant had two call sites
// and one was fixed.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// advanceTierInput resolves the tier input the REST door actually builds for a
// deal move: through tierInput, against the real advance_deal policy and the
// real registry spec, so what these assert is what the middleware produces
// rather than what a hand-picked resolver would.
func advanceTierInput(t *testing.T, deps restCommandDeps, r *http.Request, body []byte) (mcp.TierResolverInput, error) {
	t.Helper()
	_, spec := advanceSpec(t, deps)
	if spec.Tier != mcp.TierDynamic {
		t.Fatalf("advance_deal resolved a %v spec — the dynamic path these assert about is not the one this door takes", spec.Tier)
	}
	return tierInput(r.Context(), spec, agentPolicies["POST /v1/deals/{id}/advance"], deps, r, body)()
}

func TestTheRESTDoorResolvesTheTierFromBothEndpointsToo(t *testing.T) {
	deal, current, target := ids.NewV7(), ids.NewV7(), ids.NewV7()
	deps := restCommandDeps{
		stages:  reopenStages{semantics: map[ids.UUID]string{current: "won", target: "open"}},
		records: reopenRecords{stageID: current},
	}

	in, err := advanceTierInput(t, deps, requestForDeal(t, deal), []byte(`{"to_stage_id":"`+target.String()+`"}`))
	if err != nil {
		t.Fatalf("resolving the tier input: %v", err)
	}
	if in.SourceStageSemantic != "won" {
		t.Errorf("source semantic = %q, want the deal's current stage — without it this door "+
			"judges a reopen as an ordinary move to an open stage", in.SourceStageSemantic)
	}
	if in.TargetStageSemantic != "open" {
		t.Errorf("target semantic = %q, want the stage being moved to", in.TargetStageSemantic)
	}
}

// The deal is named by the route rather than the body, so a path carrying
// something that is not an id is refused as the caller's mistake rather than
// resolved against the zero deal.
func TestTheRESTDoorRefusesAPathThatNamesNoDeal(t *testing.T) {
	deps := restCommandDeps{stages: reopenStages{}, records: reopenRecords{}}
	_, err := advanceTierInput(t, deps, requestForDealRaw(t, "not-a-uuid"),
		[]byte(`{"to_stage_id":"`+ids.NewV7().String()+`"}`))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("a path naming no deal resolved to %v, want the existence-hiding refusal every other "+
			"decoder on this door gives a routed id it cannot read", err)
	}
}

// A dynamic tier the command seam cannot answer is REFUSED, never admitted at
// some default. The pairing here is the runtime disagreement the refusal exists
// for: advance_deal's dynamic spec against an operation whose command decodes
// perfectly well and resolves no invocation-time tier.
func TestADynamicSpecWhoseCommandAnswersNoTierIsRefused(t *testing.T) {
	deps := restCommandDeps{stages: reopenStages{}, records: reopenRecords{}}
	_, spec := advanceSpec(t, deps)
	lead := agentPolicies["POST /v1/leads/{id}/promote"]
	if _, described := restCommands[lead.Op]; !described {
		t.Fatalf("%s decodes into no command at all, so this proves nothing about a command that answers no tier", lead.Op)
	}

	r := requestForDeal(t, ids.NewV7())
	resolve := tierInput(r.Context(), spec, lead, deps, r, []byte(`{"trigger":"reply"}`))
	if _, err := resolve(); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("an unanswerable dynamic tier resolved to %v, want a refusal — a gate that cannot tell "+
			"whether a call needs a human must not decide that it does not", err)
	}
}

// The other half of the same fail-closed rule: an operation with no decoder has
// nothing to ask, and is refused rather than admitted ungated.
func TestADynamicSpecWithNoDecoderIsRefused(t *testing.T) {
	deps := restCommandDeps{stages: reopenStages{}, records: reopenRecords{}}
	_, spec := advanceSpec(t, deps)
	unknown := agentPolicy{Op: "anOperationNoDecoderKnows", Access: accessTool, Tool: "advance_deal", Tier: tierDynamic}

	r := requestForDeal(t, ids.NewV7())
	resolve := tierInput(r.Context(), spec, unknown, deps, r, []byte(`{}`))
	if _, err := resolve(); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("an undecodable dynamic operation resolved to %v, want a refusal", err)
	}
}

// The three faults advanceDealCommand tells apart keep three ANSWERS, and the
// answer a client acts on is the machine code — the whole reason this decoder
// does not share commandBody's single malformed_json. Eleven lines of comment
// explain why they differ; this is what makes the explanation checkable.
//
// Two bodies reach the middle answer, because "valid JSON the shape refuses" is
// one fault with two spellings: a to_stage_id that is not a UUID, and a body
// that is not an object at all. Both are fixed by sending an object carrying a
// canonical UUID there, which is why both name that field rather than the body.
func TestTheDealAdvanceDecoderAnswersEachFaultOnItsOwnField(t *testing.T) {
	deps := restCommandDeps{stages: reopenStages{}, records: reopenRecords{}}
	pol := agentPolicies["POST /v1/deals/{id}/advance"]

	for name, tc := range map[string]struct{ body, field, code string }{
		"a body that is not JSON":          {body: `{"to_stage_id":`, field: "body", code: "malformed_json"},
		"a to_stage_id that is not a UUID": {body: `{"to_stage_id":"not-a-uuid"}`, field: "to_stage_id", code: "invalid"},
		"valid JSON that is not an object": {body: `[1,2]`, field: "to_stage_id", code: "invalid"},
		"an object omitting to_stage_id":   {body: `{}`, field: "to_stage_id", code: "required"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := advanceDealCommand(pol, deps, requestForDeal(t, ids.NewV7()), []byte(tc.body))

			var detailed *httperr.DetailedError
			if !errors.As(err, &detailed) {
				t.Fatalf("the decoder answered %v, which carries no field or code for a client to act on", err)
			}
			if len(detailed.Fields) != 1 {
				t.Fatalf("the decoder named %d fields, want exactly the one at fault", len(detailed.Fields))
			}
			if got := detailed.Fields[0]; got.Field != tc.field || got.Code != tc.code {
				t.Errorf("answered %s/%s, want %s/%s — a client branches on the code, so one fault wearing "+
					"another's is a fix the caller cannot find", got.Field, got.Code, tc.field, tc.code)
			}
		})
	}
}

// wellFormedDynamicCalls is one valid request per dynamic-tier operation. It is
// the half the walk below CANNOT derive — a well-formed body is the operation's
// own vocabulary — and the walk fails on a dynamic route missing from it, so the
// half that actually goes stale (which routes are dynamic) is the derived one.
var wellFormedDynamicCalls = map[string]func(t *testing.T, stage ids.UUID) (*http.Request, []byte){
	"advanceDeal": func(t *testing.T, stage ids.UUID) (*http.Request, []byte) {
		t.Helper()
		return requestForDeal(t, ids.NewV7()), []byte(`{"to_stage_id":"` + stage.String() + `"}`)
	},
	// A relink's tier turns on the DESTINATION type, not on any record, so the
	// well-formed call is an ordinary move onto a person — the auto-executing
	// side. The project side is what the pair below proves separately.
	"relinkActivity": func(t *testing.T, _ ids.UUID) (*http.Request, []byte) {
		t.Helper()
		return requestForRelink(t, ids.NewV7()),
			[]byte(`{"entity_type":"person","entity_id":"` + ids.NewV7().String() + `"}`)
	},
	// The batch forms answer the same destination question off the same
	// argument, with no routed id to carry.
	"relinkThread": func(t *testing.T, _ ids.UUID) (*http.Request, []byte) {
		t.Helper()
		return httptest.NewRequest(http.MethodPost, "/v1/activities/relink-thread", http.NoBody),
			[]byte(`{"thread_key":"thread-1","entity_type":"person","entity_id":"` + ids.NewV7().String() + `"}`)
	},
	"relinkActivities": func(t *testing.T, _ ids.UUID) (*http.Request, []byte) {
		t.Helper()
		return httptest.NewRequest(http.MethodPost, "/v1/activities/relink-bulk", http.NoBody),
			[]byte(`{"activity_ids":["` + ids.NewV7().String() + `"],"entity_type":"person","entity_id":"` +
				ids.NewV7().String() + `"}`)
	},
}

// requestForRelink builds a request whose chi route context carries the id the
// real router would have parsed out of /v1/activities/{id}/relink.
func requestForRelink(t *testing.T, activity ids.UUID) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/activities/"+activity.String()+"/relink", http.NoBody)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", activity.String())
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}

// Filing an activity under a PROJECT reaches a human; every other destination
// does not. This is the pair that proves the raise actually happens on the REST
// door — the walk above only proves the route can answer its tier at all, and a
// resolver that answered "auto-execute" for everything would satisfy it.
//
// It matters because the raise is the whole protection: filing under a project
// classifies the correspondence as a Handelsbrief, which is write-once in the
// database and is not lifted by relinking away. An agent that could do that
// unattended could put a six-year retention floor across a mailbox with nothing
// in the product to undo it.
func TestRelinkingOntoAProjectReachesAHumanAndOtherDestinationsDoNot(t *testing.T) {
	stage := ids.NewV7()
	deps := restCommandDeps{
		stages:  reopenStages{semantics: map[ids.UUID]string{stage: "open"}},
		records: versionedDeal{stageID: stage, version: 3},
	}
	reg := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	agents.RegisterCoreTools(reg, deps.records, deps.stages, nil, nil)
	// Nil dependencies are enough: resolving a tier reads the arguments and
	// invokes no handler.
	agents.RegisterLifecycleTools(reg, deps.records, nil, nil, nil)

	pol, described := agentPolicies["POST /v1/activities/{id}/relink"]
	if !described {
		t.Fatal("the relink route carries no agent policy; the gate would not govern it at all")
	}
	spec, _, ok := operationSpec(pol, reg)
	if !ok {
		t.Fatal("the gate resolves no spec for the relink route")
	}

	for _, c := range []struct {
		destination string
		want        mcp.RiskTier
		why         string
	}{
		{
			"project", mcp.TierConfirmationRequired,
			"filing under a project writes an irreversible six-year retention floor",
		},
		{
			"person", mcp.TierAutoExecute,
			"an ordinary association a member can undo by relinking again",
		},
		{
			"deal", mcp.TierAutoExecute,
			"the deal's own stamp is governed at the deal move, not here",
		},
		{
			"not_a_record_type", mcp.TierConfirmationRequired,
			"an unrecognised destination fails toward the approval gate, never away from it",
		},
	} {
		body := []byte(`{"entity_type":"` + c.destination + `","entity_id":"` + ids.NewV7().String() + `"}`)
		in, err := tierInput(context.Background(), spec, pol, deps, requestForRelink(t, ids.NewV7()), body)()
		if err != nil {
			t.Errorf("relink onto %s: the door could not answer its tier: %v", c.destination, err)
			continue
		}
		if got := spec.TierResolver(in); got != c.want {
			t.Errorf("relink onto %s resolved tier %v, want %v — %s", c.destination, got, c.want, c.why)
		}
	}
}

// Every dynamic-tier route must have a command that can ANSWER its tier
// question. A route without one is refused, for every caller and forever — and
// that refusal is indistinguishable from a route an agent was never allowed to
// reach, which is what makes it worth deriving rather than noticing.
//
// This is the derived twin of the two synthetic pairings above: those prove the
// fail-closed refusal fires, this proves it fires only where it should, over the
// routes the real router can actually produce.
func TestEveryDynamicTierRouteHasACommandThatAnswersItsTier(t *testing.T) {
	stage := ids.NewV7()
	deps := restCommandDeps{
		stages:  reopenStages{semantics: map[ids.UUID]string{stage: "open"}},
		records: versionedDeal{stageID: stage, version: 3},
	}
	reg := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	agents.RegisterCoreTools(reg, deps.records, deps.stages, nil, nil)
	// relink_activity is a lifecycle tool, and it is dynamic — the walk below
	// derives the routes to check from the policy table, so a registry missing
	// this set would report the route as unresolvable rather than skip it.
	// Nil dependencies are enough: resolving a tier invokes no handler.
	agents.RegisterLifecycleTools(reg, deps.records, nil, nil, nil)

	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tier != tierDynamic {
			continue
		}
		checked++
		build, described := wellFormedDynamicCalls[pol.Op]
		if !described {
			t.Errorf("%s (%s) declares a dynamic tier and this walk has no well-formed call for it — add "+
				"one, so the route's own tier answer is proved rather than assumed", route, pol.Op)
			continue
		}
		spec, _, ok := operationSpec(pol, reg)
		if !ok {
			t.Errorf("%s (%s): the gate resolves no spec for it", route, pol.Op)
			continue
		}
		r, body := build(t, stage)
		if _, err := tierInput(r.Context(), spec, pol, deps, r, body)(); err != nil {
			t.Errorf("%s (%s) cannot answer its own tier question: %v — a dynamic route whose command "+
				"resolves no tier is refused for every caller", route, pol.Op, err)
		}
	}
	if checked == 0 {
		t.Fatal("no dynamic-tier route in the generated policy — this walk asserted nothing")
	}
}

func requestForDeal(t *testing.T, deal ids.UUID) *http.Request {
	t.Helper()
	return requestForDealRaw(t, deal.String())
}

// requestForDealRaw builds a request whose chi route context carries the id the
// real router would have parsed out of /v1/deals/{id}/advance.
func requestForDealRaw(t *testing.T, id string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/deals/"+id+"/advance", http.NoBody)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}

type reopenStages struct{ semantics map[ids.UUID]string }

func (s reopenStages) StageSemantic(_ context.Context, stageID ids.UUID) (string, ids.UUID, error) {
	return s.semantics[stageID], ids.NewV7(), nil
}

type reopenRecords struct {
	datasource.SystemOfRecordProvider
	stageID ids.UUID
}

func (p reopenRecords) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{Fields: json.RawMessage(`{"stage_id":"` + p.stageID.String() + `"}`)}, nil
}
