// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore

import (
	"testing"
	"time"
)

func TestBackoffGrowsThenCaps(t *testing.T) {
	if got := backoff(0); got != 200*time.Millisecond {
		t.Errorf("backoff(0) = %v, want 200ms", got)
	}
	if got := backoff(2); got != 600*time.Millisecond {
		t.Errorf("backoff(2) = %v, want 600ms", got)
	}
	// Grows without bound in attempt but is capped at 2s.
	if got := backoff(100); got != 2*time.Second {
		t.Errorf("backoff(100) = %v, want the 2s cap", got)
	}
}
