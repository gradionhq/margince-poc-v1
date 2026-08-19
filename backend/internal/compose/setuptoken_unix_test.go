// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// POSIX only, and by build tag rather than by a runtime skip: creating a symlink
// on Windows needs SeCreateSymbolicLinkPrivilege or Developer Mode, so a shared
// test would skip on the runners more often than it ran — and a security check
// that skips reads exactly like one that passes. The guarantee being pinned is
// POSIX's own, and Windows genuinely differs here (issue #1579).
//go:build !windows

package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteSetupTokenFileRefusesASymlinkAtTheFinalComponent turns a cited
// standard into a checked one.
//
// setuptokenflags_unix.go argues that O_NOFOLLOW adds nothing because POSIX
// requires open() with O_CREAT|O_EXCL to fail EEXIST when the final component is
// a symbolic link, whatever it points at. Every reason not to reach for a
// stronger primitive here rests on that sentence, and until now it was a
// citation. If it were wrong — or a platform did not conform — the writer would
// follow an attacker's link and place the credential that claims the
// installation wherever it aimed.
//
// Both directions are covered: a link to an existing file, and a DANGLING one,
// which is the case that actually differs on Windows and the one a naive
// implementation lets through.
func TestWriteSetupTokenFileRefusesASymlinkAtTheFinalComponent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dangles bool
	}{
		{name: "a link to a file the attacker already owns", dangles: false},
		{name: "a dangling link, where a followed create would land at the target", dangles: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			if err := os.MkdirAll(filepath.Dir(setupTokenFile), 0o700); err != nil {
				t.Fatalf("prepare the config directory: %v", err)
			}

			// Outside the installation, because relocation out of the tree is the
			// point of the attack.
			target := filepath.Join(t.TempDir(), "captured")
			const decoy = "the attacker's own content"
			if !tc.dangles {
				if err := os.WriteFile(target, []byte(decoy), 0o600); err != nil {
					t.Fatalf("prepare the link target: %v", err)
				}
			}
			if err := os.Symlink(target, setupTokenFile); err != nil {
				t.Fatalf("plant the symlink: %v", err)
			}

			if _, err := writeSetupTokenFile("the-real-token"); err == nil {
				t.Fatal("the writer followed a symlink at the final component, so the setup token " +
					"can be redirected out of the installation by anyone who gets there first")
			}

			// The refusal has to be total: nothing created, nothing overwritten.
			if tc.dangles {
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatalf("the link target was created despite the refusal (Lstat: %v)", err)
				}
				return
			}
			after, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read back the link target: %v", err)
			}
			if string(after) != decoy {
				t.Fatalf("the link target was written through: got %q, want %q", after, decoy)
			}
		})
	}
}
