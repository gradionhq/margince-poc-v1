// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The licensed seat ceiling as an admin meets it: the number the entitlement
// screen shows is the number the invite is refused against, and the refusal
// arrives as a verdict a client can act on rather than a server fault.
//
// It runs over the composed api because the wiring is the thing under test. The
// posture reaches identity's seat writer through the composition root, and the
// gap that shape exists to close — a role reporting a ceiling it does not
// enforce — is invisible to any test that calls the service directly.

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
)

// licenseEntitlement is the GET /installation/license body, named here rather
// than decoded into the generated type so the test reads the WIRE — a field
// renamed in the contract has to fail this, not follow it.
type licenseEntitlement struct {
	State        string `json:"state"`
	SeatsGranted *int   `json:"seats_granted"`
	SeatsUsed    int    `json:"seats_used"`
	OverLimit    bool   `json:"over_limit"`
}

// problemDocument is the RFC 7807 shape every refusal on this surface takes.
type problemDocument struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// setupLicensedApp composes an api whose license grants whatever the returned
// counter is set to. The grant is read at CALL time — as the real posture is,
// since a watcher re-resolves it while the process runs — so a test can license
// the installation for exactly the seats it turns out to be using.
func setupLicensedApp(t *testing.T) (*apptest.AppEnv, *atomic.Int64) {
	t.Helper()
	granted := &atomic.Int64{}
	e := apptest.SetupAppWithOptions(t, compose.WithLicensePosture(func() licensecheck.Posture {
		return licensecheck.Posture{
			State:     licensecheck.StateValid,
			Grants:    licensecheck.Grants{licensecheck.SeatsAttribute: float64(granted.Load())},
			CheckedAt: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC),
		}
	}))
	e.BootstrapWorkspace(t)
	return e, granted
}

// invite asks for one more member and reports what the surface answered. A
// refusal decodes into refusal when one is passed; a 201 body carries nothing
// this file asserts on, and the entitlement read is what proves the seat.
func invite(t *testing.T, e *apptest.AppEnv, email string, refusal *problemDocument) int {
	t.Helper()
	body := map[string]string{"email": email, "display_name": "Overflow", "role": "rep"}
	if refusal == nil {
		return e.Call(t, http.MethodPost, "/v1/users", body, nil, nil)
	}
	return e.Call(t, http.MethodPost, "/v1/users", body, nil, refusal)
}

func TestInviteAtTheLicensedCeilingIsRefusedWithTheNumberTheScreenShows(t *testing.T) {
	e, granted := setupLicensedApp(t)

	var before licenseEntitlement
	if status := e.Call(t, http.MethodGet, "/v1/installation/license", nil, nil, &before); status != http.StatusOK {
		t.Fatalf("GET /installation/license = %d, want 200", status)
	}
	if before.SeatsUsed == 0 {
		t.Fatal("a bootstrapped installation reports no seats in use; every assertion below would hold vacuously")
	}
	// Licensed for exactly what it is using: the installation is full, and the
	// screen says so before anybody is refused anything.
	granted.Store(int64(before.SeatsUsed))

	var refusal problemDocument
	status := invite(t, e, "one-too-many@seatceiling.test", &refusal)
	if status != http.StatusForbidden || refusal.Code != "seat_limit_reached" {
		t.Fatalf("invite at the ceiling = %d %q, want 403 seat_limit_reached", status, refusal.Code)
	}
	// A verdict, not an outage: the detail says what happened and what to do
	// about it, which is the difference between this and the opaque 500 an
	// unclassified error would have produced.
	if refusal.Detail == "" {
		t.Error("the refusal carries no detail, so the admin is told a number-free 'forbidden'")
	}

	var after licenseEntitlement
	e.Call(t, http.MethodGet, "/v1/installation/license", nil, nil, &after)
	if after.SeatsUsed != before.SeatsUsed {
		t.Errorf("seats in use = %d after the refused invite, want %d", after.SeatsUsed, before.SeatsUsed)
	}
	if after.OverLimit {
		t.Error("the installation reports itself over its limit after a refusal that was supposed to prevent exactly that")
	}
}

// The other side of the same wiring: one more licensed seat and the same
// request succeeds. Without this, a gate that refused everything would pass the
// test above.
func TestInviteBelowTheLicensedCeilingIsAdmitted(t *testing.T) {
	e, granted := setupLicensedApp(t)

	var before licenseEntitlement
	e.Call(t, http.MethodGet, "/v1/installation/license", nil, nil, &before)
	granted.Store(int64(before.SeatsUsed + 1))

	if status := invite(t, e, "welcome@seatceiling.test", nil); status != http.StatusCreated {
		t.Fatalf("invite into a licensed seat = %d, want 201", status)
	}

	var after licenseEntitlement
	e.Call(t, http.MethodGet, "/v1/installation/license", nil, nil, &after)
	if after.SeatsUsed != before.SeatsUsed+1 {
		t.Errorf("seats in use = %d after the invite, want %d", after.SeatsUsed, before.SeatsUsed+1)
	}
	if after.SeatsGranted == nil || *after.SeatsGranted != after.SeatsUsed {
		t.Errorf("the screen reports granted=%v used=%d; the installation should now be exactly full",
			after.SeatsGranted, after.SeatsUsed)
	}
	// And the next one is refused, on the same running process: the ceiling is
	// read live, so an installation that fills up starts refusing without a
	// restart.
	var refusal problemDocument
	if status := invite(t, e, "and-one-more@seatceiling.test", &refusal); status != http.StatusForbidden {
		t.Errorf("invite once the licensed seats are used = %d %q, want 403", status, refusal.Code)
	}
}
