// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/cliflags"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// apiConfig is the parsed boot configuration of the api process.
type apiConfig struct {
	dsn                 string
	configPath          string
	schemaDSN           string
	addr                string
	redisAddr           string
	inlineRelay         bool
	routingPath         string
	fakeBrain           bool
	logLevel            string
	logFormat           string
	publicBaseURL       string
	mcpAppsBaseURL      string
	apiBaseURL          string
	gmailClientID       string
	gmailClientSecret   string
	gmailPushToken      string
	gmailPushAudience   string
	gmailPushSA         string
	gmailJWKSURL        string
	graphClientID       string
	graphClientSecret   string
	graphTenant         string
	hubspotAppSecret    string
	connectorStateKey   string
	webhookKey          string
	metricsToken        string
	oauthAccessTokenTTL time.Duration
	// posture is what MARGINCE_ENV says this deployment is, read ONCE here
	// (OPS-CFG-2) rather than at each of the three places that used to ask.
	// It selects the configuration overlay and which license authorities are
	// honoured; it decides nothing destructive — see shared/runtimeenv.
	posture runtimeenv.Environment
	// unknownVars are the MARGINCE_* variables found in the environment that
	// this role does not read. Computed during parsing, where the surface is
	// assembled, and REPORTED once the logger exists — an operator has to see
	// it, and stderr before the log handler is built is a different stream in a
	// different format from everything else they are reading.
	unknownVars []string
	// envFaults are configuration faults found while REGISTERING the flags,
	// before parsing can begin — a malformed duration in the environment is
	// the only one today. Carried so they join the faults found after parsing
	// rather than pre-empting them.
	envFaults []string
}

// apiFlagSet registers this role's flags and their environment bindings, and
// returns them unparsed.
//
// Separate from parsing because the same registration answers two questions:
// what a boot should read, and what this role's configurable surface IS. The
// second is how a generated template or schema comes from the flags themselves
// rather than from a copy of them that drifts.
func apiFlagSet() (*flag.FlagSet, *cliflags.Env, *apiConfig, error) {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	env := &cliflags.Env{}
	cfg := &apiConfig{}
	env.String(fs, &cfg.dsn, "dsn", "MARGINCE_DSN", "", "Postgres DSN (runtime app role)")
	env.String(fs, &cfg.configPath, "config", "MARGINCE_CONFIG", "margince.yaml",
		"path to the deployment configuration file (A107/ADR-0061: bootstrap + auth); a missing file boots an existing installation but cannot bootstrap an empty database")
	env.String(fs, &cfg.schemaDSN, "schema-dsn", "MARGINCE_SCHEMA_DSN", "",
		"Postgres DSN (owner role) for the customfields runtime-DDL pool; unset = the two schema-change operations answer 501")
	fs.StringVar(&cfg.addr, "addr", ":8080", "listen address")
	env.String(fs, &cfg.redisAddr, "redis", "MARGINCE_REDIS", "localhost:16379", "Redis address (event bus)")
	fs.BoolVar(&cfg.inlineRelay, "inline-relay", true, "run the outbox relay in this process (false when cmd/worker runs it)")
	env.String(fs, &cfg.routingPath, "ai-routing", "MARGINCE_AI_ROUTING", "", "path to a routing file, read ONLY to seed an installation that has no stored model binding yet. The binding lives in the database once set and is changed through PUT /v1/ai/routing without a restart, so on a provisioned installation this flag is not read at all")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "drive the AI surfaces with the offline fake model (dev/test only)")
	env.String(fs, &cfg.logLevel, "log-level", "MARGINCE_LOG_LEVEL", "info", "log level: debug|info|warn|error")
	env.String(fs, &cfg.logFormat, "log-format", "MARGINCE_LOG_FORMAT", "text", "log format: text|json")
	env.String(fs, &cfg.publicBaseURL, "public-base-url", "MARGINCE_PUBLIC_BASE_URL", "", "canonical external scheme+host for buyer-facing links (RFC 8058 unsubscribe); required to send marketing mail and for the Gmail/Graph OAuth callback")
	env.String(fs, &cfg.mcpAppsBaseURL, "mcp-apps-base-url", "MARGINCE_MCP_APPS_BASE_URL", "",
		"origin the api reads the MCP App view documents from (defaults to --public-base-url); must be API-reachable, which is not the same as publicly reachable")
	env.String(fs, &cfg.gmailClientID, "gmail-client-id", "MARGINCE_GMAIL_CLIENT_ID", "", "Google OAuth client id for the Gmail capture connector; with the secret, state key and public-base-url, enables /connectors/gmail/*")
	env.String(fs, &cfg.gmailClientSecret, "gmail-client-secret", "MARGINCE_GMAIL_CLIENT_SECRET", "", "Google OAuth client secret for the Gmail capture connector")
	env.String(fs, &cfg.gmailPushToken, "gmail-push-token", "MARGINCE_GMAIL_PUSH_TOKEN", "", "shared secret on the Pub/Sub push subscription URL; enables POST /webhooks/gmail (empty = route absent)")
	env.String(fs, &cfg.gmailPushAudience, "gmail-push-audience", "MARGINCE_GMAIL_PUSH_AUDIENCE", "", "OIDC audience the Pub/Sub push subscription mints tokens for (this endpoint's public URL); with --gmail-push-service-account, the push webhook also verifies Google's OIDC token")
	env.String(fs, &cfg.gmailPushSA, "gmail-push-service-account", "MARGINCE_GMAIL_PUSH_SERVICE_ACCOUNT", "", "the Google service account email that signs Pub/Sub push OIDC tokens; verified as the token's email claim")
	env.String(fs, &cfg.gmailJWKSURL, "gmail-jwks-url", "MARGINCE_GMAIL_JWKS_URL", "", "override Google's OIDC JWKS URL; test/dev only")
	env.String(fs, &cfg.graphClientID, "graph-client-id", "MARGINCE_GRAPH_CLIENT_ID", "", "Microsoft (Entra) application id for the Outlook/M365 capture connector; with the secret, state key and public-base-url, enables /connectors/graph/*")
	env.String(fs, &cfg.graphClientSecret, "graph-client-secret", "MARGINCE_GRAPH_CLIENT_SECRET", "", "Microsoft client secret for the Outlook/M365 capture connector")
	env.String(fs, &cfg.graphTenant, "graph-tenant", "MARGINCE_GRAPH_TENANT", "", "Microsoft identity tenant for the consent endpoint (default: common — any organization)")
	env.String(fs, &cfg.hubspotAppSecret, "hubspot-app-secret", "MARGINCE_HUBSPOT_APP_SECRET", "", "HubSpot app client secret; verifies inbound overlay webhook v3 signatures and, when set, mounts /webhooks/hubspot (absent otherwise)")
	env.String(fs, &cfg.apiBaseURL, "api-base-url", "MARGINCE_API_BASE_URL", "", "the api's externally-reachable base for the OAuth callback redirect_uri; defaults to --public-base-url (same-origin deployments), set only when the api is on a different origin than the SPA (e.g. dev)")
	env.String(fs, &cfg.connectorStateKey, "connector-state-key", "MARGINCE_CONNECTOR_STATE_KEY", "", "HMAC key (>=32 bytes) signing the OAuth connect `state`; required for the Gmail and Graph connect flows")
	env.String(fs, &cfg.webhookKey, "webhook-key", "MARGINCE_WEBHOOK_KEY", "", "base64 32-byte key sealing outbound-webhook signing secrets; enables the mutating /webhook-subscriptions surface, and (with --inline-relay) the cg:webhooks delivery consumer. Empty = those paths answer 503 and no inline delivery runs. Re-attempting a parked delivery is the worker role's River job, never this one's.")
	env.String(fs, &cfg.metricsToken, "metrics-token", "MARGINCE_METRICS_TOKEN", "", "shared secret /metrics requires as a Bearer credential; empty (the default) answers 404 for /metrics rather than serving per-workspace job telemetry with no authentication at all")
	// A malformed TTL is CARRIED rather than returned, so it can be reported
	// beside a missing DSN instead of hiding it for a boot. Returning here
	// would put this fault ahead of every other one by accident of ordering —
	// the same one-fault-per-boot the collection below exists to end. The flag
	// still registers, on the compiled default, so parsing proceeds far enough
	// to find whatever else is wrong.
	accessTokenTTL, ttlErr := envDuration(oauthAccessTokenTTLEnv)
	if ttlErr != nil {
		cfg.envFaults = append(cfg.envFaults, ttlErr.Error())
	}
	fs.DurationVar(&cfg.oauthAccessTokenTTL, "oauth-access-token-ttl", accessTokenTTL,
		"lifetime of the access token (an Agent Seat Passport) the OAuth handshake mints, for the code exchange and every refresh rotation; 0 = the passport default of 720h (30 days), maximum 2160h (90 days)")
	return fs, env, cfg, nil
}

// parseAPIFlags parses and validates the boot flags; the DSN is the one
// dependency without a sane default, so its absence fails the boot here.
func parseAPIFlags(args []string) (apiConfig, error) {
	fs, env, cfg, err := apiFlagSet()
	if err != nil {
		return apiConfig{}, err
	}
	// Assembled here for two reasons. A surface that cannot be described
	// honestly is a boot error rather than a runtime surprise — a duplicate name
	// or a secret carrying a default would make a generated reference lie about
	// this installation — and the registry is also what tells this role which
	// namespaced variables it does NOT read, for the report below.
	registry, err := apiConfigItems(fs, env)
	if err != nil {
		return apiConfig{}, err
	}
	if err := fs.Parse(args); err != nil {
		return apiConfig{}, err
	}
	// The environment fills every flag the caller did not pass. It happens HERE
	// rather than in each flag's default because `flag` echoes a non-empty default
	// in its usage output, and these values are DSNs, signing keys, OAuth client
	// secrets and bearer tokens — see internal/platform/cliflags.
	env.Apply(fs, config.FromOS)
	// After Apply, so the report describes the environment the role actually
	// consulted.
	cfg.unknownVars = registry.Undeclared(config.Environ())
	cfg.posture = runtimeenv.Parse(config.FromOS(runtimeenv.EnvVar))
	// Every configuration fault this layer can see is collected and reported
	// TOGETHER, rather than returned at the first one.
	//
	// Returning early made starting the binary by hand a guessing game played
	// one boot at a time: the missing DSN was the only thing said, and the next
	// run then answered with the licence refusal, which is a second fact that
	// was true all along. Two boots to learn two requirements, and an operator
	// who fixes both at once never sees the second message at all.
	faults := append([]string{}, cfg.envFaults...)
	if cfg.dsn == "" {
		faults = append(faults, "--dsn or MARGINCE_DSN required")
	}
	// A TTL the mint would refuse must fail the BOOT, not the first handshake
	// of a connector nobody is watching.
	if cfg.oauthAccessTokenTTL < 0 || cfg.oauthAccessTokenTTL > identity.MaxOAuthAccessTokenTTL {
		faults = append(faults, fmt.Sprintf("--oauth-access-token-ttl %s is out of range: 0 (the default) or up to %s",
			cfg.oauthAccessTokenTTL, identity.MaxOAuthAccessTokenTTL))
	}
	if len(faults) == 1 {
		return apiConfig{}, errors.New("api: " + faults[0])
	}
	if len(faults) > 1 {
		return apiConfig{}, errors.New("api: the configuration is incomplete:\n  - " +
			strings.Join(faults, "\n  - "))
	}
	return *cfg, nil
}

// envDuration reads a duration from the environment as the default for its
// flag. A value the parser rejects is a boot error rather than a silently
// ignored setting — an operator who mistypes a TTL must not be told nothing and
// left running the default.
func envDuration(key string) (time.Duration, error) {
	raw := config.FromOS(key)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("api: %s=%q is not a duration (e.g. 15m, 24h): %w", key, raw, err)
	}
	return d, nil
}
