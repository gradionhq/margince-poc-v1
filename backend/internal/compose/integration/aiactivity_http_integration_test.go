// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The handler reaches the route only because an embedded handler set shadows
// the generated 501 stub, and that shadowing is asserted at COMPILE time
// alone: a handler set that stopped being embedded would still build if the
// stub answered in its place. The stub answers 501, so a 200 carrying the
// contract envelope is the proof that the real handler serves this route.
//
// The second half is the operation's `x-agent-access: human-only`
// declaration. A personal read of what the AI did on your behalf is
// exactly the surface an injected agent would use to learn what it is
// permitted to do unobserved, so the refusal is asserted through a real
// minted passport rather than trusted to the generated policy table.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

func TestMyAiActivityServesTheRealHandlerToAHuman(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	// Pointers, so an absent key and a null are both distinguishable from a
	// present empty array — the contract requires all three fields, and the
	// panel renders "at rest" from an empty `running`, not from a missing one.
	// Decoding into slices also fails the call outright if either field
	// arrives as anything but a JSON array.
	var body struct {
		AsOf    *string            `json:"as_of"`
		Running *[]json.RawMessage `json:"running"`
		Recent  *[]json.RawMessage `json:"recent"`
	}
	status := e.Call(t, "GET", "/v1/me/ai-activity", nil, nil, &body)
	if status != http.StatusOK {
		t.Fatalf("human GET /v1/me/ai-activity → %d, want 200 "+
			"(501 means the generated stub answered and the handler set is no longer embedded)", status)
	}
	if body.AsOf == nil || *body.AsOf == "" {
		t.Errorf("as_of is absent or empty: the reader cannot tell how fresh this answer is")
	}
	if body.Running == nil {
		t.Errorf("running is absent: the contract requires it, and an empty array is how the rail says the AI is at rest")
	}
	if body.Recent == nil {
		t.Errorf("recent is absent: the contract requires it even on a day with no settled occurrence")
	}
}

func TestMyAiActivityRefusesAnAgentBearer(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": "agent activity read probe", "scopes": []string{"read"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}

	// 403 exactly, and the sentinel code with it: a 404 would be a pass for
	// the wrong reason, since the point is that the refusal lands before the
	// handler ever looks for a run.
	var problem struct {
		Code string `json:"code"`
	}
	status := e.Call(t, "GET", "/v1/me/ai-activity", nil,
		map[string]string{"Authorization": "Bearer " + minted.Token}, &problem)
	if status != http.StatusForbidden {
		t.Errorf("agent GET /v1/me/ai-activity → %d, want 403 (the contract declares it human-only)", status)
	}
	if problem.Code != "permission_denied" {
		t.Errorf("agent GET /v1/me/ai-activity → code %q, want permission_denied", problem.Code)
	}
}
