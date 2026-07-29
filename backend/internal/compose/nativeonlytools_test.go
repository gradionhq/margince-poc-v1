// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Specs for the native-only tool guard: a tool whose only implementation
// reads the native domain tables holds no answer for an overlay workspace,
// whose records live in the incumbent and whose mirror carries no report,
// context-graph, or pipeline-risk projection. Such a tool must answer the
// declared unsupported-by-SoR sentinel (AC-OV-2 / ADR-0018) — querying the
// empty native tables and presenting the result would be a silent break,
// the one failure mode bounded equivalence exists to forbid.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

func overlayMode() sorModeProbe { return func(context.Context) (bool, error) { return true, nil } }
func nativeMode() sorModeProbe  { return func(context.Context) (bool, error) { return false, nil } }
func unresolvableMode() sorModeProbe {
	return func(context.Context) (bool, error) { return false, errors.New("mode read failed") }
}

// --- run_report ---

func TestReportRunnerRefusesInOverlayMode(t *testing.T) {
	called := false
	inner := func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{"rows":[],"total_rows":0}`), nil
	}

	_, err := nativeOnlyReportRunner(overlayMode(), inner)(context.Background(), "deals-by-stage", nil)

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("err = %v, want ErrUnsupportedBySoR", err)
	}
	if called {
		t.Error("the native report engine ran for an overlay workspace — an empty native result would be presented as an answer")
	}
}

func TestReportRunnerServesNativeMode(t *testing.T) {
	inner := func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"total_rows":7}`), nil
	}

	out, err := nativeOnlyReportRunner(nativeMode(), inner)(context.Background(), "deals-by-stage", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if string(out) != `{"total_rows":7}` {
		t.Errorf("out = %s, want the engine's own result", out)
	}
}

func TestReportRunnerRefusesWhenModeCannotBeResolved(t *testing.T) {
	inner := func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		t.Error("the native report engine ran without a resolved system-of-record mode")
		return nil, nil
	}

	if _, err := nativeOnlyReportRunner(unresolvableMode(), inner)(context.Background(), "deals-by-stage", nil); err == nil {
		t.Fatal("err = nil, want the mode-resolution failure")
	}
}

// The REST twin of run_report carries the same refusal: an agent or a
// direct API caller must not receive an empty native report as an answer
// just because the SPA happens to hide the screen in overlay mode.
func TestRunReportOverRESTRefusesInOverlayMode(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/reports/deals-by-stage", nil)

	refuseReportInOverlayMode(rec, req, overlayMode())

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	// The same machine code the tool half answers — a caller must not have to
	// know which transport it used to recognise a declared capability gap.
	if !strings.Contains(rec.Body.String(), "unsupported_by_sor") {
		t.Errorf("body = %s, want the unsupported_by_sor sentinel", rec.Body.String())
	}
}

func TestRunReportOverRESTServesNativeMode(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/reports/deals-by-stage", nil)

	if refused := refuseReportInOverlayMode(rec, req, nativeMode()); refused {
		t.Fatal("a native workspace was refused its own report")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %s — the guard wrote a response for a native workspace", rec.Body.String())
	}
}

// --- catch_me_up_on / prep_for_meeting (the retrieval seam) ---

type recordingRetriever struct {
	searched  bool
	assembled bool
}

func (r *recordingRetriever) Search(context.Context, retrieval.Query) ([]retrieval.Hit, error) {
	r.searched = true
	return nil, nil
}

func (r *recordingRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	r.assembled = true
	return retrieval.Context{}, nil
}

func TestRetrieverRefusesBothVerbsInOverlayMode(t *testing.T) {
	inner := &recordingRetriever{}
	guarded := nativeOnlyRetriever{mode: overlayMode(), inner: inner}
	anchor := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}

	if _, err := guarded.Search(context.Background(), retrieval.Query{Text: "acme"}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("Search err = %v, want ErrUnsupportedBySoR", err)
	}
	if _, err := guarded.AssembleContext(context.Background(), anchor, retrieval.AssembleOptions{}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("AssembleContext err = %v, want ErrUnsupportedBySoR", err)
	}
	if inner.searched || inner.assembled {
		t.Errorf("the native retriever ran for an overlay workspace (searched=%v assembled=%v)", inner.searched, inner.assembled)
	}
}

func TestRetrieverServesNativeMode(t *testing.T) {
	inner := &recordingRetriever{}
	guarded := nativeOnlyRetriever{mode: nativeMode(), inner: inner}

	if _, err := guarded.Search(context.Background(), retrieval.Query{Text: "acme"}); err != nil {
		t.Fatalf("Search err = %v, want nil", err)
	}
	if !inner.searched {
		t.Error("native mode did not reach the retriever")
	}
}

// --- whats_slipping_this_week ---

func TestSlippingListerRefusesInOverlayMode(t *testing.T) {
	called := false
	inner := func(context.Context) ([]agents.SlippingDeal, error) {
		called = true
		return nil, nil
	}

	_, err := nativeOnlySlippingLister(overlayMode(), inner)(context.Background())

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("err = %v, want ErrUnsupportedBySoR", err)
	}
	if called {
		t.Error("the native deals lister ran for an overlay workspace — an empty pipeline would read as nothing slipping")
	}
}

func TestSlippingListerServesNativeMode(t *testing.T) {
	called := false
	inner := func(context.Context) ([]agents.SlippingDeal, error) {
		called = true
		return nil, nil
	}

	if _, err := nativeOnlySlippingLister(nativeMode(), inner)(context.Background()); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !called {
		t.Error("native mode did not reach the deals lister")
	}
}

// TestTheGuardsProbeIgnoresAStaleCachedMode makes the guards' UNCACHED read a
// constraint instead of a convention. sorModeProbe is a plain func type, so
// wiring these guards to Dispatcher.isOverlay would compile and leave every
// behavioural test above green: they all supply a probe directly and so pin
// what a guard does GIVEN an answer, never where the answer comes from. A
// replica holding a pre-flip 'native' entry would then let each guarded tool
// answer out of the empty native tables — the silent break this file exists
// to forbid.
func TestTheGuardsProbeIgnoresAStaleCachedMode(t *testing.T) {
	wsID := ids.NewV7()
	d, calls := cachedModeDispatcher(wsID, false /* cached: native */, true /* stored: overlay */)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)

	before := *calls
	inOverlay, err := nativeOnlyModeProbe(d)(ctx)
	if err != nil {
		t.Fatalf("resolving the mode through the guards' probe: %v", err)
	}
	if !inOverlay {
		t.Error("the probe answered 'native' from the stale cache; a guard must re-read workspace.x_sor_mode")
	}
	if *calls == before {
		t.Error("the probe served the cached mode without paying a workspace-row read")
	}
}
