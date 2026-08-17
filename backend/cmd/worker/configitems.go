// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// What this role can be configured with, as data — the worker's half of the
// same declaration the api makes. See cmd/api/configitems.go for why the flag
// surface is read back rather than restated.

import (
	"flag"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/cliflags"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/websearchhttp"
)

// workerPublic names the flag-bound variables whose values are safe to echo;
// everything else this role reads is treated as a secret. See
// cmd/api/configitems.go for the reasoning, which is the same here.
var workerPublic = map[string]bool{
	"MARGINCE_CONFIG":             true,
	"MARGINCE_AI_ROUTING":         true,
	"MARGINCE_AITASK_DIR":         true,
	"MARGINCE_LOG_LEVEL":          true,
	"MARGINCE_LOG_FORMAT":         true,
	"MARGINCE_REDIS":              true,
	"MARGINCE_OBSERVE_ADDR":       true,
	"MARGINCE_PUBLIC_BASE_URL":    true,
	"MARGINCE_GMAIL_CLIENT_ID":    true,
	"MARGINCE_GMAIL_PUBSUB_TOPIC": true,
	"MARGINCE_GRAPH_CLIENT_ID":    true,
	"MARGINCE_GRAPH_TENANT":       true,
}

// workerConfigItems is this role's whole configurable surface.
func workerConfigItems(fs *flag.FlagSet, env *cliflags.Env) ([]config.Item, error) {
	registry, err := config.NewRegistry(
		env.Items(fs, config.RoleWorker, workerPublic),
		blobstore.ConfigItems(),
		keyvault.ConfigItems(),
		websearchhttp.ConfigItems(),
		ai.ConfigItems(),
		deployconfig.ConfigItems(),
		workerUnflaggedItems(),
	)
	if err != nil {
		return nil, err
	}
	return registry.Items(), nil
}

// workerUnflaggedItems are the variables this role reads with no flag behind
// them, owned by the composition root rather than by any one package.
func workerUnflaggedItems() []config.Item {
	both := []string{config.RoleAPI, config.RoleWorker}
	worker := []string{config.RoleWorker}
	return []config.Item{
		{
			Name: overlayBackfillLimitEnv, Kind: config.KindInt, Default: "0", Roles: both,
			Doc: "per-object-class cap on the overlay initial backfill; 0 runs it uncapped",
		},
		{
			Name: compose.ProviderModeEnv, Kind: config.KindString, Default: "live", Roles: both,
			Doc: "enrichment provider: live|offline|off; an unknown value is a boot error rather than a silently disabled feature",
		},
		{
			Name: deepReadMaxPagesEnv, Kind: config.KindInt, Default: "0", Roles: worker,
			Doc: "cap on pages one deep read fetches; 0 takes the compiled default",
		},
		{
			Name: deepReadMaxBytesEnv, Kind: config.KindInt, Default: "0", Roles: worker,
			Doc: "cap on bytes one deep read fetches; 0 takes the compiled default",
		},
		{
			Name: deepReadWallEnv, Kind: config.KindDuration, Default: "0", Roles: worker,
			Doc: "wall-clock ceiling on one deep read; 0 takes the compiled default",
		},
	}
}
