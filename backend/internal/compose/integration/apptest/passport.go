// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package apptest

import (
	"net/http"
	"testing"
)

// PassportBearer mints a passport with exactly scopes and returns the
// Authorization header an agent principal presents.
//
// Minted through the real endpoint rather than written into the table, because
// what most callers are proving is that the granting human's live seat and RBAC
// still cap the passport — a hand-inserted row would skip the admission that
// caps it.
//
// It lives here because it is keyed on AppEnv and suites on both sides of the
// integration/agentaccess boundary present passports.
func PassportBearer(t *testing.T, e *AppEnv, label string, scopes ...string) map[string]string {
	t.Helper()
	var minted struct {
		Token string `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", AnyMap{
		"label": label, "scopes": scopes,
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport %q → %d", label, status)
	}
	if minted.Token == "" {
		t.Fatalf("passport %q minted without a token", label)
	}
	return map[string]string{"Authorization": "Bearer " + minted.Token}
}
