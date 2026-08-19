// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The setup token is the credential that claims an installation, so the
// property that matters is not "the file gets written" but "an entry that is
// already there is refused, and its contents are left alone". That was asserted
// in a comment on three platforms and checked on none; these run everywhere the
// unit lane runs, Windows included.
//
// They do NOT exercise openNoFollow, and nothing here should be read as saying
// they do: a planted regular file is refused by O_EXCL alone, and on POSIX
// O_NOFOLLOW guards no case of its own at all (setuptokenflags_unix.go explains
// why). The symlink behaviour O_EXCL actually IS relied on for is pinned by the
// last test in this file.

func TestWriteSetupTokenFileRefusesAnExistingEntryAndLeavesItIntact(t *testing.T) {
	t.Chdir(t.TempDir())
	// The real writer resolves setupTokenFile against the working directory, so
	// creating the parent by hand is what the pre-boot attacker does: get there
	// first.
	if err := os.MkdirAll(filepath.Dir(setupTokenFile), 0o700); err != nil {
		t.Fatalf("prepare the config directory: %v", err)
	}
	const planted = "a value the server must not overwrite"
	if err := os.WriteFile(setupTokenFile, []byte(planted), 0o600); err != nil {
		t.Fatalf("plant the existing file: %v", err)
	}

	path, err := writeSetupTokenFile("the-real-token")
	if err == nil {
		t.Fatal("writing over an existing setup-token file succeeded; O_EXCL is what makes a pre-created path fail, and a caller that overwrites would hand out a token the operator never sees")
	}
	if !strings.Contains(path, "margince-setup-token") {
		t.Errorf("the returned path should name the file it refused, got %q", path)
	}

	// The refusal is only worth having if the plant is untouched: a truncated
	// file would mean the open got far enough to clobber it.
	after, readErr := os.ReadFile(setupTokenFile)
	if readErr != nil {
		t.Fatalf("read back the planted file: %v", readErr)
	}
	if string(after) != planted {
		t.Errorf("the refused write still altered the file: got %q, want %q", after, planted)
	}
}

func TestWriteSetupTokenFileWritesTheTokenWhenThePathIsFree(t *testing.T) {
	t.Chdir(t.TempDir())

	const token = "one-time-claim-credential"
	path, err := writeSetupTokenFile(token)
	if err != nil {
		t.Fatalf("writeSetupTokenFile on a clean tree: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("the returned path must be absolute so the log names something an operator can open, got %q", path)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the token file: %v", err)
	}
	if string(written) != token {
		t.Errorf("token file holds %q, want %q", written, token)
	}

	// A second call is the restart case, and it must refuse rather than mint
	// over the credential the operator was already given.
	if _, err := writeSetupTokenFile("a different token"); err == nil {
		t.Error("a second write to the same path succeeded; the first token is the one in the operator's hands")
	}
}

// TestWriteSetupTokenFileRefusesASymlinkAtTheFinalComponent turns a cited
// standard into a checked one.
//
// setuptokenflags_unix.go argues that O_NOFOLLOW adds nothing because POSIX
// requires open() with O_CREAT|O_EXCL to fail EEXIST when the final component is
// a symbolic link, whatever it points at. Every reason not to reach for a
// stronger primitive rests on that sentence, and until now it was a citation. If
// it were wrong, the writer would follow an attacker's link and place the
// credential that claims the installation wherever it aimed.
//
// Both directions are covered: a link to an existing file, and a DANGLING one,
// which is the case a naive implementation lets through.
func TestWriteSetupTokenFileRefusesASymlinkAtTheFinalComponent(t *testing.T) {
	// Windows, and only Windows, is skipped — for the fixture, not the property.
	// Creating a symlink there needs SeCreateSymbolicLinkPrivilege or Developer
	// Mode, so the plant cannot be built on an ordinary runner. It is also the
	// one platform where the dangling case genuinely behaves differently, which
	// setuptokenflags_windows.go names and issue #1579 tracks. Every lane that
	// runs this suite runs on Linux, so this never skips in CI.
	if runtime.GOOS == "windows" {
		t.Skip("cannot plant a symlink without SeCreateSymbolicLinkPrivilege; the Windows gap is issue #1579")
	}

	for _, tc := range []struct {
		name    string
		dangles bool
	}{
		{name: "a link to a file the attacker already owns", dangles: false},
		{name: "a dangling link, where a followed create would land at the target", dangles: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
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
