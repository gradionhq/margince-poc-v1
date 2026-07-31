// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// The `auth:` section: which methods may sign a human in. Password login is
// the default; federated sign-in over OIDC (A107/ADR-0061 §6) is off until an
// operator configures a provider, and half-configured is a boot error rather
// than a login button that dead-ends a human.

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Auth selects the enabled authentication methods: email+password, which
// defaults to enabled, and federated sign-in over OIDC (ADR-0061 §6),
// which is off until an operator configures a provider.
type Auth struct {
	Password PasswordAuth `yaml:"password"`
	OIDC     OIDC         `yaml:"oidc"`
}

// PasswordAuth is the email+password method's switch.
type PasswordAuth struct {
	Enabled *bool `yaml:"enabled"`
}

// PasswordEnabled defaults to true: an installation without an `auth`
// section authenticates by email + password.
func (a Auth) PasswordEnabled() bool {
	return a.Password.Enabled == nil || *a.Password.Enabled
}

// OIDC configures federated sign-in for ONE provider — the shape A107 §6
// pins, and the whole of what an installation needs today: a single
// organization has a single identity provider. Issuer is the discovery
// origin (`https://accounts.google.com`), not a hand-written endpoint set:
// the endpoints and the JWKS URI are read from the provider's discovery
// document, so a provider that rotates them does not need a config change.
type OIDC struct {
	Enabled          bool   `yaml:"enabled"`
	Issuer           string `yaml:"issuer"`
	ClientID         string `yaml:"client_id"`
	ClientSecretFile string `yaml:"client_secret_file"`
	// AllowedDomains optionally restricts which verified email domains may
	// complete a sign-in. Empty means no domain restriction — which is not
	// an open door: the flow still binds only to an EXISTING local user
	// (§5 — a federated login never provisions an account).
	AllowedDomains []string `yaml:"allowed_domains"`
	// Label is the button copy the capabilities probe serves. Empty takes
	// the provider's default ("Continue with Google").
	Label string `yaml:"label"`
}

// ProviderKey is the stable capability key for the configured issuer — the
// `{provider}` path segment and the `oidc_providers[].key` the login UI
// renders. Derived from the issuer rather than configured, so the key and
// the issuer can never disagree. Empty when the issuer is not a provider
// this build knows how to key.
func (o OIDC) ProviderKey() string {
	if isGoogleIssuer(o.Issuer) {
		return "google"
	}
	return ""
}

// isGoogleIssuer matches the two spellings Google's own discovery document
// and ID tokens use for the same issuer (the `iss` claim on a Google ID
// token may carry the `https://` scheme or omit it).
func isGoogleIssuer(issuer string) bool {
	switch issuer {
	case "https://accounts.google.com", "accounts.google.com":
		return true
	default:
		return false
	}
}

// ClientSecret reads the OIDC client secret from its file reference. A
// confidential client is required: the code exchange happens server-side,
// so a missing secret is a configuration error, not a public-client mode.
func (o OIDC) ClientSecret() (string, error) {
	raw, err := os.ReadFile(o.ClientSecretFile) // #nosec G304 -- the operator's own configured secret reference
	if err != nil {
		return "", fmt.Errorf("deployconfig: reading auth.oidc.client_secret_file: %w", err)
	}
	secret := strings.TrimRight(string(raw), "\r\n")
	if secret == "" {
		return "", errors.New("deployconfig: auth.oidc.client_secret_file is empty")
	}
	return secret, nil
}

// validate rejects an OIDC block that could not complete a sign-in.
// Authentication configuration fails closed (A107 §14): a half-configured
// provider is a boot error, never a button that dead-ends a human.
func (o OIDC) validate() error {
	if o.ProviderKey() == "" {
		return fmt.Errorf("deployconfig: auth.oidc.issuer %q is not a provider this build supports (expected https://accounts.google.com)", o.Issuer)
	}
	if o.ClientID == "" {
		return errors.New("deployconfig: auth.oidc.enabled requires auth.oidc.client_id")
	}
	if o.ClientSecretFile == "" {
		return errors.New("deployconfig: auth.oidc.enabled requires auth.oidc.client_secret_file (secrets are file references, never inline values)")
	}
	for _, domain := range o.AllowedDomains {
		if domain == "" || strings.ContainsAny(domain, "@ ") || !strings.Contains(domain, ".") {
			return fmt.Errorf("deployconfig: auth.oidc.allowed_domains entry %q is not a bare domain (e.g. example.com)", domain)
		}
	}
	return nil
}
