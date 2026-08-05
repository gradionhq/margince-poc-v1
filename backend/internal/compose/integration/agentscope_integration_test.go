// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The passport cap a verb spends is the CONTRACT's, over real HTTP.
//
// The REST agent gate used to admit any verb with no registered MCP tool
// under `write`, so a passport holding read+write could enrich from a
// third-party website or ship an offer to a counterparty — two egressing
// acts — on a cap that says nothing about leaving the workspace. The verbs
// now carry `x-mcp-tool.scope`, and `principal.ScopeSet.Has` is exact
// membership: `write` does not imply `enrich` or `send`.
//
// The unit suites prove contract↔registry parity. What only this lane can
// prove is the gate: that the missing cap is REFUSED at the wire, with the
// scope sentinel's own status and code, and — the part a "not 200" check
// would have passed on the old code too — that nothing was staged.

import (
	"net/http"
	"strings"
	"testing"
)

// scopeRefusalCode is what apperrors.ErrScopeExceeded maps to in
// httperr's sentinel registry, and problemType is the RFC 7807 type URI
// writeProblem derives from it.
const (
	scopeRefusalCode = "scope_exceeds_grantor"
	scopeRefusalType = "https://errors.gradion.com/scope_exceeds_grantor"
)

// capRefusal is the slice of the RFC 7807 problem the scope refusals are
// asserted on: the machine code and type a client branches on, plus the
// detail a human reads.
type capRefusal struct {
	Type   string `json:"type"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func TestEnrichRefusesAPassportWithoutTheEnrichCap(t *testing.T) {
	e := setup(t)
	e.slug = "enrich-scope"
	bootstrapWorkspaceSession(t, e, "Enrich Scope", "enrich@fable.test", "Admin")

	var org struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/organizations", anyMap{
		"display_name": "Acme GmbH", "source": "ui",
		"domains": []anyMap{{"domain": "acme.example", "is_primary": true}},
	}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create org → %d", status)
	}

	// The old default: everything a mutating agent did cost `write`.
	writeOnly := passportBearer(t, e, "write-only agent", "read", "write")

	var refusal capRefusal
	status := e.call(t, "POST", "/v1/organizations/"+org.ID+"/enrich", nil, writeOnly, &refusal)
	if status != http.StatusForbidden || refusal.Code != scopeRefusalCode || refusal.Type != scopeRefusalType {
		t.Fatalf("enrich on read+write → %d %q %q, want 403 %s / %s",
			status, refusal.Code, refusal.Type, scopeRefusalCode, scopeRefusalType)
	}
	// The distinguishing assertion. Before the scope came from the contract
	// this same call was ADMITTED and staged a 🟡 approval — a 403 either
	// way, so only the code and the absence of a staged row separate the
	// refusal from the admission.
	if refusal.Code == "approval_required" || strings.Contains(refusal.Detail, "staged as approval") {
		t.Fatalf("a passport without the enrich cap reached the 🟡 gate: %q", refusal.Detail)
	}
	assertNothingStaged(t, e, "refused enrich")
	assertRefusalLeaksNothing(t, refusal.Detail, "enrich")

	// The same principal shape with the cap the contract declares. There is
	// no route that widens a minted passport's scopes (mint and revoke only),
	// so the grant is a fresh passport differing in exactly that one cap.
	withEnrich := passportBearer(t, e, "enriching agent", "read", "write", "enrich")

	// What this proves: auth.Admit checks scope BEFORE tier, so
	// approval_required is unreachable unless the enrich cap was spent — the
	// outcome is the verb's real 🟡 behaviour, not a different refusal. And
	// it is deterministic offline: the gate stages the whole unapplied
	// request and answers before the handler runs, so no website is fetched
	// on this path at all.
	var staged capRefusal
	status = e.call(t, "POST", "/v1/organizations/"+org.ID+"/enrich", nil, withEnrich, &staged)
	if status != http.StatusForbidden || staged.Code != "approval_required" {
		t.Fatalf("enrich with the enrich cap → %d %q, want 403 approval_required (the 🟡 gate)", status, staged.Code)
	}
	approvalID := extractStagedApprovalID(t, staged.Detail)
	var kind string
	if err := e.owner.QueryRow(t.Context(),
		`SELECT kind FROM approval WHERE id = $1`, approvalID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "enrich" {
		t.Fatalf("staged approval kind = %q, want enrich", kind)
	}
}

// TestOfferSendRefusesAnyAgentAsHumanOnly proves that sendOffer carries no
// registered agent tool: the contract declares it `x-agent-access:
// human-only`, so ANY agent principal is refused outright at the gate,
// whatever caps its passport holds — no cap, however broad, reaches the
// send verb. The refusal is distinguished from a cap refusal
// (scope_exceeds_grantor) by its own code, permission_denied, so a caller
// can tell "you may never do this" from "mint a passport with the right
// cap and retry."
func TestOfferSendRefusesAnyAgentAsHumanOnly(t *testing.T) {
	e := setup(t)
	e.slug = "send-scope"
	bootstrapWorkspaceSession(t, e, "Send Scope", "send@fable.test", "Admin")
	dealID := offerFixture(t, e)

	// The broadest passport short of a human session: even every cap the
	// contract knows does not reach a human-only verb.
	bearer := passportBearer(t, e, "drafting agent", "read", "write", "send", "enrich")

	// Drafting is 🟢 under `write` and still is: send being human-only is
	// not a blanket tightening of the offer surface.
	var offer offerBody
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "mcp",
		"line_items": []anyMap{{"description": "Pilot", "quantity": 1, "unit_price_minor": 250000, "tax_rate": 19.0}},
	}, bearer, &offer); status != http.StatusCreated {
		t.Fatalf("agent 🟢 offer draft → %d", status)
	}

	var refusal capRefusal
	status := e.call(t, "POST", "/v1/offers/"+offer.ID+"/send", nil, bearer, &refusal)
	if status != http.StatusForbidden || refusal.Code != "permission_denied" {
		t.Fatalf("agent send → %d %q, want 403 permission_denied (human-only)", status, refusal.Code)
	}
	// The distinguishing assertion: this is the human-only refusal, not the
	// cap refusal or the 🟡 admission wearing the same status.
	if refusal.Code == scopeRefusalCode {
		t.Fatalf("send refused as a cap miss (%s) rather than human-only", scopeRefusalCode)
	}
	if refusal.Code == "approval_required" || strings.Contains(refusal.Detail, "staged as approval") {
		t.Fatalf("an agent reached the 🟡 gate on a human-only verb: %q", refusal.Detail)
	}
	if !strings.Contains(refusal.Detail, "human-only") {
		t.Fatalf("refusal %q does not say the verb is human-only", refusal.Detail)
	}
	assertNothingStaged(t, e, "refused send")
	assertRefusalLeaksNothing(t, refusal.Detail, "human-only")

	// Nothing left the workspace, and nothing is waiting in an inbox to say
	// it might.
	var still offerBody
	if status := e.call(t, "GET", "/v1/offers/"+offer.ID, nil, bearer, &still); status != http.StatusOK || still.Status != "draft" {
		t.Fatalf("offer after the refused send = %q, want draft", still.Status)
	}
}

// passportBearer mints a passport with exactly scopes and returns the
// Authorization header an agent principal presents.
func passportBearer(t *testing.T, e *env, label string, scopes ...string) map[string]string {
	t.Helper()
	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": label, "scopes": scopes,
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport %q → %d", label, status)
	}
	if minted.Token == "" {
		t.Fatalf("passport %q minted without a token", label)
	}
	return map[string]string{"Authorization": "Bearer " + minted.Token}
}

// assertNothingStaged proves the refusal was a cap refusal and not the 🟡
// admission wearing the same status: a staged approval is a durable
// authority object, so its absence is the observable difference.
func assertNothingStaged(t *testing.T, e *env, what string) {
	t.Helper()
	var staged int
	if err := e.owner.QueryRow(t.Context(), `SELECT count(*) FROM approval`).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if staged != 0 {
		t.Fatalf("%s staged %d approval(s) — a missing cap must refuse, never mint authority to spend it", what, staged)
	}
}

// assertRefusalLeaksNothing holds the refusal body to both halves of the
// error bar: it names the cap the caller must be granted, and it carries no
// operator-only detail.
func assertRefusalLeaksNothing(t *testing.T, detail, missingCap string) {
	t.Helper()
	if !strings.Contains(detail, missingCap) {
		t.Fatalf("refusal %q does not name the missing cap %q — the caller cannot act on it", detail, missingCap)
	}
	lower := strings.ToLower(detail)
	for _, leak := range []string{
		"select ", "insert ", "update ", "app_user", "workspace_id", "approval_effect",
		"pgx", "errscopeexceeded", "apperrors", ".go:", "goroutine", "/users/", "internal/platform",
	} {
		if strings.Contains(lower, leak) {
			t.Fatalf("refusal %q leaks internals (%q)", detail, leak)
		}
	}
}
