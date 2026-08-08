// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// secretsUnitSource is a unit declaring the given Secrets slice elements.
func secretsUnitSource(secretsFields string) string {
	return `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func New() extension.Extension {
	return extension.Extension{
		Name:    "x",
		Version: "0.1.0",
		Secrets: []extension.SecretsRequest{
` + secretsFields + `
		},
	}
}
`
}

// TestSecretsDeriveIntoManifest: a declared secret is inert data an
// operator resolves (see extension.SecretsRequest) — the reader derives
// its key and scope, sorted by key so the encoding does not depend on
// declaration order.
func TestSecretsDeriveIntoManifest(t *testing.T) {
	src := secretsUnitSource(
		"\t\t\t{Key: \"signing\", Scope: extension.SecretScopeWorkspace},\n" +
			"\t\t\t{Key: \"api_token\", Scope: extension.SecretScopeUser},\n")
	derived, err := deriveSynthetic(t, "x", src)
	if err != nil {
		t.Fatal(err)
	}
	s := string(derived)
	for _, want := range []string{
		`"key": "api_token"`, `"scope": "user"`,
		`"key": "signing"`, `"scope": "workspace"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("derived manifest misses %s:\n%s", want, s)
		}
	}
	if strings.Index(s, "api_token") > strings.Index(s, "signing") {
		t.Fatalf("secrets are not sorted by key:\n%s", s)
	}
}

// TestNoSecretsOmitsTheField: nothing declares Secrets yet, and every
// manifest already committed to the tree predates the field — an
// unconditional "secrets" key would rewrite every one of them for a field
// they do not use, so an undeclared Secrets list must not appear at all.
func TestNoSecretsOmitsTheField(t *testing.T) {
	derived, err := deriveSynthetic(t, "x", toolUnitSource(
		"\t\t\tName: \"t\", Version: \"1.0.0\", Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(derived), "secret") {
		t.Fatalf("the secrets field must be omitted when nothing is declared:\n%s", derived)
	}
}

// TestDuplicateSecretIsRejected: the same key in the same scope declared
// twice describes one secret two different ways, which the manifest cannot
// represent — the same shape of refusal Tools already applies to a
// duplicate governed operation id.
func TestDuplicateSecretIsRejected(t *testing.T) {
	src := secretsUnitSource(
		"\t\t\t{Key: \"signing\", Scope: extension.SecretScopeWorkspace},\n" +
			"\t\t\t{Key: \"signing\", Scope: extension.SecretScopeWorkspace},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("err = %v, want the duplicate-secret refusal", err)
	}
}

// TestSecretWithInvalidScopeIsRejected: a scope outside the published
// {workspace, user} vocabulary is a SEMANTIC error caught through the same
// Validate the boot preflight runs — gen-time acceptance cannot diverge
// from boot-time.
func TestSecretWithInvalidScopeIsRejected(t *testing.T) {
	src := secretsUnitSource("\t\t\t{Key: \"signing\", Scope: \"admin\"},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "the scopes are") {
		t.Fatalf("err = %v, want the invalid-scope refusal", err)
	}
}

// TestSecretWithEmptyKeyIsRejected pins the same Validate path for an
// empty key name.
func TestSecretWithEmptyKeyIsRejected(t *testing.T) {
	src := secretsUnitSource("\t\t\t{Key: \"\", Scope: extension.SecretScopeWorkspace},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "empty key name") {
		t.Fatalf("err = %v, want the empty-key refusal", err)
	}
}

// TestSecretsFieldMustBeASliceLiteral: a computed Secrets value cannot be
// derived without evaluating it, which a static reader must never do.
func TestSecretsFieldMustBeASliceLiteral(t *testing.T) {
	src := `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func secrets() []extension.SecretsRequest { return nil }

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0", Secrets: secrets()}
}
`
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "Secrets must be a slice literal") {
		t.Fatalf("err = %v, want the non-literal-Secrets refusal", err)
	}
}

// TestSecretsEntryUnrecognizedFieldFailsClosed mirrors Tool's fail-closed
// default: a field this generator does not recognize could be a future
// governed detail, and silently omitting it would hide a request from the
// operator.
func TestSecretsEntryUnrecognizedFieldFailsClosed(t *testing.T) {
	src := secretsUnitSource("\t\t\t{Key: \"signing\", Scope: extension.SecretScopeWorkspace, Future: nil},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "is not derivable by this generator") {
		t.Fatalf("err = %v, want the unrecognized-field refusal", err)
	}
}
