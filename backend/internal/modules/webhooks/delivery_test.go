// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"testing"
	"time"
)

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, backoffCap},
		{99, backoffCap}, // overflow guard: never a zero or negative gap
	}
	for _, c := range cases {
		if got := backoff(c.attempts); got != c.want {
			t.Errorf("backoff(%d) = %s, want %s", c.attempts, got, c.want)
		}
	}
}

func TestBackoffNeverZero(t *testing.T) {
	for n := 1; n <= 200; n++ {
		if backoff(n) <= 0 {
			t.Fatalf("backoff(%d) is non-positive — a delivery would hot-loop", n)
		}
	}
}
