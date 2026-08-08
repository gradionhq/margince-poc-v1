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
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// The canned answers these specs supply, over the package's one
// overlayModeChecker stub (fakeMode, overlaywrite_test.go).
func overlayMode() overlayModeChecker { return &fakeMode{overlay: true} }
func nativeMode() overlayModeChecker  { return &fakeMode{} }
func unresolvableMode() overlayModeChecker {
	return &fakeMode{err: errModeUnresolvable}
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

func (r *recordingRetriever) Search(context.Context, retrieval.Query) (retrieval.Result, error) {
	r.searched = true
	return retrieval.Result{}, nil
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

// --- list_pipelines ---

func TestPipelineListerRefusesInOverlayMode(t *testing.T) {
	called := false
	inner := func(context.Context) ([]agents.Pipeline, error) {
		called = true
		return nil, nil
	}

	_, err := nativeOnlyPipelines(overlayMode(), inner)(context.Background())

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("err = %v, want ErrUnsupportedBySoR", err)
	}
	if called {
		t.Error("the native pipeline read ran for an overlay workspace — the native tables hold none " +
			"of its configuration, so the answer would be 'this workspace has no pipelines'")
	}
}

func TestPipelineListerServesNativeMode(t *testing.T) {
	called := false
	inner := func(context.Context) ([]agents.Pipeline, error) {
		called = true
		return nil, nil
	}

	if _, err := nativeOnlyPipelines(nativeMode(), inner)(context.Background()); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !called {
		t.Error("native mode did not reach the pipeline read")
	}
}

func TestPipelineListerRefusesWhenModeCannotBeResolved(t *testing.T) {
	// An unresolved mode refuses rather than defaulting to native: guessing
	// wrong in that direction is the silent break the guard exists to stop.
	inner := func(context.Context) ([]agents.Pipeline, error) {
		t.Error("the native pipeline read ran without a resolved system-of-record mode")
		return nil, nil
	}

	if _, err := nativeOnlyPipelines(unresolvableMode(), inner)(context.Background()); err == nil {
		t.Fatal("err = nil, want the mode-resolution failure")
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

// TestSlippingGuardRefusesOnAStaleNativeCache drives a real guard against a real
// Dispatcher, which the specs above cannot: they hand in a canned answer, so
// they pin what a guard does GIVEN a mode, never that the mode it consults is
// the fresh one. Here the process holds a pre-flip 'native' entry while the
// workspace row already says overlay — the second-replica state, since
// Invalidate reaches only the process that committed the flip.
//
// What it pins is that a guard honours the fresh answer, NOT that the read
// targets the right row: queryMode is stubbed and ignores its workspace id, so
// a read of the WRONG workspace would still pass here. That half is carried by
// the integration pair — an overlay workspace must refuse, a native one must
// not — which drives the real SQL.
func TestSlippingGuardRefusesOnAStaleNativeCache(t *testing.T) {
	wsID := ids.NewV7()
	d, calls := cachedModeDispatcher(wsID, modeNative)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)

	ranNative := false
	guarded := nativeOnlySlippingLister(d, func(context.Context) ([]agents.SlippingDeal, error) {
		ranNative = true
		return nil, nil
	})

	_, err := guarded(ctx)

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("err = %v, want ErrUnsupportedBySoR — the guard answered from the stale 'native' entry", err)
	}
	if ranNative {
		t.Error("the native deals lister ran for an overlay workspace: an empty pipeline reads as nothing slipping")
	}
	if *calls == 0 {
		t.Error("the guard never re-read workspace.x_sor_mode")
	}
}

// Every nativeOnly… guard in this file must name the tool(s) it guards, and the
// wiring pin must cover exactly those tools.
//
// The unit specs above prove what a guard DOES given a mode; only
// integration/overlay/overlay_toolsurface_integration_test.go proves a guard is actually
// wired, and its map is written by hand. So the obligation is derived here:
// declaring a guard enrols its tool.
//
// The guard→tool link cannot be read off a type — a decorator is invisible from
// the tool spec — so it is read off the DECLARATION's doc comment. That makes the
// comment load-bearing, which is the point, but a comment is prose and prose can
// be wrong in two ways this gate has to close:
//
//   - a guard that is a TYPE rather than a func (nativeOnlyRetriever is one, and it
//     guards two of the eight pinned tools), which a FuncDecl-only walk never sees;
//   - a doc that names SOME pinned tool rather than its own, which a
//     does-it-mention-any check passes.
//
// Hence a bijection: every pinned tool is named by exactly one guard, and every
// guard names at least one pinned tool. That also forces a guard handed to two
// tools to name both — nativeOnlySlippingLister is one.
func TestEveryNativeOnlyGuardNamesAToolThePinCovers(t *testing.T) {
	const guardFile = "nativeonlytools.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, guardFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing %s: %v", guardFile, err)
	}
	pinned := pinnedNativeOnlyTools(t)

	// guard name → the doc comment that must name its tools. Both declaration
	// shapes, because a guard may be a decorator func OR a decorator type.
	docs := map[string]string{}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if strings.HasPrefix(d.Name.Name, nativeOnlyPrefix) {
				docs[d.Name.Name] = d.Doc.Text()
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !strings.HasPrefix(ts.Name.Name, nativeOnlyPrefix) {
					continue
				}
				// A single-spec GenDecl carries the doc; a grouped one leaves it
				// on the spec.
				doc := ts.Doc.Text()
				if doc == "" {
					doc = d.Doc.Text()
				}
				docs[ts.Name.Name] = doc
			}
		}
	}
	if len(docs) == 0 {
		t.Fatalf("no %s… declaration found in %s — the walk is reading the wrong file", nativeOnlyPrefix, guardFile)
	}

	namedBy := map[string][]string{}
	for guard, doc := range docs {
		named := 0
		for tool := range pinned {
			if strings.Contains(doc, tool) {
				namedBy[tool] = append(namedBy[tool], guard)
				named++
			}
		}
		if named == 0 {
			t.Errorf("%s names no tool the wiring pin covers. Its doc comment must name the tool(s) it "+
				"guards, and each must appear in nativeOnlyAgentTools "+
				"(compose/integration/overlay/overlay_toolsurface_integration_test.go) — a guard nothing drives "+
				"against a real overlay workspace is a guard nobody has tested.\ndoc: %q", guard, doc)
		}
	}

	// The other direction, which is what catches a doc naming the wrong tool: a
	// pinned tool no guard claims is either unguarded or guarded by a decorator
	// whose comment points somewhere else.
	for tool := range pinned {
		switch len(namedBy[tool]) {
		case 1:
		case 0:
			t.Errorf("the wiring pin covers %q and no nativeOnly… guard's doc names it — either the tool "+
				"has no mode guard at all, or the guard that decorates it describes a different tool", tool)
		default:
			t.Errorf("%q is named by %v — two guards claiming one tool means at least one comment is "+
				"describing something it does not decorate", tool, namedBy[tool])
		}
	}
}

// nativeOnlyPrefix is the naming convention the walk above depends on: a mode
// guard in this file is named for what it guards against, not for what it wraps.
const nativeOnlyPrefix = "nativeOnly"

// pinnedNativeOnlyTools reads the tool names out of the integration suite's pin.
// Parsed rather than duplicated: a copy here would be a third hand-kept list, and
// the whole point is that there be one.
func pinnedNativeOnlyTools(t *testing.T) map[string]bool {
	t.Helper()
	pinFile := overlayPin(t)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, pinFile, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", pinFile, err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "nativeOnlyAgentTools" {
			return true
		}
		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			kv, ok := inner.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if key, ok := kv.Key.(*ast.BasicLit); ok && key.Kind == token.STRING {
				name, err := strconv.Unquote(key.Value)
				if err != nil {
					t.Fatalf("unquoting a pinned tool name: %v", err)
				}
				out[name] = true
			}
			return true
		})
		return false
	})
	if len(out) == 0 {
		t.Fatalf("no tool names found in %s's nativeOnlyAgentTools — the pin moved or was renamed", pinFile)
	}
	return out
}
