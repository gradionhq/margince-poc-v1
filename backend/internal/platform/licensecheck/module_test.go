// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package licensecheck

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

//go:embed module/licensecheck.wasm.module.sha256
var recordedDigest string

// The bundled module is a binary nobody reviews by reading it, fetched from a
// release this repository cannot rebuild. What CAN be reviewed is the digest
// beside it, so the two are held together here: a blob that was swapped,
// truncated, or refreshed without its pin fails the build gate rather than
// booting and quietly trusting a different keyset than the one that was
// reviewed.
func TestBundledModuleMatchesItsRecordedDigest(t *testing.T) {
	t.Parallel()
	want, name, ok := strings.Cut(strings.TrimSpace(recordedDigest), " ")
	if !ok {
		t.Fatalf("module/licensecheck.wasm.module.sha256 is not in `shasum -a 256` format: %q", recordedDigest)
	}
	// The digest names the UPSTREAM asset, which is how the tree records which
	// artifact was fetched once the bundled file name stopped saying so.
	if !strings.HasPrefix(strings.TrimSpace(name), "licensecheck.wasm.") {
		t.Errorf("the digest file names %q, not a published licensecheck module", strings.TrimSpace(name))
	}
	sum := sha256.Sum256(bundledModule)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("the bundled module hashes to %s but module/licensecheck.wasm.module.sha256 records %s —\n"+
			"a refresh installs both together; neither is edited by hand", got, want)
	}
}

// What the blob has to BE, whatever framing upstream published it in: something
// the host can unwrap into a WebAssembly module. Asserted through the real
// maybeDecompress rather than by sniffing a magic number here, so this keeps
// holding across a compression change instead of pinning the tree to gzip.
func TestBundledModuleUnwrapsToAWebAssemblyModule(t *testing.T) {
	t.Parallel()
	raw, err := maybeDecompress(bundledModule)
	if err != nil {
		t.Fatalf("unwrap the bundled module: %v", err)
	}
	// The wasm preamble: \0asm plus the version word.
	if len(raw) < 8 || !bytes.HasPrefix(raw, wasmMagic) {
		t.Fatal("the unwrapped module does not begin with the WebAssembly preamble")
	}
	// A compressed artifact is the point of bundling one: the raw module is
	// roughly five times the size, and it is embedded in every binary.
	if len(bundledModule) >= len(raw) {
		t.Errorf("the bundled module is %d bytes and unwraps to %d — it is not the compressed artifact",
			len(bundledModule), len(raw))
	}
}

// The recorded release tag reaches every posture the process reports, so it has
// to be the upstream tag and not a hand-written note.
func TestModuleVersionIsAnUpstreamReleaseTag(t *testing.T) {
	t.Parallel()
	got := ModuleVersion()
	if !regexp.MustCompile(`^sha-[0-9a-f]{12,}$`).MatchString(got) {
		t.Errorf("ModuleVersion() = %q, want an upstream `sha-<commit>` release tag: the rolling `latest` tag "+
			"names a different build every day and could not identify the bundled one", got)
	}
}
