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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

func TestTheRESTDoorResolvesTheTierFromBothEndpointsToo(t *testing.T) {
	deal, current, target := ids.NewV7(), ids.NewV7(), ids.NewV7()
	deps := tierDeps{
		stages:  reopenStages{semantics: map[ids.UUID]string{current: "won", target: "open"}},
		records: reopenRecords{stageID: current},
	}

	in, err := advanceDealTierInput(context.Background(), deps, agentPolicy{},
		requestForDeal(t, deal), []byte(`{"to_stage_id":"`+target.String()+`"}`))
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
	deps := tierDeps{stages: reopenStages{}, records: reopenRecords{}}
	if _, err := advanceDealTierInput(context.Background(), deps, agentPolicy{},
		requestForDealRaw(t, "not-a-uuid"), []byte(`{"to_stage_id":"`+ids.NewV7().String()+`"}`)); err == nil {
		t.Error("a path naming no deal was resolved rather than refused")
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
