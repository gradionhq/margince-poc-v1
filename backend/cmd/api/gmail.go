// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// gmailOptions wires the Gmail capture surface: the OAuth
// connect/callback transport (which rides the vault, so the caller
// appends these AFTER keyvault options) and, when a subscription token
// is configured, the Pub/Sub push webhook over an insert-only River
// client (the deep-read pattern — the api enqueues, the worker works).
// WithGmailCapture self-gates: absent the client id/secret, state key,
// or public base URL it is a no-op and /connectors/gmail/* keeps its
// declared 501.
func gmailOptions(cfg apiConfig, pool *pgxpool.Pool, logger *slog.Logger, stdout io.Writer) ([]compose.Option, error) {
	gmailCfg := compose.GmailConfig{
		ClientID:      cfg.gmailClientID,
		ClientSecret:  cfg.gmailClientSecret,
		StateKey:      cfg.connectorStateKey,
		PublicBaseURL: cfg.publicBaseURL,
		APIBaseURL:    cfg.apiBaseURL,
	}
	opts := []compose.Option{compose.WithGmailCapture(gmailCfg)}
	if gmailCfg.Enabled() && cfg.gmailPushToken != "" {
		pushInserter, err := jobs.NewInserter(pool, logger)
		if err != nil {
			return nil, err
		}
		opts = append(opts, compose.WithGmailPush(pushInserter, cfg.gmailPushToken))
		_, _ = fmt.Fprintln(stdout, "api gmail push webhook enabled (/webhooks/gmail-push)")
	}
	switch {
	case gmailCfg.Enabled():
		_, _ = fmt.Fprintln(stdout, "api gmail capture connector enabled (/connectors/gmail/*)")
	case cfg.gmailClientID != "":
		_, _ = fmt.Fprintln(stdout, "api gmail capture connector configured but INCOMPLETE — needs client secret, --connector-state-key (>=32B), and --public-base-url; surface stays 501")
	}
	return opts, nil
}
