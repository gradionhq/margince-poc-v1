// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"net/http"
	"testing"
)

type agentToolWire struct {
	Name          string `json:"name"`
	RequiredScope string `json:"required_scope"`
	Tier          string `json:"tier"`
	Egress        bool   `json:"egress"`
}
type agentToolListWire struct {
	Data []agentToolWire `json:"data"`
}

func TestListAgentToolsMirrorsTheGovernedSurface(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var page agentToolListWire
	status := e.call(t, "GET", "/v1/agent-tools", nil, nil, &page)
	if status != http.StatusOK {
		t.Fatalf("GET /agent-tools status = %d, want 200", status)
	}
	if len(page.Data) == 0 {
		t.Fatal("expected a non-empty governed tool surface")
	}
	// search_records is a 🟢 read tool that must be present and non-egress.
	var found bool
	for _, tool := range page.Data {
		if tool.Name == "search_records" {
			found = true
			if tool.Tier != "green" || tool.Egress {
				t.Fatalf("search_records = %+v, want green/non-egress", tool)
			}
		}
		if tool.Tier != "green" && tool.Tier != "yellow" && tool.Tier != "dynamic" {
			t.Fatalf("tool %q has unmapped tier %q", tool.Name, tool.Tier)
		}
	}
	if !found {
		t.Fatal("search_records missing from the surface")
	}
}
