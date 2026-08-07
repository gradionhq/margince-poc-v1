// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The wire half of the provenance filter (ADR-0075/A121 §3a). The store-level
// semantics live in compose/capturedbykind_integration_test.go; what is proved
// here is the thing the store cannot see — that an unknown value is REFUSED at
// the edge rather than passed through.
//
// Declaring an enum in the contract does not enforce it: the generated binding
// checks only that the parameter is a string. Without the handler check, a typo
// like `captured_by_kind=ai` builds a clause that matches nothing and returns
// an empty page, which reads exactly like "no AI created anything here". A
// filter whose failure mode is a confident wrong answer is worse than no
// filter, so the refusal is the behaviour under test.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

func TestCapturedByKindRefusesAValueOutsideTheEnum(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	for _, path := range []string{
		"/v1/people?captured_by_kind=ai",
		"/v1/organizations?captured_by_kind=robot",
		"/v1/leads?captured_by_kind=Agent",
		// Present-but-empty is a VALUE, and not one the enum has. Reading it as
		// "no filter" would answer the whole list to a caller who did ask to
		// filter — the same wrong answer, arrived at more quietly.
		"/v1/people?captured_by_kind=",
		"/v1/organizations?captured_by_kind=",
		"/v1/leads?captured_by_kind=",
	} {
		if status := e.Call(t, "GET", path, nil, nil, nil); status != http.StatusUnprocessableEntity {
			t.Errorf("GET %s = %d, want 422 — an unusable provenance kind must be refused, not answered with an unfiltered page", path, status)
		}
	}

	// The known values still answer, so the refusal above is about the
	// vocabulary rather than the parameter being rejected outright.
	for _, path := range []string{
		"/v1/people?captured_by_kind=agent",
		"/v1/organizations?captured_by_kind=human",
		"/v1/leads?captured_by_kind=connector",
		"/v1/people?captured_by_kind=system",
	} {
		if status := e.Call(t, "GET", path, nil, nil, nil); status != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, status)
		}
	}

	// Omitting the parameter entirely is the one thing that means "no filter".
	if status := e.Call(t, "GET", "/v1/people", nil, nil, nil); status != http.StatusOK {
		t.Errorf("GET /v1/people = %d, want 200 — an absent filter is not an unusable one", status)
	}
}
