// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// oidcLoginOptions wires federated sign-in when the deployment file
// configures a provider (A107/ADR-0061 §6). It refuses to boot on a
// configuration that could not complete a sign-in: authentication
// configuration fails closed (§14), and a login button that cannot finish
// is worse than no button — the operator asked for one and would never see
// it fail until a human did.
func oidcLoginOptions(deployCfg deployconfig.Config, publicBaseURL, apiBaseURL string, stdout io.Writer) ([]compose.Option, error) {
	oidcCfg := deployCfg.Auth.OIDC
	if !oidcCfg.Enabled {
		return nil, nil
	}
	if publicBaseURL == "" {
		return nil, errors.New("api: auth.oidc.enabled requires --public-base-url/MARGINCE_PUBLIC_BASE_URL (the provider's registered redirect target is derived from it)")
	}
	secret, err := oidcCfg.ClientSecret()
	if err != nil {
		return nil, err
	}

	providerKey := oidcCfg.ProviderKey()
	// The api's own externally-reachable base, which is where the provider
	// redirects — the SPA's base only differs from it in a split-origin
	// deployment, and that is exactly what --api-base-url exists for.
	callbackBase := strings.TrimRight(apiBaseURL, "/")
	if callbackBase == "" {
		callbackBase = strings.TrimRight(publicBaseURL, "/")
	}
	opt, err := compose.WithOIDCLogin(identity.OIDCLoginConfig{
		ProviderKey:    providerKey,
		Label:          providerLabel(oidcCfg),
		Issuer:         oidcCfg.Issuer,
		ClientID:       oidcCfg.ClientID,
		ClientSecret:   secret,
		RedirectURI:    callbackBase + "/v1/auth/oidc/" + providerKey + "/callback",
		AppBaseURL:     strings.TrimRight(publicBaseURL, "/"),
		AllowedDomains: oidcCfg.AllowedDomains,
	})
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "api federated sign-in enabled (%s; redirect_uri %s/v1/auth/oidc/%s/callback)\n", providerKey, callbackBase, providerKey)
	if len(oidcCfg.AllowedDomains) > 0 {
		_, _ = fmt.Fprintf(stdout, "api federated sign-in restricted to domains: %s\n", strings.Join(oidcCfg.AllowedDomains, ", "))
	}
	return []compose.Option{opt}, nil
}

// providerLabel is the button copy the login screen renders verbatim. The
// operator may override it; otherwise the provider's own conventional
// wording applies.
func providerLabel(cfg deployconfig.OIDC) string {
	if cfg.Label != "" {
		return cfg.Label
	}
	return "Continue with Google"
}
