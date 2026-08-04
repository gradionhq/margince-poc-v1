// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// /me carries the authorization snapshot the web client scopes its affordances
// with. These prove the two ways that snapshot can be wrong while still looking
// right: a grant serialized under the wrong key names, and a seat that fails
// open.
//
// Both failures are invisible in a struct comparison — the Go value is correct
// in each case — so these assert against the JSON the client actually receives.

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The mapping in meResponse exists because principal.ObjectGrant carries no
// JSON tags: handed to the encoder directly it emits Create/Read/Update/Delete,
// the client's lowercase lookups all miss, and every affordance disappears as
// though the permission had been withheld. This asserts the wire spelling
// rather than the Go value, so replacing the mapping with a direct
// serialization fails here instead of in a browser.
func TestMeResponseAuthorizationUsesTheContractSpelling(t *testing.T) {
	// Deliberately asymmetric: a grant that is true everywhere would still
	// match if create and delete were transposed by a hand-written mapping.
	id := Identity{
		// A real address: openapi_types.Email regex-validates on marshal, so an
		// empty one would fail the encode before reaching what this asserts.
		Email:    "rep@example.com",
		SeatType: "full",
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"custom_field": {Create: true, Update: true},
			},
		},
	}

	var decoded struct {
		Authorization struct {
			SeatType string                     `json:"seat_type"`
			Objects  map[string]map[string]bool `json:"objects"`
		} `json:"authorization"`
	}
	raw, err := json.Marshal(meResponse(id, crmcontracts.Native, false))
	if err != nil {
		t.Fatalf("marshalling /me: %v", err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding /me: %v", err)
	}

	want := map[string]bool{"create": true, "read": false, "update": true, "delete": false}
	got, ok := decoded.Authorization.Objects["custom_field"]
	if !ok {
		t.Fatalf("authorization.objects carries no custom_field key; got %v", decoded.Authorization.Objects)
	}
	// An exact map comparison, not per-key lookups: a capitalized key would
	// leave every lowercase lookup false, which reads identically to a
	// correctly denied grant.
	if !maps.Equal(got, want) {
		t.Errorf("authorization.objects.custom_field = %v, want %v — the grant is not reaching the "+
			"client under the key names the contract declares", got, want)
	}
}

// The seat ceiling is checked before RBAC and denies on its own, so a snapshot
// that overstates it invites the client to offer actions the server refuses.
// An unrecognized seat must therefore report `read`: the value that denies.
func TestMeResponseSeatTypeFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		seat string
		want crmcontracts.AuthorizationSeatType
	}{
		{"full seat mutates", "full", crmcontracts.AuthorizationSeatTypeFull},
		{"read seat does not", "read", crmcontracts.AuthorizationSeatTypeRead},
		// Neither is reachable through the schema (app_user.seat_type is NOT
		// NULL and CHECK-constrained to read|full), so these pin the posture
		// for a seat that arrives unresolved rather than a live case.
		{"unset seat denies", "", crmcontracts.AuthorizationSeatTypeRead},
		{"unknown seat denies", "enterprise", crmcontracts.AuthorizationSeatTypeRead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := meResponse(Identity{SeatType: tt.seat}, crmcontracts.Native, false)
			if got.Authorization == nil {
				t.Fatal("authorization must always be present on a human /me")
			}
			if got.Authorization.SeatType != tt.want {
				t.Errorf("seat_type = %q, want %q", got.Authorization.SeatType, tt.want)
			}
			if !got.Authorization.SeatType.Valid() {
				t.Errorf("seat_type %q is not a value the contract enum declares",
					got.Authorization.SeatType)
			}
		})
	}
}

// The snapshot is per-principal. Served from a shared cache it would hand one
// user another's capabilities; stored, it would outlive the role change that
// revoked them.
func TestGetCurrentPrincipalForbidsCaching(t *testing.T) {
	// A real address, for the reason the mapping test gives: an empty one fails
	// the generated Email type's marshal, and this assertion would then be
	// reading headers off a response that never serialized.
	req := httptest.NewRequest(http.MethodGet, "/me", nil).
		WithContext(withIdentity(t.Context(), Identity{
			Email:    "rep@example.com",
			SeatType: "full",
		}))
	rec := httptest.NewRecorder()

	Handlers{}.GetCurrentPrincipal(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "private, no-store")
	}
}

// A request carrying no session identity is refused outright, rather than
// answered with a partial profile.
//
// This is the HALF of the passport story that lives in this handler. It does
// not exercise serveAsAgent, so it does not prove that a passport bearer
// arrives without an identity — only that a caller who does is turned away. The
// other half is serveAsAgent binding no Identity, which its own tests cover;
// the contract's claim that the passport field is permanently null rests on
// both, not on this test alone.
func TestGetCurrentPrincipalRejectsAPrincipalWithoutASession(t *testing.T) {
	rec := httptest.NewRecorder()

	Handlers{}.GetCurrentPrincipal(rec, httptest.NewRequest(http.MethodGet, "/me", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — an agent must not receive a human profile", rec.Code)
	}
}
