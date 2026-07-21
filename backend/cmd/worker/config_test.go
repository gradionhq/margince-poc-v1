// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// TestParseWorkerFlagsRejectsNonPositiveIntervals pins the boot guard:
// every scheduler interval becomes a time.Ticker period or a River
// periodic schedule, both of which misbehave on a non-positive duration (a
// Ticker panics; a non-positive River interval reschedules continuously).
// A zero or negative interval must be a boot error, never a silent default.
func TestParseWorkerFlagsRejectsNonPositiveIntervals(t *testing.T) {
	base := []string{"--dsn", "postgres://localhost/x"}
	for _, flag := range []string{
		"--runner-interval",
		"--retention-interval",
		"--close-date-interval",
		"--reconcile-interval",
		"--time-scan-interval",
		"--gmail-sync-interval",
		"--gmail-watch-interval",
		"--gmail-watch-renew-within",
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
