// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// What the consent read model may disclose, and to whom, is decided BEFORE it
// resolves a client_id: an unknown, disabled or soft-deleted client answers 404
// while a live one goes on to a real screen, so anyone who reaches that lookup
// can tell the two apart. These two tests pin the reason nobody but the
// signed-in human ever reaches it.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The handler's first act is to demand the human whose authority would be lent.
// A caller without one is refused there — above the client lookup, and without
// a database round trip — which is why the Service below carries no pool: a
// lookup that ran anyway could not silently pass.
func TestConsentRequestRefusesACallerWithNoSignedInHumanBeforeResolvingTheClient(t *testing.T) {
	h := Handlers{svc: &Service{}}
	params := crmcontracts.GetConsentRequestParams{ClientId: "some-client", Scope: "read"}

	for name, ctx := range map[string]context.Context{
		// No principal at all: the shape a mount without the session
		// middleware would produce.
		"no principal": context.Background(),
		// An agent principal, which is what a passport bearer becomes. It
		// arrives WITHOUT an identity (serveAsAgent binds an actor and
		// nothing else), so this refusal is the one it meets.
		"an agent principal": principal.WithActor(context.Background(), principal.Principal{
			Type:   principal.PrincipalAgent,
			ID:     "agent:passport",
			Scopes: principal.NewScopeSet(principal.ScopeRead),
		}),
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet,
				"/v1/oauth/consent-request?client_id=some-client&scope=read", nil).WithContext(ctx)

			h.GetConsentRequest(rec, req, params)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("GetConsentRequest → %d, want %d: the consent screen belongs to a signed-in human, and nothing about a client_id may be disclosed before one is present",
					rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// The refusal above is only sufficient because an identity in the context means
// a HUMAN principal: withIdentity is called once in this package, from
// withHumanPrincipal, which binds principal.PrincipalHuman alongside it. A
// second caller — an agent path that bound an identity of its own for
// convenience — would carry a non-human principal past that refusal and into
// the client lookup, so the property is asserted over the package's source
// rather than described in a comment.
func TestOnlyAHumanPrincipalIsGivenAnIdentity(t *testing.T) {
	const binder = "withHumanPrincipal"
	callers := identityBinders(t)
	if len(callers) != 1 || callers[0] != binder {
		t.Fatalf("withIdentity is called from %v, want exactly [%s]: an identity bound anywhere else can reach the consent read as a non-human principal", callers, binder)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "handlers.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing handlers.go: %v", err)
	}
	body := funcBody(t, file, binder)
	if body == nil {
		t.Fatalf("handlers.go has no %s — the one identity binder moved", binder)
	}
	if !bindsHumanPrincipalType(body) {
		t.Errorf("%s binds an identity without principal.PrincipalHuman", binder)
	}
}

// identityBinders names every function in this package's non-test sources that
// calls withIdentity.
func identityBinders(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var callers []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			// withIdentity's own declaration is not a caller of it.
			if !ok || fn.Body == nil || fn.Name.Name == "withIdentity" {
				continue
			}
			if callsFunc(fn.Body, "withIdentity") {
				callers = append(callers, fn.Name.Name)
			}
		}
	}
	return callers
}

// bindsHumanPrincipalType reports whether body names the human principal type,
// which is what makes "has an identity" and "is a human" the same statement.
func bindsHumanPrincipalType(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "PrincipalHuman" {
			found = true
		}
		return !found
	})
	return found
}
