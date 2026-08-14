// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
)

var resolvedAt = time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)

func TestToContractLicenseEntitlement(t *testing.T) {
	t.Parallel()
	granted := func(seats int) licensecheck.Posture {
		return licensecheck.Posture{
			State:     licensecheck.StateValid,
			Grants:    licensecheck.Grants{licensecheck.SeatsAttribute: float64(seats)},
			CheckedAt: resolvedAt,
		}
	}
	for _, tc := range []struct {
		name        string
		posture     licensecheck.Posture
		used        int
		wantState   string
		wantGranted *int
		wantOver    bool
	}{
		{
			name:        "inside the grant",
			posture:     granted(10),
			used:        9,
			wantState:   "valid",
			wantGranted: ptr(10),
		},
		{
			name:        "exactly at the grant is not over it",
			posture:     granted(10),
			used:        10,
			wantState:   "valid",
			wantGranted: ptr(10),
		},
		{
			name:        "one past the grant",
			posture:     granted(10),
			used:        11,
			wantState:   "valid",
			wantGranted: ptr(10),
			wantOver:    true,
		},
		{
			// The whole reason the field is nullable: rendering absent as 0 would
			// tell an admin their license permits nobody, and every seat would read
			// as over the limit.
			name:      "no license caps nothing",
			posture:   licensecheck.Posture{State: licensecheck.StateAbsent, CheckedAt: resolvedAt},
			used:      40,
			wantState: "absent",
		},
		{
			name:      "a valid license that carries no seat count caps nothing either",
			posture:   licensecheck.Posture{State: licensecheck.StateValid, Grants: licensecheck.Grants{"feature": true}, CheckedAt: resolvedAt},
			used:      40,
			wantState: "valid",
		},
		{
			name:      "a grant of zero seats is a cap, and one seat exceeds it",
			posture:   granted(0),
			used:      1,
			wantState: "valid",
			// Distinct from the absent case above: this license really does permit
			// nobody, and the meter has to be able to say so.
			wantGranted: ptr(0),
			wantOver:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := toContractLicenseEntitlement(tc.posture, tc.used)
			if string(got.State) != tc.wantState {
				t.Errorf("State = %q, want %q", got.State, tc.wantState)
			}
			if got.SeatsUsed != tc.used {
				t.Errorf("SeatsUsed = %d, want %d", got.SeatsUsed, tc.used)
			}
			switch {
			case tc.wantGranted == nil && got.SeatsGranted != nil:
				t.Errorf("SeatsGranted = %d, want it absent", *got.SeatsGranted)
			case tc.wantGranted != nil && got.SeatsGranted == nil:
				t.Errorf("SeatsGranted is absent, want %d", *tc.wantGranted)
			case tc.wantGranted != nil && *got.SeatsGranted != *tc.wantGranted:
				t.Errorf("SeatsGranted = %d, want %d", *got.SeatsGranted, *tc.wantGranted)
			}
			if got.OverLimit != tc.wantOver {
				t.Errorf("OverLimit = %v, want %v", got.OverLimit, tc.wantOver)
			}
			if !got.CheckedAt.Equal(resolvedAt) {
				t.Errorf("CheckedAt = %v, want %v", got.CheckedAt, resolvedAt)
			}
		})
	}
}

func ptr(n int) *int { return &n }

// The refusal reason never reaches the wire. It is the module's account of the
// installation's own configuration, and the module quotes token content it has
// not verified — so it belongs in the boot error and the log, not a response.
func TestLicenseEntitlementNeverCarriesTheRefusalReason(t *testing.T) {
	t.Parallel()
	rejected := licensecheck.Posture{
		State:     licensecheck.StateRejected,
		Reason:    "licensecheck: signature is not trusted",
		CheckedAt: resolvedAt,
	}
	got := toContractLicenseEntitlement(rejected, 3)
	if string(got.State) != "rejected" {
		t.Errorf("State = %q, want rejected", got.State)
	}
	if got.SeatsGranted != nil {
		t.Errorf("a refused license granted %d seats", *got.SeatsGranted)
	}
	if got.OverLimit {
		t.Error("a refused license reports being over a limit it never granted")
	}
}

// The posture reaches the handler only through the option, because the assembly
// that wires the seat count runs BEFORE the options: capturing it there would
// capture nil and answer 501 for the life of the process.
func TestWithLicensePostureReachesTheEntitlementHandler(t *testing.T) {
	t.Parallel()
	// The handler set is embedded, so its fields are reached through Server
	// itself — `posture` here IS licenseHandlers.posture.
	var srv Server
	if srv.posture != nil {
		t.Fatal("a Server nobody configured already holds a posture")
	}
	WithLicensePosture(func() licensecheck.Posture {
		return licensecheck.Posture{State: licensecheck.StateAbsent, CheckedAt: resolvedAt}
	})(&srv, nil)

	if srv.posture == nil {
		t.Fatal("the option did not reach the entitlement handler; it would answer 501 forever")
	}
	if got := srv.posture().State; got != licensecheck.StateAbsent {
		t.Errorf("the handler's posture reports %q", got)
	}
	// The same option feeds the /metrics section: one wiring point, so the screen
	// and the exposition cannot disagree about what this process resolved.
	if srv.licensePosture == nil {
		t.Error("the option did not reach the /metrics accessor")
	}
}
