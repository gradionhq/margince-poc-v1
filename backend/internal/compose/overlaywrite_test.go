// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// fakeMode is an overlayModeChecker stub returning a fixed answer. It counts
// reads so a test can assert the workspace row was NOT consulted, which is the
// only way to state that claim rather than infer it from an outcome.
type fakeMode struct {
	overlay bool
	err     error
	reads   int
}

func (f *fakeMode) isOverlayUncached(context.Context) (bool, error) {
	f.reads++
	return f.overlay, f.err
}

// guardRequest builds a request carrying the chi route pattern the guard
// keys on — the same shape the contract router populates before running
// the middleware chain.
func guardRequest(method, pattern string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{pattern}
	r := httptest.NewRequest(method, "http://example.test"+pattern, nil)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestOverlayWriteGuard(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		pattern    string
		overlay    bool
		wantNext   bool
		wantStatus int
	}{
		{"SoR write refused in overlay", "POST", "/v1/people", true, false, http.StatusUnprocessableEntity},
		{"SoR write allowed off overlay", "POST", "/v1/people", false, true, http.StatusOK},
		{"deal advance refused in overlay", "POST", "/v1/deals/{id}/advance", true, false, http.StatusUnprocessableEntity},
		{"lead promote refused in overlay", "POST", "/v1/leads/{id}/promote", true, false, http.StatusUnprocessableEntity},
		// DELETE /v1/leads/{id} is disqualify_lead (agentpolicy_gen.go), a
		// DIFFERENT route and tool from lead promote above — it has no entry
		// in overlayWriteVerbs, so it is refused on that basis alone, not on
		// SupportsWrite. Pinned here so a future overlayWriteVerbs entry (or a
		// policy regen reclassifying the route) that started letting it
		// through would fail a test, not just fall silently to
		// DisqualifyLead's native handler.
		{"lead disqualify refused in overlay", "DELETE", "/v1/leads/{id}", true, false, http.StatusUnprocessableEntity},
		// Archive of a mirrored type the provider DOES support
		// (overlay.SupportsWrite(WriteArchive, person) is true — archivableTypes)
		// is let through rather than refused: it is destined for the write
		// shadow, never the native handler.
		{"supported archive allowed in overlay", "DELETE", "/v1/people/{id}", true, true, http.StatusOK},
		// Archive of a mirrored type the provider does NOT support (activity
		// is not in archivableTypes) is still refused.
		{"unsupported archive refused in overlay", "DELETE", "/v1/activities/{id}", true, false, http.StatusUnprocessableEntity},
		// Native governance write (human-only, e.g. an approval decision) is
		// NOT a SoR record write — it stays available in overlay.
		{"governance write allowed in overlay", "POST", "/v1/approvals/{id}/approve", true, true, http.StatusOK},
		// A read is never guarded.
		{"read passes through in overlay", "GET", "/v1/people", true, true, http.StatusOK},
		// An unknown route is not a SoR write — pass through.
		{"unknown route passes through", "POST", "/v1/not-a-route", true, true, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})
			h := overlayWriteGuard(&fakeMode{overlay: tc.overlay})(next)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, guardRequest(tc.method, tc.pattern))

			if nextCalled != tc.wantNext {
				t.Errorf("next called = %v, want %v", nextCalled, tc.wantNext)
			}
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

// runOverlayWriteGuard drives the guard for one request in overlay mode and
// reports whether it reached next and with what status — the shared shape
// TestOverlayWriteGuardAllowsNativeOnlyEntities and
// TestGuardRefusalMatchesProviderCapability both drive.
func runOverlayWriteGuard(method, pattern string) (nextCalled bool, status int) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	h := overlayWriteGuard(&fakeMode{overlay: true})(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, guardRequest(method, pattern))
	return nextCalled, rec.Code
}

// A native-only entity is not mirrored, so its native table is the live one
// even in overlay mode: the guard must let its writes through rather than
// refusing on the tool verb alone.
func TestOverlayWriteGuardAllowsNativeOnlyEntities(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		pattern string
	}{
		{"offer line-item create", "POST", "/v1/offers/{id}/line-items"},
		{"product update", "PATCH", "/v1/products/{id}"},
		{"tag create", "POST", "/v1/tags"},
		{"saved view archive", "DELETE", "/v1/views/{id}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled, status := runOverlayWriteGuard(tc.method, tc.pattern)
			if !nextCalled {
				t.Errorf("next called = false, want true (native-only entity must not be refused)")
			}
			if status != http.StatusOK {
				t.Errorf("status = %d, want %d", status, http.StatusOK)
			}
		})
	}
}

// The guard refuses exactly what the provider cannot serve — no more, no
// less. A verb/type the provider supports must reach its shadow (i.e. pass
// the guard); one it refuses must never reach a native handler.
func TestGuardRefusalMatchesProviderCapability(t *testing.T) {
	tests := []struct {
		entityType datasource.EntityType
		verb       overlay.WriteVerb
		method     string
		pattern    string
	}{
		{datasource.EntityPerson, overlay.WriteCreate, "POST", "/v1/people"},
		{datasource.EntityPerson, overlay.WriteUpdate, "PATCH", "/v1/people/{id}"},
		{datasource.EntityPerson, overlay.WriteArchive, "DELETE", "/v1/people/{id}"},
		{datasource.EntityOrganization, overlay.WriteCreate, "POST", "/v1/organizations"},
		{datasource.EntityOrganization, overlay.WriteUpdate, "PATCH", "/v1/organizations/{id}"},
		{datasource.EntityOrganization, overlay.WriteArchive, "DELETE", "/v1/organizations/{id}"},
		{datasource.EntityDeal, overlay.WriteCreate, "POST", "/v1/deals"},
		{datasource.EntityDeal, overlay.WriteUpdate, "PATCH", "/v1/deals/{id}"},
		{datasource.EntityDeal, overlay.WriteArchive, "DELETE", "/v1/deals/{id}"},
		{datasource.EntityLead, overlay.WriteCreate, "POST", "/v1/leads"},
		{datasource.EntityLead, overlay.WriteUpdate, "PATCH", "/v1/leads/{id}"},
		// Lead has no archive_record route — DELETE /v1/leads/{id} is
		// disqualify_lead, a lifecycle verb the seam mapping never carries
		// (overlayWriteVerbs), so overlay.SupportsWrite has no WriteArchive
		// opinion about it at all; it is covered on its own terms by
		// TestOverlayWriteGuard's "lead disqualify refused in overlay" case,
		// not by this SupportsWrite-driven table.
		{datasource.EntityActivity, overlay.WriteCreate, "POST", "/v1/activities"}, // log_activity
		{datasource.EntityActivity, overlay.WriteUpdate, "PATCH", "/v1/activities/{id}"},
		{datasource.EntityActivity, overlay.WriteArchive, "DELETE", "/v1/activities/{id}"},
	}
	for _, tc := range tests {
		t.Run(string(tc.entityType)+"/"+string(tc.verb), func(t *testing.T) {
			nextCalled, status := runOverlayWriteGuard(tc.method, tc.pattern)
			wantNext := overlay.SupportsWrite(tc.verb, tc.entityType)
			if nextCalled != wantNext {
				t.Errorf("next called = %v, want %v (SupportsWrite(%s, %s) = %v)",
					nextCalled, wantNext, tc.verb, tc.entityType, wantNext)
			}
			wantStatus := http.StatusOK
			if !wantNext {
				wantStatus = http.StatusUnprocessableEntity
			}
			if status != wantStatus {
				t.Errorf("status = %d, want %d", status, wantStatus)
			}
		})
	}
}

// serverDeclaredMethods parses this package's own hand-written source (test
// files excluded) and returns the set of method names declared with a
// receiver of type Server — a static-source check, not reflection: every
// contract op Server does not shadow directly is instead SATISFIED BY
// PROMOTION from an embedded module handler (e.g. people.Handlers), which
// carries the identical method name. Reflection over Server{}'s resolved
// method set cannot tell a hand-written shadow apart from a promoted
// fallback (both simply appear as "Server has a method named X"), so the
// fitness test below reads the source directly, the same static-analysis
// idiom backend/arch_test.go already uses for its own package-boundary
// fitness functions.
func serverDeclaredMethods(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package compose's directory: %v", err)
	}
	fset := token.NewFileSet()
	declared := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
				continue
			}
			recv := fd.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if ident, ok := recv.(*ast.Ident); ok && ident.Name == "Server" {
				declared[fd.Name.Name] = true
			}
		}
	}
	return declared
}

// overlayEntityTitles is the Go-identifier title-case for each mirrored
// entity type, spelling the Update<Title>/Archive<Title> shadow method
// names the same way the contract's own generated operation names do.
// gatekit:fixture the expected spelling each shadow method name is built from —
// naming-convention data, not a cost.
var overlayEntityTitles = map[string]string{
	string(datasource.EntityPerson):       "Person",
	string(datasource.EntityOrganization): "Organization",
	string(datasource.EntityDeal):         "Deal",
	string(datasource.EntityLead):         "Lead",
	string(datasource.EntityActivity):     "Activity",
}

// TestOverlayWriteShadowsCoverEverySupportedWrite keeps the guard and the
// shadows honest with each other: overlaywrite.go's guard admits a
// mirrored-type write the whole way to a handler once overlay.SupportsWrite
// says the provider can serve it — on the promise that a shadow is there to
// serve it. For every mirrored type × verb the provider actually supports,
// Server must declare its own Update<Type>/Archive<Type> shadow; a
// supported write with no shadow falls through to the native handler's
// promoted method instead and commits to the empty overlay-mode table.
func TestOverlayWriteShadowsCoverEverySupportedWrite(t *testing.T) {
	declared := serverDeclaredMethods(t)
	verbPrefixes := map[overlay.WriteVerb]string{
		overlay.WriteCreate:  "Create",
		overlay.WriteUpdate:  "Update",
		overlay.WriteArchive: "Archive",
	}
	for et := range overlayMirroredTypes {
		title, ok := overlayEntityTitles[et]
		if !ok {
			t.Fatalf("overlayMirroredTypes has %q with no entry in overlayEntityTitles — add one so this fitness function can name its shadow", et)
		}
		for verb, prefix := range verbPrefixes {
			if !overlay.SupportsWrite(verb, datasource.EntityType(et)) {
				continue
			}
			method := prefix + title
			if !declared[method] {
				t.Errorf("overlay.SupportsWrite(%s, %s) is true but Server declares no %s shadow — "+
					"this write would fall through to the native handler's promoted method and commit to the empty overlay-mode table",
					verb, et, method)
			}
		}
	}
}

// guardRequestAs is guardRequest with a principal bound — the state the
// identity middleware leaves before the router runs. It is needed because
// isAgentPrincipal is the ONLY discriminator for a verb the seam can serve: a
// principal-less request takes the human path, so the agent branch has to be
// driven explicitly or it is untested. What that branch closes is a live
// chain — staged from a nil version pin, redeemed with nothing re-checked,
// archived in the customer's CRM.
func guardRequestAs(method, pattern string, p principal.Principal) *http.Request {
	r := guardRequest(method, pattern)
	return r.WithContext(principal.WithActor(r.Context(), p))
}

func guardAgent() principal.Principal {
	return principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(principal.ScopeWrite),
	}
}

func guardHuman() principal.Principal {
	return principal.Principal{Type: principal.PrincipalHuman, ID: "human:t", UserID: ids.NewV7()}
}

func TestOverlayWriteGuardRefusesAnAgentEvenForASeamServedVerb(t *testing.T) {
	// Both are verbs the seam DOES serve, so only the principal decides.
	for _, tc := range []struct{ method, pattern string }{
		{"PATCH", "/v1/people/{id}"},
		{"DELETE", "/v1/people/{id}"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			nexted := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nexted = true })
			rec := httptest.NewRecorder()

			overlayWriteGuard(&fakeMode{overlay: true})(next).
				ServeHTTP(rec, guardRequestAs(tc.method, tc.pattern, guardAgent()))

			if nexted {
				t.Error("an agent write reached the handler — it would be staged, then released past the seam backstop")
			}
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", rec.Code)
			}
		})
	}
}

// The other direction of the same clause: a human keeps the write it always
// had, and takes the fast path without a mode read.
func TestOverlayWriteGuardPassesAHumanSeamServedWrite(t *testing.T) {
	for _, tc := range []struct{ method, pattern string }{
		{"PATCH", "/v1/people/{id}"},
		{"DELETE", "/v1/people/{id}"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			nexted := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nexted = true })
			rec := httptest.NewRecorder()

			mode := &fakeMode{overlay: true}
			overlayWriteGuard(mode)(next).
				ServeHTTP(rec, guardRequestAs(tc.method, tc.pattern, guardHuman()))

			if !nexted {
				t.Errorf("a human seam-served write was blocked (status %d)", rec.Code)
			}
			// The fast path exists to spare a second workspace-row read per
			// mutation; the dispatch resolves the mode fresh either way.
			if mode.reads != 0 {
				t.Errorf("the guard read the mode %d time(s) on a human seam-served write, want 0", mode.reads)
			}
		})
	}
}

// The guard's fail-closed branch: it cannot tell whether this workspace reads
// from the incumbent, so it must refuse rather than let a write through on a
// guess. Nothing else asserts this, and it is the one path where guessing
// wrong sends a mutation to the wrong system of record.
func TestOverlayWriteGuardRefusesWhenTheModeCannotBeResolved(t *testing.T) {
	nexted := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nexted = true })
	rec := httptest.NewRecorder()

	// A verb the seam does NOT serve, so the guard reaches the mode read.
	mode := &fakeMode{err: errModeUnresolvable}
	overlayWriteGuard(mode)(next).ServeHTTP(rec, guardRequest("POST", "/v1/people"))

	if nexted {
		t.Error("the guard admitted a write without resolving the system-of-record mode")
	}
	if rec.Code == http.StatusOK {
		t.Errorf("status = %d, want a refusal", rec.Code)
	}
	if mode.reads != 1 {
		t.Errorf("mode reads = %d, want 1", mode.reads)
	}
}

var errModeUnresolvable = errors.New("the workspace row could not be read")
