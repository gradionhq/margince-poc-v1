// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The blocked-domain surface over the real wire: what an admin can see about a
// refusal, what they can change, and that unblocking normalizes and records the
// decision as theirs.
//
// The list is the only way an operator can tell "we refused this domain" from
// "nothing ever arrived", so its contents matter as much as its writes.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

type blockedDomainDTO struct {
	Domain    string `json:"domain"`
	Admission string `json:"admission"`
	Reason    string `json:"reason"`
	Source    string `json:"source"`
	DecidedAt string `json:"decided_at"`
}

type blockedDomainListDTO struct {
	Data []blockedDomainDTO `json:"data"`
}

func TestBlockedDomainsOverHTTP(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	// A workspace that has refused nothing answers with an empty list, never a
	// null: the contract promises an array.
	var list blockedDomainListDTO
	if status := e.Call(t, "GET", "/v1/capture/blocked-domains", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("GET → %d, want 200", status)
	}
	if len(list.Data) != 0 {
		t.Fatalf("a fresh workspace lists %d refusals, want 0", len(list.Data))
	}

	// Blocking a vendor. The domain is stored in the registrable form the
	// matcher keys on, whatever spelling the caller used.
	var blocked blockedDomainDTO
	if status := e.Call(t, "PUT", "/v1/capture/blocked-domains",
		map[string]string{
			"domain": "Mail.Expensify.Example", "admission": "suppressed",
			"reason": "a tool we use, not a customer",
		}, nil, &blocked); status != http.StatusOK {
		t.Fatalf("PUT suppressed → %d, want 200", status)
	}
	if blocked.Domain != "expensify.example" {
		t.Fatalf("stored domain %q, want the registrable form", blocked.Domain)
	}
	if blocked.Source != "human" {
		t.Fatalf("source = %q, want human — the admin surface may not claim a machine made this", blocked.Source)
	}
	if blocked.Reason == "" || blocked.DecidedAt == "" {
		t.Fatalf("stored %+v, want a reason and a decision time an operator can read", blocked)
	}

	// It appears in the list, with what decided it.
	if status := e.Call(t, "GET", "/v1/capture/blocked-domains", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("GET after PUT → %d, want 200", status)
	}
	if len(list.Data) != 1 || list.Data[0].Domain != "expensify.example" {
		t.Fatalf("list = %+v, want the refusal that was just recorded", list.Data)
	}

	// Unblocking is the same endpoint, and the decision flips.
	var admitted blockedDomainDTO
	if status := e.Call(t, "PUT", "/v1/capture/blocked-domains",
		map[string]string{
			"domain": "expensify.example", "admission": "admitted",
			"reason": "they became a client",
		}, nil, &admitted); status != http.StatusOK {
		t.Fatalf("PUT admitted → %d, want 200", status)
	}
	if admitted.Admission != "admitted" || admitted.Reason != "they became a client" {
		t.Fatalf("stored %+v, want the admission recorded with its reason", admitted)
	}

	// A reason is required: a refusal nobody can explain is one nobody can
	// review, so the surface refuses to store one.
	if status := e.Call(t, "PUT", "/v1/capture/blocked-domains",
		map[string]string{"domain": "nothing.example", "admission": "suppressed", "reason": ""},
		nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("PUT with no reason → %d, want 422", status)
	}
	// And so is a real domain.
	if status := e.Call(t, "PUT", "/v1/capture/blocked-domains",
		map[string]string{"domain": "not a domain", "admission": "suppressed", "reason": "x"},
		nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("PUT with a non-domain → %d, want 422", status)
	}
	// The contract's maxLength is enforced here, because the generated type
	// does not: unchecked, one caller stores a megabyte per domain and every
	// reader of the list is served it back.
	if status := e.Call(t, "PUT", "/v1/capture/blocked-domains",
		map[string]string{
			"domain": "verbose.example", "admission": "suppressed",
			"reason": strings.Repeat("x", 501),
		}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("PUT with an over-long reason → %d, want 422", status)
	}
}
