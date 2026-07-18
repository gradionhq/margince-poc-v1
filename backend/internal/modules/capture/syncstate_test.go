// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The classifier is what stands between a provider hiccup and a dead
// connection: each class schedules differently, so a misclassification is a
// scheduling bug, not a cosmetic one. (That each provider's package errors
// answer the shared vocabulary is the provider packages' own tests.)
func TestClassifySyncError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want errorClass
	}{
		{"rate limit carries its class", &connector.RateLimitedError{RetryAfter: time.Minute}, classRateLimited},
		{"auth parks", fmt.Errorf("provider: %w", connector.ErrAuthRejected), classAuth},
		{"unreachable backs off", fmt.Errorf("provider: %w", connector.ErrUnreachable), classUnreachable},
		{"cursor gone is its own class", fmt.Errorf("provider: %w", connector.ErrCursorGone), classHistoryGone},
		{"anything else is our bug", errors.New("nil map write"), classInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySyncError(tc.err); got != tc.want {
				t.Fatalf("classify(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

func TestBackoffDelayLadder(t *testing.T) {
	// 2min·2^n with ±20% jitter: assert the envelope, not the die roll.
	for n, base := range map[int]time.Duration{
		0: 2 * time.Minute,
		1: 4 * time.Minute,
		3: 16 * time.Minute,
	} {
		got := backoffDelay(n)
		lo, hi := time.Duration(float64(base)*0.8), time.Duration(float64(base)*1.2)
		if got < lo || got > hi {
			t.Fatalf("backoffDelay(%d) = %s, want within [%s, %s]", n, got, lo, hi)
		}
	}
	// The cap: past the ladder's top every delay stays inside the jittered
	// 4h envelope — a 20th failure must not schedule next year.
	got := backoffDelay(30)
	if hi := time.Duration(float64(backoffCap) * 1.2); got > hi {
		t.Fatalf("backoffDelay(30) = %s, exceeds the jittered cap %s", got, hi)
	}
	if lo := time.Duration(float64(backoffCap) * 0.8); got < lo {
		t.Fatalf("backoffDelay(30) = %s, below the jittered cap floor %s", got, lo)
	}
}

func TestRateLimitedErrorSpeaksTheSharedVocabulary(t *testing.T) {
	err := fmt.Errorf("gmail: messages.get: %w", &connector.RateLimitedError{RetryAfter: 30 * time.Second})
	if !errors.Is(err, connector.ErrRateLimited) {
		t.Fatal("a wrapped RateLimitedError must answer errors.Is(ErrRateLimited)")
	}
	var rl *connector.RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second {
		t.Fatalf("Retry-After lost through the wrap: %v", rl)
	}
}
