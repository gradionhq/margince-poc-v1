// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// What this role can be configured with, as data.
//
// The flag surface is not restated here: apiConfigItems reads it back off the
// FlagSet parseAPIFlags already built, so the usage text an operator sees on
// `-h` and the doc a generated artefact carries are one sentence. What this
// file adds is the part a FlagSet cannot hold — which values are credentials,
// and the items this role reads WITHOUT a flag, which each owning package
// declares for itself.

import (
	"flag"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/cliflags"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// apiPublic names the flag-bound variables whose values are safe to echo.
// Everything else this role reads is treated as a secret — see cliflags.Items
// for why the list runs this way round.
//
// The test is "could this string authenticate somebody", not "does it look
// sensitive". A log level, a path, a base URL and an OAuth CLIENT id (public by
// the protocol's own design) authenticate nobody, and are safer visible because
// an operator debugging a boot wants to read them. Everything not named here —
// the two DSNs, both client secrets, the push token, the app secret, the
// connector-state HMAC key, the webhook sealing key and the /metrics bearer —
// authenticates something.
var apiPublic = map[string]bool{
	"MARGINCE_CONFIG":                     true,
	"MARGINCE_AI_ROUTING":                 true,
	"MARGINCE_LOG_LEVEL":                  true,
	"MARGINCE_LOG_FORMAT":                 true,
	"MARGINCE_REDIS":                      true,
	"MARGINCE_PUBLIC_BASE_URL":            true,
	"MARGINCE_API_BASE_URL":               true,
	"MARGINCE_MCP_APPS_BASE_URL":          true,
	"MARGINCE_GMAIL_CLIENT_ID":            true,
	"MARGINCE_GRAPH_CLIENT_ID":            true,
	"MARGINCE_GRAPH_TENANT":               true,
	"MARGINCE_GMAIL_JWKS_URL":             true,
	"MARGINCE_GMAIL_PUSH_AUDIENCE":        true,
	"MARGINCE_GMAIL_PUSH_SERVICE_ACCOUNT": true,
}

// apiConfigItems is this role's whole configurable surface: its own flags, plus
// the packages it wires that read the environment on their own account.
func apiConfigItems(fs *flag.FlagSet, env *cliflags.Env) ([]config.Item, error) {
	registry, err := config.NewRegistry(
		env.Items(fs, config.RoleAPI, apiPublic),
		blobstore.ConfigItems(),
		keyvault.ConfigItems(),
		ai.ConfigItems(),
		deployconfig.ConfigItems(),
		apiUnflaggedItems(),
	)
	if err != nil {
		return nil, err
	}
	return registry.Items(), nil
}

// apiUnflaggedItems are the variables this role reads with no flag behind them.
// They are declared here rather than in the packages that read them because
// their owners are the composition root itself — a posture, a licence override,
// a cap on a backfill — and a package that has no opinion about them should not
// carry their documentation either.
func apiUnflaggedItems() []config.Item {
	both := []string{config.RoleAPI, config.RoleWorker}
	return []config.Item{
		{
			Name: overlayBackfillLimitEnv, Kind: config.KindInt, Default: "0", Roles: both,
			Doc: "per-object-class cap on the overlay initial backfill; 0 runs it uncapped",
		},
		{
			Name: oauthAccessTokenTTLEnv, Kind: config.KindDuration, Default: "0", Roles: []string{config.RoleAPI},
			Doc: "lifetime of a minted OAuth access token; 0 takes the compiled default",
		},
		{
			Name: compose.ProviderModeEnv, Kind: config.KindString, Default: "live", Roles: both,
			Doc: "enrichment provider: live|offline|off; an unknown value is a boot error rather than a silently disabled feature",
		},
	}
}

// oauthAccessTokenTTLEnv is spelled once, here and at its read.
// #nosec G101 -- the NAME of a variable holding a duration, not a credential
const oauthAccessTokenTTLEnv = "MARGINCE_OAUTH_ACCESS_TOKEN_TTL"
