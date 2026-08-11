// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Route ownership: which unit's handler a mounted extension route may dispatch.
//
// Its own file rather than another entry in extparity_test.go, because the
// parity sweeps answer "is every declaration mounted, and every mount
// declared" — a question about COMPLETENESS. This one answers "whose behavior
// does a mounted route run", which is a question about ATTRIBUTION, and the two
// were conflated in the defect it pins.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// TestOneUnitCannotInheritAnothersHandlerByNamingItsVerb is the route-ownership
// regression. The served set used to be keyed on the bare tool verb, and an
// `x-mcp-tool` value is just a string a unit writes into its own contract
// fragment — so unit beta could declare a CONTRACT-ONLY operation naming
// alpha's served verb, be marked implemented on the strength of alpha's
// handler, and have its own published route dispatch that handler. What
// executed then was alpha's registry spec — alpha's tier, scope, RBAC object
// and schemas — under beta's published operation.
//
// The two routes are distinct paths under distinct unit namespaces, so nothing
// else in the mounting objects: the duplicate-pattern refusal never fires, and
// the parity sweeps see two legitimately declared operations.
func TestOneUnitCannotInheritAnothersHandlerByNamingItsVerb(t *testing.T) {
	// One verb name, two units. Alpha ships the behavior; beta ships none.
	const shared = "sync_contacts"
	alpha := unitVerb("alpha", shared, extension.TierAutoExecute, extension.ScopeRead)
	beta := unitVerb("beta", shared, extension.TierAutoExecute, extension.ScopeRead)
	// unitVerb derives the route from the unit, so these are already distinct
	// paths; assert it rather than trust it, because a shared pattern would make
	// this test pass for the wrong reason (the duplicate-pattern refusal).
	if alpha.ServedPath() == beta.ServedPath() {
		t.Fatalf("both units declare %s — this test needs two distinct routes", alpha.ServedPath())
	}

	// The real served set, through the real global: only alpha registered a
	// handler for the verb.
	withComposedVerbs(t, []extension.Verb{alpha, beta}, alpha)

	invoked := ""
	mux := http.NewServeMux()
	routes, err := MountExtensionRoutes(mux, []extension.Verb{alpha, beta}, composedServedVerbs(),
		func(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
			invoked = name
			return json.RawMessage(sealedEmptyResult), nil
		})
	if err != nil {
		t.Fatalf("mounting two units sharing a verb: %v", err)
	}

	implemented := map[string]bool{}
	for _, route := range routes {
		implemented[string(route.Verb.Unit)] = route.Implemented
	}
	if !implemented["alpha"] {
		t.Error("alpha shipped the handler but its own route is not implemented")
	}
	if implemented["beta"] {
		t.Error("beta shipped no handler, yet its route reports as implemented — it is claiming alpha's")
	}

	// The behaviour, not just the flag: beta's published route must refuse
	// rather than dispatch. A 501 here and a silent 200 there is the whole
	// difference between a documented gap and a cross-unit execution.
	invoked = ""
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(beta.Method, beta.ServedPath(), strings.NewReader(`{}`)))
	if invoked != "" {
		t.Fatalf("beta's route invoked %q — it dispatched a handler belonging to another unit", invoked)
	}
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("beta's contract-only route answered %d, want 501 (body %s)", rec.Code, rec.Body)
	}

	// And alpha's own route still works, so the fix is a narrowing rather than
	// a blanket refusal of any verb two units mention.
	invoked = ""
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(alpha.Method, alpha.ServedPath(), strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("alpha's own route answered %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if invoked != shared {
		t.Fatalf("alpha's route invoked %q, want %q", invoked, shared)
	}
}
