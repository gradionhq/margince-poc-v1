// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The workspace consumer-mail list over the real wire (CAP-PARAM-5): what an
// admin can say about a domain, what the surface refuses, and that a withdrawal
// really returns the workspace to the shipped baseline's answer.
//
// The list decides whether a domain can ever name a company, so its refusals
// matter as much as its writes: an entry the matcher could never read would
// leave an admin believing they had corrected something.

import (
	"net/http"
	"testing"
)

type consumerMailDomainDTO struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
	Kind   string `json:"kind"`
}

type consumerMailDomainListDTO struct {
	Data []consumerMailDomainDTO `json:"data"`
}

func TestConsumerMailDomainsOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A workspace that has said nothing: the shipped baseline decides every
	// domain, and the list is empty rather than absent.
	var list consumerMailDomainListDTO
	if status := e.call(t, "GET", "/v1/capture/consumer-mail-domains", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("GET → %d, want 200", status)
	}
	if len(list.Data) != 0 {
		t.Fatalf("a fresh workspace lists %d entries, want 0", len(list.Data))
	}

	// A provider the shipped dataset missed, and a domain it wrongly claims.
	var added consumerMailDomainDTO
	if status := e.call(t, "POST", "/v1/capture/consumer-mail-domains",
		map[string]string{"domain": "Regional-Mail.Example", "kind": "extra"}, nil, &added); status != http.StatusCreated {
		t.Fatalf("POST extra → %d, want 201", status)
	}
	// Stored in the form the matcher keys on, lower-cased: an entry that keeps
	// the caller's spelling is an entry that never matches anything.
	if added.Domain != "regional-mail.example" || added.Kind != "extra" {
		t.Fatalf("created %+v, want the normalized domain", added)
	}
	var carved consumerMailDomainDTO
	if status := e.call(t, "POST", "/v1/capture/consumer-mail-domains",
		map[string]string{"domain": "mail.gmx.de", "kind": "never"}, nil, &carved); status != http.StatusCreated {
		t.Fatalf("POST never → %d, want 201", status)
	}
	// A subdomain means its registrable domain — the matcher reads eTLD+1.
	if carved.Domain != "gmx.de" {
		t.Fatalf("carve-out stored as %q, want gmx.de", carved.Domain)
	}

	if status := e.call(t, "GET", "/v1/capture/consumer-mail-domains", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("GET → %d, want 200", status)
	}
	if len(list.Data) != 2 {
		t.Fatalf("list holds %d entries, want 2", len(list.Data))
	}

	// Re-adding the same domain is the same entry, not a second row: a domain
	// cannot be both added and carved out.
	var readded consumerMailDomainDTO
	if status := e.call(t, "POST", "/v1/capture/consumer-mail-domains",
		map[string]string{"domain": "regional-mail.example", "kind": "never"}, nil, &readded); status != http.StatusCreated {
		t.Fatalf("re-POST → %d, want 201", status)
	}
	if readded.ID != added.ID || readded.Kind != "never" {
		t.Fatalf("re-add = %+v, want the existing row %s switched to never", readded, added.ID)
	}

	// Withdrawing returns the workspace to the baseline's own answer, and doing
	// it twice is not an error — the caller's intent is already satisfied.
	if status := e.call(t, "DELETE", "/v1/capture/consumer-mail-domains/"+added.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE → %d, want 204", status)
	}
	if status := e.call(t, "DELETE", "/v1/capture/consumer-mail-domains/"+added.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("repeat DELETE → %d, want 204", status)
	}
	if status := e.call(t, "GET", "/v1/capture/consumer-mail-domains", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("GET → %d, want 200", status)
	}
	if len(list.Data) != 1 || list.Data[0].Domain != "gmx.de" {
		t.Fatalf("after the withdrawal the list is %+v, want the carve-out alone", list.Data)
	}
}

func TestConsumerMailDomainsRefusesWhatTheMatcherCouldNeverRead(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// Each of these would sit in the list looking like a correction while
	// matching nothing — or, for a bare public suffix, matching a whole TLD.
	for _, bad := range []map[string]string{
		{"domain": "localhost", "kind": "extra"},
		{"domain": "co.uk", "kind": "never"},
		{"domain": "not a domain", "kind": "extra"},
		{"domain": "acme.example", "kind": "maybe"},
		{"domain": "", "kind": "extra"},
	} {
		if status := e.call(t, "POST", "/v1/capture/consumer-mail-domains", bad, nil, nil); status != http.StatusUnprocessableEntity {
			t.Errorf("POST %v → %d, want 422", bad, status)
		}
	}
}
