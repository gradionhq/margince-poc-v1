// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

// TestReleaseGuardStopsATornSetAndLetsAnUpgradeThrough walks the whole life of
// the guard against a real database, through the two functions the process roles
// actually call: the api records, every other role asserts.
//
// One scenario rather than a test per branch, because the branches are not
// independent — what a role may do next depends on what the api has recorded so
// far, and the ordering is the part worth pinning. A torn set has to stop; an
// upgrade and a rollback have to get through; and an unstamped binary has to do
// neither, in either direction.
func TestReleaseGuardStopsATornSetAndLetsAnUpgradeThrough(t *testing.T) {
	env := Setup(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	record := func(version string) {
		t.Helper()
		if err := compose.RecordInstallationRelease(ctx, env.Pool, logger, version); err != nil {
			t.Fatalf("RecordInstallationRelease(%q): %v", version, err)
		}
	}
	mustStart := func(version string) {
		t.Helper()
		if err := compose.AssertInstallationRelease(ctx, env.Pool, logger, version); err != nil {
			t.Fatalf("a role at release %q refused to start: %v", version, err)
		}
	}
	mustRefuse := func(version string) {
		t.Helper()
		if err := compose.AssertInstallationRelease(ctx, env.Pool, logger, version); err == nil {
			t.Fatalf("a role at release %q started against a different installation release; a mixed set must not run", version)
		}
	}
	rows := func() int {
		t.Helper()
		return env.WsCount(t, `SELECT count(*) FROM system_log WHERE action = 'release.version_observed'`)
	}

	// Nothing recorded yet: no api on this installation has ever carried a
	// release, so there is nothing to disagree with and every role starts.
	mustStart("1970.41")
	mustStart("1970.42")

	// The api records its release. A restart at the same release records nothing
	// more — the ledger is the installation's upgrade history, not its boot count.
	record("1970.42")
	if got := rows(); got != 1 {
		t.Fatalf("recording a release wrote %d rows, want 1", got)
	}
	record("1970.42")
	if got := rows(); got != 1 {
		t.Fatalf("restarting at the same release wrote a new row (%d total), want still 1", got)
	}

	// The set as pulled: a worker from the same release runs, one from another
	// release does not.
	mustStart("1970.42")
	mustRefuse("1970.41")
	mustRefuse("1970.43")

	// An unstamped binary is not a release, in either direction. It never
	// refuses, and — the half that is easy to get wrong — it never erases the
	// release a real api recorded, which would silently disarm the guard for
	// every role that boots after it.
	mustStart("dev")
	mustStart("")
	record("dev")
	record("")
	if got := rows(); got != 1 {
		t.Fatalf("an unstamped api wrote %d rows, want still 1", got)
	}
	mustRefuse("1970.41")

	// An upgrade: the api moves first by definition, and the roles that follow
	// match the release it recorded. The role still on the old one now refuses,
	// which is what makes the rollout converge instead of deadlock.
	record("1970.43")
	if got := rows(); got != 2 {
		t.Fatalf("an upgrade wrote %d rows, want 2", got)
	}
	mustStart("1970.43")
	mustRefuse("1970.42")

	// A rollback is an ordinary move, not a special case: the api states the
	// release, so going backwards needs no permission. This is why the guard
	// compares for equality and never for order.
	record("1970.42")
	if got := rows(); got != 3 {
		t.Fatalf("a rollback wrote %d rows, want 3", got)
	}
	mustStart("1970.42")
	mustRefuse("1970.43")
}
