// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// unlicensedEnvironment makes the environment say what a test about the FILE
// reference needs it to say: nothing. Token reads the variable first, so an
// engineer or CI lane that exports a real license would otherwise fail every
// test below for a reason that has nothing to do with the code. Empty rather
// than unset because that is the state Token treats as no license, and it is
// the one a container that declares the variable without filling it produces.
// The environment arrives as a parameter, so no case here mutates process state
// to steer the code it is testing.
func unlicensedEnvironment(t *testing.T) {
	t.Helper()
}

func TestLicenseTokenReadsTheFileReference(t *testing.T) {
	unlicensedEnvironment(t)
	path := filepath.Join(t.TempDir(), "license")
	// Written the way a secret store or an editor leaves it: a trailing newline.
	if err := os.WriteFile(path, []byte("a.token.value\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	got, err := License{TokenFile: path}.Token(config.Static(map[string]string{LicenseTokenEnvVar: ""}))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "a.token.value" {
		t.Errorf("Token() = %q, want the file's contents without its terminator", got)
	}
}

func TestLicenseTokenEnvironmentOverridesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "license")
	if err := os.WriteFile(path, []byte("from.the.file"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	got, err := License{TokenFile: path}.Token(config.Static(map[string]string{LicenseTokenEnvVar: " from.the.environment "}))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "from.the.environment" {
		t.Errorf("Token() = %q, want the environment value trimmed", got)
	}
}

// An empty variable is not a license. A container that exports MARGINCE_LICENSE
// with nothing in it falls through to the file rather than reading as
// unlicensed.
func TestLicenseTokenIgnoresAnEmptyEnvironmentValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "license")
	if err := os.WriteFile(path, []byte("from.the.file"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	got, err := License{TokenFile: path}.Token(config.Static(map[string]string{LicenseTokenEnvVar: "   "}))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "from.the.file" {
		t.Errorf("Token() = %q, want the file reference to still be read", got)
	}
}

func TestLicenseTokenIsEmptyForAnUnlicensedInstallation(t *testing.T) {
	unlicensedEnvironment(t)
	got, err := License{}.Token(config.Static(nil))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "" {
		t.Errorf("Token() = %q, want empty", got)
	}
}

// A path that does not resolve must fail the boot. Read as "unlicensed" it would
// hand the operator a workspace that quietly believes it has no entitlement.
func TestLicenseTokenRefusesAnUnreadableFileRatherThanReadingAsUnlicensed(t *testing.T) {
	unlicensedEnvironment(t)
	_, err := License{TokenFile: filepath.Join(t.TempDir(), "typo")}.Token(config.Static(nil))
	if err == nil {
		t.Fatal("Token accepted a token_file that does not exist")
	}
	if !strings.Contains(err.Error(), "license.token_file") {
		t.Errorf("error = %q, want it to name the setting the operator has to correct", err)
	}
}

func TestLicenseTokenRefusesAnEmptyFile(t *testing.T) {
	unlicensedEnvironment(t)
	path := filepath.Join(t.TempDir(), "license")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	_, err := License{TokenFile: path}.Token(config.Static(nil))
	if err == nil {
		t.Fatal("Token accepted an empty token_file")
	}
	if !strings.Contains(err.Error(), "remove the setting to run unlicensed") {
		t.Errorf("error = %q, want it to name the way out", err)
	}
}

// The section decodes as part of the file, and a typo inside it is a boot error
// like every other unknown key.
func TestLicenseSectionParses(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nlicense:\n  token_file: /etc/margince/license\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.License.TokenFile != "/etc/margince/license" {
		t.Errorf("License.TokenFile = %q", cfg.License.TokenFile)
	}
	if _, err := Parse([]byte("version: 1\nlicense:\n  token: inline-secret\n")); err == nil {
		t.Error("Parse accepted an inline license token; secrets are file references and an unknown key is a boot error")
	}
}

// A path pointed at the wrong file — a log, an image, a whole database dump —
// fails as the mistake it is. Everything downstream copies the token whole: into
// a process environment, into a WebAssembly module's memory, and into whatever
// that module quotes back on refusal.
func TestLicenseTokenRefusesAFileTooLargeToBeALicense(t *testing.T) {
	unlicensedEnvironment(t)
	path := filepath.Join(t.TempDir(), "not-a-license")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), TokenLimit+1), 0o600); err != nil {
		t.Fatalf("write oversized file: %v", err)
	}
	_, err := License{TokenFile: path}.Token(config.Static(nil))
	if err == nil {
		t.Fatal("Token read a file too large to be a license token")
	}
	if !strings.Contains(err.Error(), "check the path points at the token") {
		t.Errorf("error = %q, want it to name the likely mistake", err)
	}
}

// A token exactly at the limit is still read: the bound refuses what cannot be a
// license, not what is merely large.
func TestLicenseTokenAcceptsAFileAtTheLimit(t *testing.T) {
	unlicensedEnvironment(t)
	path := filepath.Join(t.TempDir(), "license")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), TokenLimit), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	got, err := License{TokenFile: path}.Token(config.Static(nil))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if len(got) != TokenLimit {
		t.Errorf("Token() returned %d bytes, want %d", len(got), TokenLimit)
	}
}

// The boot line says which of the two sources a token came from, because the
// environment outranks the file an operator reviews.
func TestTokenOriginNamesTheSourceThatWins(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     string
		license License
		want    string
	}{
		{name: "nothing configured", want: "none"},
		{name: "the file reference", license: License{TokenFile: "/etc/margince/license"}, want: "license.token_file"},
		{name: "the environment, over a file", env: "a.token", license: License{TokenFile: "/etc/margince/license"}, want: LicenseTokenEnvVar},
		{name: "an empty variable does not win", env: "  ", license: License{TokenFile: "/etc/margince/license"}, want: "license.token_file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.license.TokenOrigin(config.Static(map[string]string{LicenseTokenEnvVar: tc.env})); got != tc.want {
				t.Errorf("TokenOrigin() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A named token source that yields nothing is a MISTAKE, not an unlicensed
// installation, and it has to be one for both spellings.
//
// The reference form arrived second, so it was the one at risk of being the
// weaker: a mounted secret that failed to project, or a variable the deploy
// pipeline forgot, would otherwise report an installation with no entitlement.
// In production those two produce the same refusal with the wrong remedy
// attached — the operator is told to point license.token at a token when
// license.token is already pointed at an empty file.
func TestAnEmptyTokenReferenceIsAnErrorNotAnUnlicensedInstallation(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "license")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatalf("writing the empty token file: %v", err)
	}
	for name, tc := range map[string]struct {
		doc  string
		want string
	}{
		"a file reference that holds nothing": {
			doc:  "version: 1\nlicense:\n  token: ${file:" + empty + "}\n",
			want: empty + " holds nothing",
		},
		"a variable reference that is unset": {
			doc:  "version: 1\nlicense:\n  token: ${env:" + absentVar + "}\n",
			want: absentVar + " is unset or empty",
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			token, err := cfg.License.Token(config.Static(nil))
			if err == nil {
				t.Fatalf("an empty license.token resolved to %q with no error — the installation reads as unlicensed", token)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "license.token") {
				t.Errorf("error = %q; it must name the key an operator edits", err)
			}
		})
	}
}
