// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package licensecheck

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

//go:embed module/licensecheck.wasm.gz.sha256
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
		t.Fatalf("module/licensecheck.wasm.gz.sha256 is not in `shasum -a 256` format: %q", recordedDigest)
	}
	if !strings.Contains(name, "licensecheck.wasm.gz") {
		t.Errorf("the digest file names %q, not the bundled module", strings.TrimSpace(name))
	}
	sum := sha256.Sum256(moduleGz)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("the bundled module hashes to %s but module/licensecheck.wasm.gz.sha256 records %s —\n"+
			"if the module was refreshed on purpose, `make license-module` rewrites both together", got, want)
	}
}

// The module is embedded compressed, as published. The host gunzips it, so a
// blob that lost its gzip header would still be handed to wazero — as wasm it
// is not — and the failure would surface at boot as an unrunnable module rather
// than here.
func TestBundledModuleIsGzipped(t *testing.T) {
	t.Parallel()
	if len(moduleGz) < 2 || moduleGz[0] != 0x1f || moduleGz[1] != 0x8b {
		t.Fatal("module/licensecheck.wasm.gz does not carry the gzip magic bytes")
	}
	raw, err := maybeDecompress(moduleGz)
	if err != nil {
		t.Fatalf("gunzip the bundled module: %v", err)
	}
	// The wasm preamble: \0asm plus the version word.
	if len(raw) < 8 || string(raw[:4]) != "\x00asm" {
		t.Fatal("the decompressed module does not begin with the WebAssembly preamble")
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
