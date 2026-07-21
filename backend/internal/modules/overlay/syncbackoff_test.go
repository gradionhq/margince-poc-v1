// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The sweep-backoff pure logic in isolation: the error classification and
// the retry-ladder bounds. The DB-backed RecordSweep* round trip and the
// DueOverlayConnections due-gate are proven in the integration lane.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

func TestClassifySweepError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want sweepErrorClass
	}{
		{"rate limit", apperrors.ErrIncumbentBudgetExhausted, classSweepRateLimited},
		{"wrapped rate limit", fmt.Errorf("sweeping: %w", apperrors.ErrIncumbentBudgetExhausted), classSweepRateLimited},
		{"auth", apperrors.ErrPermissionDenied, classSweepAuth},
		{"wrapped auth", fmt.Errorf("owners: %w", apperrors.ErrPermissionDenied), classSweepAuth},
		{"anything else", errors.New("boom"), classSweepInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifySweepError(c.err); got != c.want {
				t.Errorf("classifySweepError = %q, want %q", got, c.want)
			}
		})
	}
}

// TestSweepBackoffDelayStaysWithinTheJitteredLadder pins the ladder
// bounds without depending on the ±20% jitter: delay(n) sits within ±20%
// of min(base·2^n, cap), so it grows with n and never exceeds the cap by
// more than the jitter. Asserting bounds (not an exact value) keeps the
// test deterministic despite the randomised jitter (T11).
func TestSweepBackoffDelayStaysWithinTheJitteredLadder(t *testing.T) {
	within := func(t *testing.T, got, center time.Duration) {
		t.Helper()
		lo := time.Duration(float64(center) * 0.8)
		hi := time.Duration(float64(center) * 1.2)
		if got < lo || got > hi {
			t.Fatalf("backoff = %v, want within ±20%% of %v ([%v, %v])", got, center, lo, hi)
		}
	}
	within(t, sweepBackoffDelay(0), sweepBackoffBase)   // 2m
	within(t, sweepBackoffDelay(1), 2*sweepBackoffBase) // 4m
	within(t, sweepBackoffDelay(3), 8*sweepBackoffBase) // 16m
	within(t, sweepBackoffDelay(100), sweepBackoffCap)  // capped at 4h
	if got := sweepBackoffDelay(100); got > time.Duration(float64(sweepBackoffCap)*1.2) {
		t.Fatalf("backoff at a huge failure count = %v, must never exceed cap·1.2 (%v)", got, sweepBackoffCap)
	}
}
