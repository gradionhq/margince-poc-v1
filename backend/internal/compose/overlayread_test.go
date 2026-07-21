// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

// TestClampOverlaySearchLimit pins the guard on the overlay search limit:
// a bound integer that slips past request validation (a negative or an
// oversized ?limit=) must never reach a slice capacity, so it is clamped
// to the contract's 1..100 range before it sizes any allocation.
func TestClampOverlaySearchLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{-1, 1},
		{0, 1},
		{1, 1},
		{25, 25},
		{100, 100},
		{101, overlaySearchMaxLimit},
		{1 << 30, overlaySearchMaxLimit},
	}
	for _, c := range cases {
		if got := clampOverlaySearchLimit(c.in); got != c.want {
			t.Errorf("clampOverlaySearchLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
