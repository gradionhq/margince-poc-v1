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
