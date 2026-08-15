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

// The licensee detail the module proved, onto the wire. What matters here is
// which claims survive and which verdicts the server states for the client.
func TestToContractLicenseHolder(t *testing.T) {
	t.Parallel()
	full := licensecheck.License{
		ID:           "0199c4f2-1d6e-7a41-9f0b-7b2a2c1d5e30",
		Subject:      "acme-prod",
		Org:          "Acme GmbH",
		ContactName:  "Ada Lovelace",
		ContactEmail: "ada@acme.example",
		Expiry:       resolvedAt.AddDate(1, 0, 0),
	}

	t.Run("a complete license renders every claim", func(t *testing.T) {
		t.Parallel()
		got := toContractLicenseHolder(full, resolvedAt)
		if got.Id != full.ID || got.Subject != "acme-prod" {
			t.Errorf("identifiers = %q / %q", got.Id, got.Subject)
		}
		for name, field := range map[string]*string{
			"org": got.Org, "contact_name": got.ContactName, "contact_email": got.ContactEmail,
		} {
			if field == nil {
				t.Errorf("%s is absent for a license that carries it", name)
			}
		}
		if got.RenewalDue {
			t.Error("a license a year from expiry asks for a renewal")
		}
	})

	// A license issued before those claims existed verifies exactly like any
	// other. Its rows are ABSENT, so a client renders what it has rather than
	// three empty placeholders.
	t.Run("a license from before the claims existed renders none of them", func(t *testing.T) {
		t.Parallel()
		got := toContractLicenseHolder(licensecheck.License{
			ID: full.ID, Subject: full.Subject, Expiry: full.Expiry,
		}, resolvedAt)
		if got.Org != nil || got.ContactName != nil || got.ContactEmail != nil {
			t.Errorf("absent claims rendered as %v / %v / %v", got.Org, got.ContactName, got.ContactEmail)
		}
		if got.Id == "" || got.Subject == "" {
			t.Error("the identifiers every license carries went missing")
		}
	})

	t.Run("the renewal window opens at ninety days and not before", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name   string
			expiry time.Time
			want   bool
		}{
			{name: "ninety-one days out", expiry: resolvedAt.Add(91 * 24 * time.Hour)},
			{name: "exactly ninety days out", expiry: resolvedAt.Add(renewalWindow), want: true},
			{name: "a week out", expiry: resolvedAt.Add(7 * 24 * time.Hour), want: true},
			{name: "already past", expiry: resolvedAt.Add(-time.Hour), want: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				license := full
				license.Expiry = tc.expiry
				if got := toContractLicenseHolder(license, resolvedAt).RenewalDue; got != tc.want {
					t.Errorf("RenewalDue = %v, want %v", got, tc.want)
				}
			})
		}
	})

	// A client that reads only `renewal_due` must still warn about a license
	// living on its grace period.
	t.Run("a license on its grace period always asks for a renewal", func(t *testing.T) {
		t.Parallel()
		license := full
		license.InGrace = true
		got := toContractLicenseHolder(license, resolvedAt)
		if !got.InGrace {
			t.Error("InGrace was dropped")
		}
		if !got.RenewalDue {
			t.Error("a license in grace does not ask for a renewal; a client reading one field would say nothing")
		}
	})
}

// The holder rides only a verified license. An unlicensed installation has no
// licensee, and a refused one proved nothing about who holds it.
func TestLicenseEntitlementCarriesTheHolderOnlyWhenValid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		posture licensecheck.Posture
		want    bool
	}{
		{
			name: "valid",
			posture: licensecheck.Posture{
				State:     licensecheck.StateValid,
				Grants:    licensecheck.Grants{licensecheck.SeatsAttribute: float64(10)},
				License:   licensecheck.License{ID: "0199", Subject: "acme-prod", Expiry: resolvedAt.AddDate(1, 0, 0)},
				CheckedAt: resolvedAt,
			},
			want: true,
		},
		{name: "absent", posture: licensecheck.Posture{State: licensecheck.StateAbsent, CheckedAt: resolvedAt}},
		{name: "rejected", posture: licensecheck.Posture{State: licensecheck.StateRejected, CheckedAt: resolvedAt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := toContractLicenseEntitlement(tc.posture, 3)
			if (got.License != nil) != tc.want {
				t.Errorf("License present = %v, want %v", got.License != nil, tc.want)
			}
		})
	}
}
