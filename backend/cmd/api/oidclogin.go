// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
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
	// The password switch is wired whatever the OIDC posture is: an
	// installation that turned it off must have that refused at the surface,
	// not merely validated at boot.
	opts := []compose.Option{compose.WithPasswordLogin(deployCfg.Auth.PasswordEnabled())}
	if !deployCfg.Auth.PasswordEnabled() {
		_, _ = fmt.Fprintln(stdout, "api password sign-in DISABLED by auth.password.enabled=false — /auth/login and password recovery refuse; humans sign in through the configured provider")
	}
	if !oidcCfg.Enabled {
		return opts, nil
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
	if err := sameCookieHost(callbackBase, publicBaseURL); err != nil {
		return nil, err
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
	return append(opts, opt), nil
}

// sameCookieHost refuses a deployment whose federated sign-in could not
// finish. The flow's browser binding is a host-scoped cookie: the SPA starts
// it with a RELATIVE navigation, so the cookie lands on the app's host, while
// the provider returns to the callback on the api's host. Cookies ignore
// ports and schemes but not hosts, so when those two hosts differ the
// callback arrives without the handle and EVERY sign-in dies as `expired`.
//
// It is a boot error rather than a runtime surprise for the reason A107 §14
// gives: authentication configuration fails closed. It also cannot be caught
// in `make dev`, where both bases are `localhost` and only the ports differ —
// exactly the shape that would ship green and fail in production.
func sameCookieHost(callbackBase, publicBaseURL string) error {
	callback, err := url.Parse(callbackBase)
	if err != nil {
		return fmt.Errorf("api: --api-base-url %q is not a URL: %w", callbackBase, err)
	}
	app, err := url.Parse(strings.TrimRight(publicBaseURL, "/"))
	if err != nil {
		return fmt.Errorf("api: --public-base-url %q is not a URL: %w", publicBaseURL, err)
	}
	// A host is checked for BEFORE the two are compared, because `url.Parse`
	// reads a scheme-less `crm.example.test:8080` as a path with an opaque
	// scheme and reports no host at all. Two such values compare equal on ""
	// and would walk straight through this gate into a redirect_uri no provider
	// can resolve.
	for _, base := range []struct {
		flag string
		url  *url.URL
	}{{"--public-base-url", app}, {"--api-base-url", callback}} {
		if base.url.Hostname() == "" {
			return fmt.Errorf(
				"api: auth.oidc needs an absolute %s — %q names no host, so no redirect_uri can be derived from it. "+
					"Give it a scheme and host, as in https://crm.example.test",
				base.flag, base.url.String())
		}
	}
	if callback.Hostname() == app.Hostname() {
		return nil
	}
	return fmt.Errorf(
		"api: auth.oidc cannot work across hosts — the app is on %q and the OIDC callback on %q, "+
			"and the flow's browser handle cookie is host-scoped, so every sign-in would fail as expired. "+
			"Serve /v1 from the app's host (a path proxy, as `make dev` does) or point --api-base-url at that host",
		app.Hostname(), callback.Hostname())
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
