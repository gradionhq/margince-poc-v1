// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the auto-execute version pin.
//
// ADR-0055's claim is that a passport is governed the same way on both doors,
// and the pin is part of what a 🟢 admission means: the tier was resolved by
// reading a record, so the write it admits is conditioned on the version that
// record was read at. On MCP the pin travels as the tool's `if_version`
// argument; here it travels as the request's own If-Match — the header
// redeemIfPresented already forwards for a released 🟡 call, one tier down.
//
// Admission runs through the real gate and the real resolver, in the two lines
// agentGate itself uses, so what these assert about the pin is what the
// middleware produces rather than what a hand-set context claims.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// versionedDeal is a deal sitting in one open stage at one known version — the
// record the tier gate reads to decide that the move may run unattended.
type versionedDeal struct {
	datasource.SystemOfRecordProvider
	stageID ids.UUID
	version int64
}

func (p versionedDeal) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{
		Fields:  json.RawMessage(`{"stage_id":"` + p.stageID.String() + `"}`),
		Version: p.version,
	}, nil
}

// advanceSpec is the registered advance_deal spec, resolved the way the gate
// resolves it: through the generated policy table's op→tool mapping. The
// registry comes back with it because the dispatch this door performs charges
// its counters against that same surface.
func advanceSpec(t *testing.T, deps tierDeps) (*agents.Registry, mcp.ToolSpec) {
	t.Helper()
	reg := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	agents.RegisterCoreTools(reg, deps.records, deps.stages, nil, nil)
	spec, _, ok := operationSpec(agentPolicies["POST /v1/deals/{id}/advance"], reg)
	if !ok {
		t.Fatal("the registry serves no advance_deal spec for the REST twin to admit against")
	}
	return reg, spec
}

// admitOpenMove runs an open→open deal move through the real gate and answers
// the If-Match the downstream contract handler would read, plus the status the
// door answered. These are the two lines agentGate runs between resolving the
// tier input and dispatching.
//
// seen is empty when the door refused, because then no handler ran.
func admitOpenMove(t *testing.T, callerIfMatch string) (seen string, status int) {
	t.Helper()
	deal, stage := ids.NewV7(), ids.NewV7()
	deps := tierDeps{
		stages:  reopenStages{semantics: map[ids.UUID]string{stage: "open"}},
		records: versionedDeal{stageID: stage, version: 12},
	}
	body := []byte(`{"to_stage_id":"` + stage.String() + `"}`)
	r := requestForDeal(t, deal)
	if callerIfMatch != "" {
		r.Header.Set("If-Match", callerIfMatch)
	}
	r = r.WithContext(agentRequestCtx(r.Context()))

	reg, spec := advanceSpec(t, deps)
	ctx, err := auth.NewGate(fullSeat{}).Admit(r.Context(), spec, func() (mcp.TierResolverInput, error) {
		return advanceDealTierInput(r.Context(), deps, agentPolicy{}, r, body)
	})
	if err != nil {
		t.Fatalf("the open→open move was refused: %v", err)
	}
	r = r.WithContext(ctx)

	next := http.HandlerFunc(func(_ http.ResponseWriter, got *http.Request) {
		seen = got.Header.Get("If-Match")
	})
	recorder := httptest.NewRecorder()
	admitAgentCall(recorder, r, next, admissionOutcome{
		pol: agentPolicies["POST /v1/deals/{id}/advance"], spec: spec, registry: reg,
	})
	return seen, recorder.Code
}

// agentRequestCtx is the passport principal the gate governs: a full seat with
// the scope a deal move needs.
func agentRequestCtx(ctx context.Context) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:rest-pin", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeWrite),
	})
}

func TestAnAdmittedAgentWriteCarriesThePinItsTierWasResolvedFrom(t *testing.T) {
	if got, _ := admitOpenMove(t, ""); got != "12" {
		t.Errorf("the handler saw If-Match %q, want 12 — an unpinned 🟢 write lets a deal that "+
			"closed since the tier was resolved be reopened on a decision that no longer holds", got)
	}
}

// The caller may pin its own write, and does — as long as it names the record
// the gate judged.
func TestACallerIfMatchThatNamesTheGatesVersionIsForwardedUnchanged(t *testing.T) {
	if got, status := admitOpenMove(t, "12"); got != "12" || status != http.StatusOK {
		t.Errorf("the handler saw If-Match %q at status %d, want 12 and 200", got, status)
	}
}

// A caller If-Match the gate did not read is refused rather than honoured. The
// dangerous half is a FUTURE version: it names the row the racing close will
// leave behind, so the store's compare passes on the one record the tier
// decision does not describe.
func TestACallerIfMatchTheGateDidNotReadIsRefused(t *testing.T) {
	for name, caller := range map[string]string{
		"a future version the gate never read": "13",
		"a version the caller still holds":     "4",
	} {
		t.Run(name, func(t *testing.T) {
			got, status := admitOpenMove(t, caller)
			if status != http.StatusConflict {
				t.Errorf("status = %d, want 409 — a pin the gate never proved anything about must "+
					"not condition the write it admitted", status)
			}
			if got != "" {
				t.Errorf("the handler ran with If-Match %q despite the refusal", got)
			}
		})
	}
}

// A call whose tier was not decided from a record has nothing to pin, and a
// header invented here would refuse writes for a reason nobody can act on.
func TestAWriteWithNoAdmittedPinIsForwardedUnconditioned(t *testing.T) {
	stage := ids.NewV7()
	reg, spec := advanceSpec(t, tierDeps{
		stages:  reopenStages{semantics: map[ids.UUID]string{stage: "open"}},
		records: versionedDeal{stageID: stage, version: 12},
	})
	// A create carries no id and no record to read, so its tier is static and
	// the gate proved nothing about any row.
	r := httptest.NewRequest(http.MethodPost, "/v1/people", http.NoBody)
	r = r.WithContext(agentRequestCtx(r.Context()))

	var seen string
	next := http.HandlerFunc(func(_ http.ResponseWriter, got *http.Request) {
		seen = got.Header.Get("If-Match")
	})

	admitAgentCall(httptest.NewRecorder(), r, next, admissionOutcome{
		pol: agentPolicies["POST /v1/people"], spec: spec, registry: reg,
	})
	if seen != "" {
		t.Errorf("the handler saw If-Match %q, want none", seen)
	}
}
