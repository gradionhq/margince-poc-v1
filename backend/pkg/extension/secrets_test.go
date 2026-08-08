// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"strings"
	"testing"
)

func TestSecretsRequestValidate(t *testing.T) {
	for _, valid := range []SecretsRequest{
		{Key: "signing", Scope: SecretScopeWorkspace},
		{Key: "provider token", Scope: SecretScopeUser},
		{Key: strings.Repeat("k", maxSecretKeyLength), Scope: SecretScopeWorkspace},
	} {
		if err := valid.Validate(); err != nil {
			t.Errorf("SecretsRequest%+v.Validate() = %v, want nil", valid, err)
		}
	}

	for name, invalid := range map[string]SecretsRequest{
		"empty key":       {Key: "", Scope: SecretScopeWorkspace},
		"blank key":       {Key: "   ", Scope: SecretScopeWorkspace},
		"overlong key":    {Key: strings.Repeat("k", maxSecretKeyLength+1), Scope: SecretScopeUser},
		"control in key":  {Key: "sign\ning", Scope: SecretScopeWorkspace},
		"unstated scope":  {Key: "signing"},
		"invented scope":  {Key: "signing", Scope: SecretScope("team")},
		"scope as a key":  {Key: "signing", Scope: SecretScope("Workspace")},
		"nothing at all":  {},
		"key with a null": {Key: "sign\x00ing", Scope: SecretScopeUser},
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("%s: SecretsRequest%+v.Validate() = nil, want a refusal", name, invalid)
		}
	}
}

// TestSecretsRequestZeroScopeIsNotWorkspace pins the one default that would
// be tempting and wrong: "I did not think about it" must not read to an
// operator as "I want the installation-wide credential".
func TestSecretsRequestZeroScopeIsNotWorkspace(t *testing.T) {
	var zero SecretScope
	if zero == SecretScopeWorkspace {
		t.Fatal("the zero SecretScope equals SecretScopeWorkspace — an undeclared scope would silently mean the installation credential")
	}
}
