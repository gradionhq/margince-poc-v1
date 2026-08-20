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
	// Mode, so the plant cannot be built on an ordinary runner. Every lane that
	// runs this suite runs on Linux, so this never skips in CI.
	//
	// The skip names what goes untested rather than an issue number, because a
	// number tells a reader where to ask and not what is missing — and a skipped
	// security test looks exactly like a passing one.
	if runtime.GOOS == "windows" {
		t.Skip("cannot plant a symlink without SeCreateSymbolicLinkPrivilege: what goes unexercised here is the refusal of a symlinked FINAL component, including the dangling case that setuptokenflags_windows.go documents as behaving differently on NT")
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

// The parent case, which O_EXCL and O_NOFOLLOW both miss: they act on the FINAL
// component, so a symlink standing in for config/ redirects the whole write and
// MkdirAll walks straight through it reporting success.
//
// This is the attack #1579 verified empirically — the credential lands inside a
// directory the attacker owns, and the write reports no error at all, so nothing
// downstream can tell.
func TestWriteSetupTokenFileRefusesASymlinkedParentDirectory(t *testing.T) {
	// Skipped on Windows for the FIXTURE, not the property: planting a symlink
	// needs SeCreateSymbolicLinkPrivilege or Developer Mode. What goes untested
	// there is the parent-directory refusal itself — ownTokenDirectory's Lstat is
	// platform-independent, but no lane in this repository executes it on Windows.
	if runtime.GOOS == "windows" {
		t.Skip("cannot plant a symlink without SeCreateSymbolicLinkPrivilege; the parent-directory refusal is therefore unexercised on Windows")
	}

	work := t.TempDir()
	// Outside the installation, because relocating the credential out of the tree
	// is the point.
	attacker := filepath.Join(t.TempDir(), "attacker-owned")
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatalf("prepare the attacker directory: %v", err)
	}
	// Get there first: config/ is a link before the server ever boots.
	if err := os.Symlink(attacker, filepath.Join(work, filepath.Dir(setupTokenFile))); err != nil {
		t.Fatalf("plant the parent symlink: %v", err)
	}
	t.Chdir(work)

	if _, err := writeSetupTokenFile("the-real-token"); err == nil {
		t.Fatal("the writer followed a symlinked parent directory, so the credential that claims " +
			"this installation was created inside a directory somebody else owns — and the write reported success")
	}

	// Total refusal: the redirect must leave nothing behind at the target either.
	if _, err := os.Lstat(filepath.Join(attacker, filepath.Base(setupTokenFile))); !os.IsNotExist(err) {
		t.Fatalf("the token was written into the attacker's directory despite the refusal (Lstat: %v)", err)
	}
}

// A config/ that legitimately pre-exists is the normal case, not the attack:
// every real checkout has one holding margince.yaml and the admin password, and
// a guard that refused it would break every developer and every operator.
func TestWriteSetupTokenFileAcceptsAnOrdinaryPreExistingConfigDirectory(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, filepath.Dir(setupTokenFile))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("prepare the config directory: %v", err)
	}
	// The neighbours a real config/ carries, so this is the directory an
	// installation actually has rather than an empty one.
	if err := os.WriteFile(filepath.Join(dir, "margince.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatalf("prepare margince.yaml: %v", err)
	}
	t.Chdir(work)

	path, err := writeSetupTokenFile("the-real-token")
	if err != nil {
		t.Fatalf("a pre-existing config directory was refused: %v — every real installation has one", err)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- a path this test just built under t.TempDir()
	if err != nil {
		t.Fatalf("read back the token: %v", err)
	}
	if string(body) != "the-real-token" {
		t.Errorf("token file holds %q, want the token", body)
	}
	// The neighbour is untouched: owning the directory must not mean rewriting it.
	if _, err := os.Stat(filepath.Join(dir, "margince.yaml")); err != nil {
		t.Errorf("the pre-existing margince.yaml did not survive: %v", err)
	}
}

// The refusals the happy path never reaches. They are short branches, but they
// are the branches that decide whether a credential is written into something
// unexpected, so "obviously correct" is not a reason to leave them unexercised.
func TestWriteSetupTokenFileRefusesADirectoryPathItCannotOwn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		plant      func(t *testing.T, work string)
		want       string
		skipAsRoot bool
	}{
		{
			name: "an ordinary file standing where the directory belongs",
			plant: func(t *testing.T, work string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(work, filepath.Dir(setupTokenFile)), []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("plant the file: %v", err)
				}
			},
			want: "exists and is not a directory",
		},
		{
			name: "a working directory the process may not write",
			plant: func(t *testing.T, work string) {
				t.Helper()
				if err := os.Chmod(work, 0o500); err != nil {
					t.Fatalf("make the working directory read-only: %v", err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(work, 0o700); err != nil {
						t.Errorf("restoring the working directory's mode: %v", err)
					}
				})
			},
			want:       "creating the setup-token directory",
			skipAsRoot: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// root writes into a read-only directory, so that plant proves nothing
			// when the suite runs as root — as it does in some containers.
			if tc.skipAsRoot && os.Geteuid() == 0 {
				t.Skip("running as root, which writes into a read-only directory: what goes unexercised here is the refusal when config/ cannot be created")
			}
			work := t.TempDir()
			t.Chdir(work)
			tc.plant(t, work)

			_, err := writeSetupTokenFile("the-real-token")
			if err == nil {
				t.Fatal("the writer accepted a parent it could not own, so the credential went somewhere this test cannot vouch for")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say why: got %q, want it to contain %q", err, tc.want)
			}
		})
	}
}
