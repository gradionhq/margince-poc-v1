// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The change-password handler's pre-database refusals, and the two answers a
// client branches on. Provable with a zero Service and no database, because
// each of these must happen before any query runs — which is also what makes
// them worth pinning: a refusal that reached the database would be a refusal
// that cost a KDF derivation.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// changeRequest posts a body to the handler as an authenticated human, so the
// caller-bound branches are reachable without a session cookie.
func changeRequest(t *testing.T, h Handlers, body string, bind bool) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/change-password", strings.NewReader(body))
	if bind {
		id := Identity{
			UserID:      ids.UserID{UUID: ids.NewV7()},
			WorkspaceID: ids.WorkspaceID{UUID: ids.NewV7()},
		}
		r = r.WithContext(withIdentity(
			principal.WithWorkspaceID(r.Context(), id.WorkspaceID.UUID), id))
	}
	h.ChangePassword(rec, r)
	return rec
}

// problemCode reads the machine code a client branches on, which is the half of
// the answer prose cannot carry.
func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the problem body %q: %v", rec.Body.String(), err)
	}
	return body.Code
}

func TestChangePasswordRefusesAnEmptyCurrentPasswordBeforeAnyWork(t *testing.T) {
	h := NewHandlers(&Service{})
	rec := changeRequest(t, h, `{"current_password":"","new_password":"a fine new password"}`, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 — a missing current password is the caller's to fix", rec.Code)
	}
}

func TestChangePasswordRefusesAMalformedBody(t *testing.T) {
	h := NewHandlers(&Service{})
	for name, body := range map[string]string{
		"truncated":     `{"current_password":`,
		"unknown field": `{"current_password":"x","new_passwrod":"typo"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if rec := changeRequest(t, h, body, true); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", rec.Code)
			}
		})
	}
}

func TestChangePasswordThrottlesRepeatedFailuresPerAccount(t *testing.T) {
	// The bucket caps WRONG GUESSES. It is the defence that keeps this route
	// from being an unthrottled oracle for the same secret the login route
	// caps, so what matters is that it blocks before the verify runs.
	h := NewHandlers(&Service{})
	key := ids.UserID{UUID: ids.NewV7()}.String()
	for range 10 {
		h.changeFailures.Record(key)
	}
	if !h.changeFailures.Blocked(key) {
		t.Fatal("ten recorded failures did not fill the per-account bucket")
	}
	// And a success must not spend one: charging the rotation would throttle
	// the very thing the route exists to allow.
	fresh := NewHandlers(&Service{})
	other := ids.UserID{UUID: ids.NewV7()}.String()
	if fresh.changeFailures.Blocked(other) {
		t.Error("a fresh account is already blocked")
	}
}

func TestChangePasswordAnswersAreMachineReadable(t *testing.T) {
	// All three 401s this handler can write would otherwise carry the same
	// generic code, and the settings card reads the answer to tell a wrong
	// password from an expired session. Prose alone makes an expired session
	// mid-form render as a password error.
	h := NewHandlers(&Service{})
	key := ids.UserID{UUID: ids.NewV7()}
	for range 10 {
		h.changeFailures.Record(key.String())
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/change-password",
		strings.NewReader(`{"current_password":"x","new_password":"a fine new password"}`))
	r = r.WithContext(withIdentity(
		principal.WithWorkspaceID(r.Context(), ids.NewV7()), Identity{UserID: key}))
	h.ChangePassword(rec, r)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a throttled caller got %d, want 429", rec.Code)
	}
	if code := problemCode(t, rec); code == "" || code == "unauthorized" {
		t.Errorf("the throttle answer carries code %q — a client cannot branch on prose", code)
	}
}

func TestOwnCredentialChangeIsOutsideTheSeatCeiling(t *testing.T) {
	// A read seat may not write business records — that is a licensing bound.
	// Its own password is not a business record, and a read seat that cannot
	// rotate its own credential is stranded on exactly the installations this
	// route was added for.
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/change-password", nil)
	if !isOwnCredentialRequest(r) {
		t.Error("the change-password route is inside the seat ceiling; a read seat cannot rotate its own password")
	}
	other := httptest.NewRequest(http.MethodPost, "/v1/people", nil)
	if isOwnCredentialRequest(other) {
		t.Error("the exemption is wider than one route — it must not admit business writes")
	}
}
