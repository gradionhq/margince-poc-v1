// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Where this api reads its MCP App view documents from.
//
// RESOLUTION IS ITS OWN CONTRACT, spelled here rather than assumed by analogy.
// --api-base-url's documented fallback is implemented at USE time in
// compose/connectors.go, not by flag parsing, so copying "it falls back" from
// there would have been copying a sentence rather than a mechanism.
//
// The fallback is sound exactly where it matters. --public-base-url is optional
// in general, but the MCP connector gate is a boot error without one — and MCP
// Apps exist only where /mcp is served. So wherever a view could be wanted, the
// chain cannot be empty.
//
// | connector | URLs        | behaviour                                    |
// |-----------|-------------|----------------------------------------------|
// | enabled   | both empty  | boot error (already true today, in boot.go)  |
// | enabled   | either set  | fetch                                        |
// | disabled  | anything    | NO fetch at all — nothing composes a view    |

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents/apps"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// mcpAppsOrigin answers the origin to read view documents from, or nil when this
// deployment serves no views at all.
//
// A nil origin with a nil error is the connector-disabled shape and is not a
// failure: compose returns before it composes any resource provider, so there is
// nothing to fetch for. Answering an origin anyway would build a fetcher that
// polls a web tier this installation never asked to depend on.
//
//nolint:nilnil // a deployment that serves no views has no origin AND nothing to report: the absence is the answer, and the caller composes no fetcher for it
func mcpAppsOrigin(cfg apiConfig, connectorEnabled bool) (*url.URL, error) {
	if !connectorEnabled {
		return nil, nil
	}
	raw := cfg.mcpAppsBaseURL
	flagName := "--mcp-apps-base-url"
	if raw == "" {
		// boot.go has already refused an empty --public-base-url under this
		// gate, so this is the configured value rather than a hopeful one.
		raw, flagName = cfg.publicBaseURL, "--public-base-url"
	}
	if raw == "" {
		return nil, nil
	}
	if err := validateBareOrigin(flagName, raw); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// Unreachable after validateBareOrigin parsed the same string, and
		// handled rather than discarded so it cannot become one silently.
		return nil, fmt.Errorf("api: %s %q is not a URL: %w", flagName, raw, err)
	}
	return parsed, nil
}

// mcpAppViewsLane builds the view provider, makes the one bounded startup fetch
// and starts the refresh loop, answering the compose options and the stop func.
//
// It is a LANE in this file rather than an Option because a bounded fetch needs
// a context and a background loop needs cancelling, and an Option carries
// neither — the same reason inlineRelayLane is built here. It also puts the
// lifecycle where the process role is decided: the worker never calls this, and
// two processes refreshing one snapshot would be two answers to a question that
// has one.
//
// Priming happens BEFORE compose.New returns a handler anyone can reach, so the
// first client to list is told about the views this process is actually holding
// rather than a set still being assembled.
//
// A view that did not answer is NOT an error here: the api starts, serves what
// it has, and says in the log which views it is without. Only a condition an
// operator must fix — an origin configured but unusable — comes back as one.
func mcpAppViewsLane(ctx context.Context, cfg apiConfig, deployCfg deployconfig.Config, logger *slog.Logger) ([]compose.Option, func(), error) {
	noop := func() {}
	origin, err := mcpAppsOrigin(cfg, deployCfg.MCP.ConnectorEnabled)
	if err != nil {
		return nil, noop, err
	}
	if origin == nil {
		// The connector-disabled shape: no provider, no `ui://` document, no
		// `_meta.ui`, and every tool still answering in text.
		return nil, noop, nil
	}
	views := apps.NewProvider(apps.NewFetcher(origin), logger)
	if err := views.Prime(ctx); err != nil {
		return nil, noop, err
	}
	// WithoutCancel: the refresh loop outlives the boot context and is ended by
	// the returned stop, which the caller defers.
	refreshCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	go views.RunRefresh(refreshCtx)
	return []compose.Option{compose.WithMCPAppViews(views)}, stop, nil
}
