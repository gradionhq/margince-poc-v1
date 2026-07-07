// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The store-entry-point admission rule as a fitness function: every
// exported method on a module's *Store or *Service — the seam both the
// HTTP handlers and the MCP tool surface call through — references the
// platform auth gate (object RBAC and/or the row-scope spellings),
// directly or through a same-package helper. A store method without one
// is an ungoverned door into tenant data: reachable by any transport
// wired to it, invisible to review. Row-scope composition itself stays
// a call-site obligation until it moves into the database (the ADR
// direction); this gate pins the half that is statically checkable.
//
// Gatedness is resolved transitively over same-package calls, matched
// by name: a name shared by several functions counts as gated when ANY
// of them references auth — optimistic on purpose, so the gate never
// cries wolf on dispatch it cannot resolve.
//
// Exceptions are explicit, keyed by "package-dir:FuncName", each with
// the rationale that ratified it; a reasonless or stale waiver fails.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// ungatedEntryPoints are the ratified auth-free store/service methods.
var ungatedEntryPoints = map[string]string{ // #nosec G101 -- waiver rationales for the fitness gate, not credentials
	// Authentication IS the gate these methods implement: they run
	// before a principal exists, or mint/retire the session itself.
	"internal/modules/identity:Login":                 "pre-principal: password verification is what admits the actor; there is no principal to gate yet",
	"internal/modules/identity:Logout":                "session retirement; the bearer's possession of the session IS the authority being revoked",
	"internal/modules/identity:Authenticate":          "pre-principal: this resolves the session cookie INTO the principal every other gate consumes",
	"internal/modules/identity:AuthenticateAgent":     "pre-principal: passport verification is what admits the agent actor (every call re-authenticates, ADR-0055)",
	"internal/modules/identity:AuthenticateAgentByID": "pre-principal: the by-id half of passport verification, same admission seam",
	"internal/modules/identity:ResolveWorkspace":      "tenancy resolution from the request host, before any principal exists",
	"internal/modules/identity:Bootstrap":             "first-run provisioning under the system principal; no human principal can exist before it",
	"internal/modules/identity:EffectiveRBAC":         "this LOADS the merged role policy the auth gate enforces — gating it on itself would recurse",
	"internal/modules/identity:SeatType":              "seat-tier lookup feeding the auth gate (scope ∧ tier); same layer as EffectiveRBAC, not above it",
	"internal/modules/identity:IssuePassport":         "gated by the explicit Identity parameter (the authenticated session): a passport is minted for that identity only, capped by validScopes",
	"internal/modules/identity:ListPassports":         "gated by the explicit Identity parameter: the query is pinned to on_behalf_of = the caller (admin sees the workspace's)",
	"internal/modules/identity:RevokePassport":        "gated by the explicit Identity parameter: owner-or-admin is checked against the passport's on_behalf_of before revoking",

	// Public-by-design token surfaces: possession of the single-use
	// token is the authority; there is no authenticated principal.
	"internal/modules/activities:ResolveBookingPage":  "public booking page (A16): resolved by slug for the anonymous visitor; writes nothing",
	"internal/modules/consent:ResolvePreferenceToken": "public preference-center resolve: the signed single-use token is the authority (no session exists)",
	"internal/modules/approvals:MintApprovalToken":    "signs the approval JWS for a decision already admitted by Decide; crypto, not admission",
	"internal/modules/approvals:VerifyApprovalToken":  "verifies the approval JWS presented back; the token is the authority being checked",
	"internal/modules/approvals:Redeem":               "redeems a verified approval token: the token (minted for an admitted decision) is the authority",

	// Engine/system seams that never carry a human principal: the
	// worker loop and cross-module effects run as the system actor, and
	// the admission happened at the surface that staged the work.
	"internal/modules/agents/runner:StartRun":                "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:SaveOutcome":             "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:MarkFailed":              "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:FindSuspendedByApproval": "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:EnqueueJob":              "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:ClaimDueJobs":            "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:FinishJob":               "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/approvals:WithEffect":                  "composition-root wiring (registers the confirm effect); no data access",
	"internal/modules/approvals:Stage":                       "staging is invoked BY an admitted mutation (the 🟡 path of a gated store call); the staging row records that actor",
	"internal/modules/approvals:HasPendingFor":               "existence probe consumed by gated sibling flows (the sweep's duplicate check); returns no record data",
	"internal/modules/approvals:HasPendingKind":              "existence probe consumed by gated sibling flows (the sweep's duplicate check); returns no record data",
	"internal/modules/deals:SeedDefaultsTx":                  "workspace-provisioning seed invoked by identity's Bootstrap under the system principal (the compose-injected edge)",
	"internal/modules/deals:StageSemantic":                   "vocabulary lookup (stage → open/won/lost) consumed by gated flows; reads config, not records",
	"internal/modules/search:UpsertEmbedding":                "written by the outbox consumer under the system principal; reads happen through the gated search paths",
}

// gateFnInfo is what the gate needs to know about one function name in a
// package: whether any body under that name references auth, and every
// name it mentions (the transitive-resolution edges).
type gateFnInfo struct {
	auth  bool
	calls map[string]bool
}

// gateEntry is one exported *Store/*Service method — a store entry point
// the gate must prove reaches auth.
type gateEntry struct{ dir, name string }

// collectStoreEntryPoints parses every non-test, non-integration module
// source file and returns, per package dir, the function index (a name
// shared across receivers merges optimistically — see the package
// comment) plus the list of exported *Store/*Service methods to check.
func collectStoreEntryPoints(t *testing.T) (map[string]map[string]*gateFnInfo, []gateEntry) {
	t.Helper()
	pkgs := map[string]map[string]*gateFnInfo{}
	var entries []gateEntry

	fset := token.NewFileSet()
	err := filepath.WalkDir("internal/modules", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			isIntegrationTagged(path) {
			return err
		}
		path = filepath.ToSlash(path)
		dir := filepath.ToSlash(filepath.Dir(path))
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		if pkgs[dir] == nil {
			pkgs[dir] = map[string]*gateFnInfo{}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			info := pkgs[dir][fn.Name.Name]
			if info == nil {
				info = &gateFnInfo{calls: map[string]bool{}}
				pkgs[dir][fn.Name.Name] = info
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "auth" {
						info.auth = true
					}
					info.calls[sel.Sel.Name] = true
				}
				if id, ok := n.(*ast.Ident); ok {
					info.calls[id.Name] = true
				}
				return true
			})
			if fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			if se, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := se.X.(*ast.Ident); ok && (id.Name == "Store" || id.Name == "Service") {
					entries = append(entries, gateEntry{dir, fn.Name.Name})
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkgs, entries
}

// reachesAuthGate resolves gatedness transitively over same-package
// calls, matched by name; seen breaks recursion cycles.
func reachesAuthGate(fns map[string]*gateFnInfo, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	info, ok := fns[name]
	if !ok {
		return false
	}
	if info.auth {
		return true
	}
	for c := range info.calls {
		if _, ok := fns[c]; ok && reachesAuthGate(fns, c, seen) {
			return true
		}
	}
	return false
}

func TestEveryStoreEntryPointIsAuthGated(t *testing.T) {
	for fn, rationale := range ungatedEntryPoints {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("ungatedEntryPoints[%s] has no rationale — a waiver must say why no gate is needed", fn)
		}
	}

	pkgs, entries := collectStoreEntryPoints(t)

	used := map[string]bool{}
	for _, e := range entries {
		if reachesAuthGate(pkgs[e.dir], e.name, map[string]bool{}) {
			continue
		}
		key := e.dir + ":" + e.name
		if _, ratified := ungatedEntryPoints[key]; ratified {
			used[key] = true
			continue
		}
		t.Errorf("%s: exported %s reaches no auth gate (directly or via same-package helpers) — every store entry point is RBAC-gated, or the exception is ratified in ungatedEntryPoints", e.dir, e.name)
	}
	for key := range ungatedEntryPoints {
		if !used[key] {
			t.Errorf("ungatedEntryPoints[%s] matches no ungated entry point — stale waiver, remove it", key)
		}
	}
}
