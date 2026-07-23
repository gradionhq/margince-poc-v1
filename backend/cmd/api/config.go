// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"errors"
	"flag"
	"os"
	"time"
)

// apiConfig is the parsed boot configuration of the api process.
type apiConfig struct {
	dsn                  string
	configPath           string
	schemaDSN            string
	addr                 string
	redisAddr            string
	inlineRelay          bool
	routingPath          string
	fakeBrain            bool
	logLevel             string
	logFormat            string
	publicBaseURL        string
	apiBaseURL           string
	gmailClientID        string
	gmailClientSecret    string
	gmailPushToken       string
	gmailPushAudience    string
	gmailPushSA          string
	gmailJWKSURL         string
	graphClientID        string
	graphClientSecret    string
	graphTenant          string
	hubspotAppSecret     string
	connectorStateKey    string
	webhookKey           string
	webhookRetryInterval time.Duration
}

// parseAPIFlags parses and validates the boot flags; the DSN is the one
// dependency without a sane default, so its absence fails the boot here.
func parseAPIFlags(args []string) (apiConfig, error) {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	var cfg apiConfig
	fs.StringVar(&cfg.dsn, "dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	fs.StringVar(&cfg.configPath, "config", envOr("MARGINCE_CONFIG", "margince.yaml"),
		"path to the deployment configuration file (A107/ADR-0061: bootstrap + auth); a missing file boots an existing installation but cannot bootstrap an empty database")
	fs.StringVar(&cfg.schemaDSN, "schema-dsn", os.Getenv("MARGINCE_SCHEMA_DSN"),
		"Postgres DSN (owner role) for the customfields runtime-DDL pool; unset = the two schema-change operations answer 501")
	fs.StringVar(&cfg.addr, "addr", ":8080", "listen address")
	fs.StringVar(&cfg.redisAddr, "redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	fs.BoolVar(&cfg.inlineRelay, "inline-relay", true, "run the outbox relay in this process (false when cmd/worker runs it)")
	fs.StringVar(&cfg.routingPath, "ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml; enables the cold-start read-back")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "drive the AI surfaces with the offline fake model (dev/test only)")
	fs.StringVar(&cfg.logLevel, "log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&cfg.logFormat, "log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")
	fs.StringVar(&cfg.publicBaseURL, "public-base-url", os.Getenv("MARGINCE_PUBLIC_BASE_URL"), "canonical external scheme+host for buyer-facing links (RFC 8058 unsubscribe); required to send marketing mail and for the Gmail/Graph OAuth callback")
	fs.StringVar(&cfg.gmailClientID, "gmail-client-id", os.Getenv("MARGINCE_GMAIL_CLIENT_ID"), "Google OAuth client id for the Gmail capture connector; with the secret, state key and public-base-url, enables /connectors/gmail/*")
	fs.StringVar(&cfg.gmailClientSecret, "gmail-client-secret", os.Getenv("MARGINCE_GMAIL_CLIENT_SECRET"), "Google OAuth client secret for the Gmail capture connector")
	fs.StringVar(&cfg.gmailPushToken, "gmail-push-token", os.Getenv("MARGINCE_GMAIL_PUSH_TOKEN"), "shared secret on the Pub/Sub push subscription URL; enables POST /webhooks/gmail-push (empty = route absent)")
	fs.StringVar(&cfg.gmailPushAudience, "gmail-push-audience", os.Getenv("MARGINCE_GMAIL_PUSH_AUDIENCE"), "OIDC audience the Pub/Sub push subscription mints tokens for (this endpoint's public URL); with --gmail-push-service-account, the push webhook also verifies Google's OIDC token")
	fs.StringVar(&cfg.gmailPushSA, "gmail-push-service-account", os.Getenv("MARGINCE_GMAIL_PUSH_SERVICE_ACCOUNT"), "the Google service account email that signs Pub/Sub push OIDC tokens; verified as the token's email claim")
	fs.StringVar(&cfg.gmailJWKSURL, "gmail-jwks-url", os.Getenv("MARGINCE_GMAIL_JWKS_URL"), "override Google's OIDC JWKS URL; test/dev only")
	fs.StringVar(&cfg.graphClientID, "graph-client-id", os.Getenv("MARGINCE_GRAPH_CLIENT_ID"), "Microsoft (Entra) application id for the Outlook/M365 capture connector; with the secret, state key and public-base-url, enables /connectors/graph/*")
	fs.StringVar(&cfg.graphClientSecret, "graph-client-secret", os.Getenv("MARGINCE_GRAPH_CLIENT_SECRET"), "Microsoft client secret for the Outlook/M365 capture connector")
	fs.StringVar(&cfg.graphTenant, "graph-tenant", os.Getenv("MARGINCE_GRAPH_TENANT"), "Microsoft identity tenant for the consent endpoint (default: common — any organization)")
	fs.StringVar(&cfg.hubspotAppSecret, "hubspot-app-secret", os.Getenv("MARGINCE_HUBSPOT_APP_SECRET"), "HubSpot app client secret; verifies inbound overlay webhook v3 signatures and, when set, mounts /webhooks/hubspot (absent otherwise)")
	fs.StringVar(&cfg.apiBaseURL, "api-base-url", os.Getenv("MARGINCE_API_BASE_URL"), "the api's externally-reachable base for the OAuth callback redirect_uri; defaults to --public-base-url (same-origin deployments), set only when the api is on a different origin than the SPA (e.g. dev)")
	fs.StringVar(&cfg.connectorStateKey, "connector-state-key", os.Getenv("MARGINCE_CONNECTOR_STATE_KEY"), "HMAC key (>=32 bytes) signing the OAuth connect `state`; required for the Gmail and Graph connect flows")
	fs.StringVar(&cfg.webhookKey, "webhook-key", os.Getenv("MARGINCE_WEBHOOK_KEY"), "base64 32-byte key sealing outbound-webhook signing secrets; enables the mutating /webhook-subscriptions surface, and (with --inline-relay) the cg:webhooks delivery consumer + retry sweep. Empty = those paths answer 503 and no inline delivery runs.")
	fs.DurationVar(&cfg.webhookRetryInterval, "webhook-retry-interval", 5*time.Second, "outbound-webhook retry-sweep tick interval (inline-relay only)")
	if err := fs.Parse(args); err != nil {
		return apiConfig{}, err
	}
	if cfg.dsn == "" {
		return apiConfig{}, errors.New("api: --dsn or MARGINCE_DSN required")
	}
	return cfg, nil
}

// envOr reads an environment variable with an explicit default, keeping
// flag definitions self-documenting.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
