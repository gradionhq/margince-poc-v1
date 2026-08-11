// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package webhooks

import (
	"os"
	"path/filepath"
	"testing"
)

// backendModuleRoot is the directory holding the backend module's go.mod, for the
// schema conformance suite that reads api/public-events.yaml off disk.
//
// This is a second copy of a helper the parent package also has, and it is not an
// oversight: the parent's copy lives in overlay_acceptance_seam_test.go, which
// carries NO integration build tag because it belongs to the unit lane. A
// definition reachable from both lanes would have to sit in an untagged, non-test
// file, which would pull package integration — and `testing` with it — into
// ordinary builds. Two small copies across a build-tag boundary is the cheaper
// trade.
//
// It SEARCHES upward rather than counting parents. The parent's copy joins three
// ".." hops, which is right only at its own depth; this package sits one level
// deeper, so a counted version here would resolve to internal/compose and fail on
// the go.mod check. Searching is depth-independent, which is what a suite package
// needs.
func backendModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod in any parent of %q — cannot locate the backend module root", dir)
		}
		dir = parent
	}
}
