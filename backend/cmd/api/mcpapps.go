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
	"fmt"
	"net/url"
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
