// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLicenseTokenReadsTheFileReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "license")
	// Written the way a secret store or an editor leaves it: a trailing newline.
	if err := os.WriteFile(path, []byte("a.token.value\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	got, err := License{TokenFile: path}.Token()
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
	t.Setenv(LicenseTokenEnvVar, " from.the.environment ")
	got, err := License{TokenFile: path}.Token()
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
	t.Setenv(LicenseTokenEnvVar, "   ")
	got, err := License{TokenFile: path}.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "from.the.file" {
		t.Errorf("Token() = %q, want the file reference to still be read", got)
	}
}

func TestLicenseTokenIsEmptyForAnUnlicensedInstallation(t *testing.T) {
	got, err := License{}.Token()
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
	_, err := License{TokenFile: filepath.Join(t.TempDir(), "typo")}.Token()
	if err == nil {
		t.Fatal("Token accepted a token_file that does not exist")
	}
	if !strings.Contains(err.Error(), "license.token_file") {
		t.Errorf("error = %q, want it to name the setting the operator has to correct", err)
	}
}

func TestLicenseTokenRefusesAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "license")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	_, err := License{TokenFile: path}.Token()
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
