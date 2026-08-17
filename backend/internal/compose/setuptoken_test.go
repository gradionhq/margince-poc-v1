// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The setup token is the credential that claims an installation, so the
// property that matters is not "the file gets written" but "an entry that is
// already there is refused, and its contents are left alone". That was asserted
// in a comment on three platforms and checked on none; these run everywhere the
// unit lane runs, Windows included, which is the only way the OS-split
// openNoFollow constant is exercised rather than described.

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
