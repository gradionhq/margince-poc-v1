// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// TestFxBootstrapCurrenciesFallsBackToTheDefault pins the fresh-install
// contract: an operator who configures no candidate set still gets the
// USD/GBP/CHF default, so "Refresh from sources" bootstraps an empty FX sheet
// rather than being a dead button. A configured set is used verbatim.
func TestFxBootstrapCurrenciesFallsBackToTheDefault(t *testing.T) {
	if got := strings.Join(fxBootstrapCurrencies(nil), ","); got != "USD,GBP,CHF" {
		t.Fatalf("unset config = %q, want the USD,GBP,CHF default", got)
	}
	if got := strings.Join(fxBootstrapCurrencies([]string{}), ","); got != "USD,GBP,CHF" {
		t.Fatalf("empty config = %q, want the USD,GBP,CHF default", got)
	}
	if got := strings.Join(fxBootstrapCurrencies([]string{"JPY", "SEK"}), ","); got != "JPY,SEK" {
		t.Fatalf("configured set = %q, want it used verbatim", got)
	}
}

// TestParseWorkerFlagsRejectsNonPositiveIntervals pins the boot guard:
// every scheduler interval becomes a time.Ticker period or a River
// periodic schedule, both of which misbehave on a non-positive duration (a
// Ticker panics; a non-positive River interval reschedules continuously).
// A zero or negative interval must be a boot error, never a silent default.
func TestParseWorkerFlagsRejectsNonPositiveIntervals(t *testing.T) {
	base := []string{"--dsn", "postgres://localhost/x"}
	// Strict scheduling PERIODS: both zero and negative are boot errors.
	for _, flag := range []string{
		"--runner-interval",
		"--retention-interval",
		"--close-date-interval",
		"--reconcile-interval",
		"--time-scan-interval",
		"--gmail-sync-interval",
		"--gmail-watch-interval",
		"--overlay-reconcile-interval",
	} {
		for _, bad := range []string{"0", "-1s"} {
			args := append(append([]string{}, base...), flag+"="+bad)
			if _, err := parseWorkerFlags(args); err == nil {
				t.Errorf("parseWorkerFlags(%s=%s): want a boot error, got nil", flag, bad)
			} else if !strings.Contains(err.Error(), flag[2:]) {
				t.Errorf("parseWorkerFlags(%s=%s): error %q should name the offending flag", flag, bad, err)
			}
		}
	}
	// gmail-watch-renew-within is a renewal THRESHOLD, not a period: zero is
	// valid (renew already-expired watches), negative is not.
	if _, err := parseWorkerFlags(append(append([]string{}, base...), "--gmail-watch-renew-within=0")); err != nil {
		t.Errorf("parseWorkerFlags(--gmail-watch-renew-within=0): want acceptance, got %v", err)
	}
	if _, err := parseWorkerFlags(append(append([]string{}, base...), "--gmail-watch-renew-within=-1s")); err == nil {
		t.Error("parseWorkerFlags(--gmail-watch-renew-within=-1s): want a boot error, got nil")
	}
	// A negative overlay backfill limit is rejected; zero (uncapped) is fine.
	if _, err := parseWorkerFlags(append(append([]string{}, base...), "--overlay-backfill-limit=-1")); err == nil {
		t.Error("parseWorkerFlags(--overlay-backfill-limit=-1): want a boot error, got nil")
	}
}

// TestParseWorkerFlagsAcceptsPositiveIntervals proves the guard does not
// reject the ordinary positive case (a smoke test so a stricter bound can
// never silently reject a valid boot).
func TestParseWorkerFlagsAcceptsPositiveIntervals(t *testing.T) {
	cfg, err := parseWorkerFlags([]string{"--dsn", "postgres://localhost/x", "--runner-interval=15s"})
	if err != nil {
		t.Fatalf("parseWorkerFlags with a positive interval: %v", err)
	}
	if cfg.runnerInterval.String() != "15s" {
		t.Errorf("runnerInterval = %s, want 15s", cfg.runnerInterval)
	}
}
