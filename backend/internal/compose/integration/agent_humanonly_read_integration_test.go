// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// `x-agent-access: human-only` on a `get:` used to be documentation. The
// admission gate returned early on every non-mutating method, and the
// generated policy table carried no GET keys at all, so the refusal was
// structurally unreachable for the ~37 human-only reads the contract
// declares — attachment BYTES among them, which is where an injected agent
// turns a read grant into bulk exfiltration.
//
// This drives the real HTTP stack with a real passport: the refusal is the
// contract's, and the reads that were never human-only keep working.

import (
	"net/http"
	"testing"
)

func TestAgentBearerIsRefusedOnHumanOnlyReads(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var minted struct {
		PassportID string `json:"passport_id"`
		Token      string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "human-only read probe", "scopes": []string{"read"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// One route per class the finding named: the attachment surface (the
	// listing that supplies the ids, the bytes, and their extracted text),
	// the AI call log, the audit log, the voice profiles, and the webhook
	// subscriptions. A 404 would be a pass for the wrong reason — the point
	// is that the refusal happens before the handler ever looks for the row
	// — so each asserts 403 exactly.
	//
	// The listing carries its required query parameters: the generated
	// wrapper binds those before it runs the middleware chain, so a request
	// missing them answers 422 without ever reaching the gate. That ordering
	// discloses nothing (it is the same answer for any principal), but a
	// probe that stopped there would be asserting the wrong refusal.
	for _, route := range []string{
		"/v1/attachments?entity_type=deal&entity_id=00000000-0000-7000-8000-0000000000bb",
		"/v1/attachments/00000000-0000-7000-8000-0000000000aa",
		"/v1/attachments/00000000-0000-7000-8000-0000000000aa/extraction",
		"/v1/ai/calls",
		"/v1/audit-log",
		"/v1/voice-profiles",
		"/v1/webhook-subscriptions",
		"/v1/passports",
		// The consent screen's read model lists the granting human's
		// lendable passports — ids, labels, scopes, expiries — so it is
		// the same credential inventory as /v1/passports above. Its
		// required query parameters are present but need name no real
		// client: the gate refuses before the handler resolves one.
		"/v1/oauth/consent-request?client_id=night-agent&scope=read",
		// The pre-flip export bundle: a full-estate read, audit log
		// included, in a single GET.
		"/v1/overlay/export",
	} {
		t.Run(route, func(t *testing.T) {
			var problem struct {
				Code string `json:"code"`
			}
			status := e.call(t, "GET", route, nil, bearer, &problem)
			if status != http.StatusForbidden {
				t.Errorf("agent GET %s → %d, want 403 (the contract declares it human-only)", route, status)
			}
			if problem.Code != "permission_denied" {
				t.Errorf("agent GET %s → code %q, want permission_denied", route, problem.Code)
			}
		})
	}

	// The gate narrows the annotated exceptions, it does not close the read
	// surface: an ordinary agent-readable route still answers.
	if status := e.call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusOK {
		t.Errorf("agent GET /v1/people → %d, want 200 — an unannotated read stays agent-readable", status)
	}
}
