// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteNewSecretRefusesAnExistingFileAsErrExist pins the error CHAIN, not
// just the failure.
//
// ensureAdminPassword decides what an existing file means by asking
// errors.Is(err, os.ErrExist). That question only works while writeNewSecret
// wraps with %w — swap it for %s and the message is identical, the error is
// still an error, and a second start silently begins failing outright instead of
// staying quiet about a credential it must not re-disclose.
func TestWriteNewSecretRefusesAnExistingFileAsErrExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := writeNewSecret(path, "first"); err != nil {
		t.Fatalf("first write: %v", err)
	}

	err := writeNewSecret(path, "second")
	if err == nil {
		t.Fatal("writing over an existing secret succeeded, so O_EXCL is not in effect")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("error does not unwrap to os.ErrExist, so ensureAdminPassword cannot tell an\n"+
			"existing credential from a real failure: %v", err)
	}

	kept, readErr := os.ReadFile(path) // #nosec G304 -- a path this test just created under t.TempDir()
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(kept) != "first" {
		t.Fatalf("the existing secret was modified: got %q, want %q", kept, "first")
	}
}

// newTestLayout returns a layout over an empty installation directory with its
// data/ present, which is the state the launcher has reached by the time it asks
// for a password.
func newTestLayout(t *testing.T) layout {
	t.Helper()
	l := layout{root: t.TempDir()}
	if err := os.MkdirAll(l.data(), 0o700); err != nil {
		t.Fatalf("prepare data dir: %v", err)
	}
	return l
}

func TestEnsureAdminPasswordDisclosesOnlyOnFirstRun(t *testing.T) {
	t.Run("the first run generates it, announces it, and stores the same value", func(t *testing.T) {
		l := newTestLayout(t)

		password, err := l.ensureAdminPassword()
		if err != nil {
			t.Fatalf("first run: %v", err)
		}
		if password == "" {
			t.Fatal("first run announced nothing, so the user never learns the password")
		}
		onDisk, err := os.ReadFile(l.adminPasswordPath())
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(onDisk) != password {
			t.Fatalf("announced %q but stored %q — the installation would be locked out of the\n"+
				"account it just reported creating", password, onDisk)
		}
	})

	// This is the branch the fix is about, and it is reachable only because
	// ensureAdminPassword no longer stats first: an existing file is now
	// discovered BY the create, which is the same code path a launcher that lost
	// the race takes. With a stat in front, this test would exercise the early
	// return instead and prove nothing about it.
	t.Run("an existing credential is kept and not re-announced", func(t *testing.T) {
		l := newTestLayout(t)
		if err := writeNewSecret(l.adminPasswordPath(), "the-persisted-password"); err != nil {
			t.Fatalf("stand in for the earlier start: %v", err)
		}

		password, err := l.ensureAdminPassword()
		if err != nil {
			t.Fatalf("an existing credential failed the start: %v", err)
		}
		if password != "" {
			t.Fatalf("re-disclosed a stored credential as %q", password)
		}
		onDisk, err := os.ReadFile(l.adminPasswordPath())
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(onDisk) != "the-persisted-password" {
			t.Fatalf("the stored credential was overwritten: %q", onDisk)
		}
	})

	// A dangling symlink reaches the same ErrExist branch — O_EXCL refuses a
	// symlink whether or not its target exists — but with nothing behind it.
	// Treating that as "another launcher won" would start an installation with no
	// readable password, immediately after printing where to read it.
	t.Run("a path that is not a regular file fails loudly", func(t *testing.T) {
		l := newTestLayout(t)
		if err := os.Symlink(filepath.Join(l.data(), "nowhere"), l.adminPasswordPath()); err != nil {
			t.Fatalf("plant a dangling symlink: %v", err)
		}

		password, err := l.ensureAdminPassword()
		if err == nil {
			t.Fatalf("a dangling symlink was accepted as an existing credential (returned %q), so the\n"+
				"installation starts with no password and no complaint", password)
		}
		if password != "" {
			t.Fatalf("announced %q alongside an error", password)
		}
	})
}
