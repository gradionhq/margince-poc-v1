// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// loadEnvFile reads margince.env into a KEY=VALUE slice for the child
// processes.
//
// The server roles take their secrets from the environment (12-factor), which
// on a server is set by the deployment. A desktop installation has no
// deployment, so this file is that layer: it is the ONE place a user turns
// features on, and it is why "full features" does not require a terminal
// incantation.
//
// Values here are secrets. The file is created 0600 and never logged.
// The returns are NAMED so the deferred close can reach them: with unnamed
// results the assignment below compiles and does nothing, which is a swallowed
// error wearing the clothes of a handled one.
func loadEnvFile(path string) (env []string, err error) {
	// #nosec G304 -- path is layout.envPath(), derived from the installation directory; no request or user input reaches it
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		// A failure to close a read-only file cannot corrupt anything, but it
		// must not be silently discarded either.
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", path, closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		entry, parseErr := parseEnvLine(scanner.Text())
		if parseErr != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, parseErr)
		}
		if entry != "" {
			env = append(env, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return env, nil
}

// parseEnvLine returns a KEY=VALUE entry, or "" for a blank or comment line.
//
// A malformed line is an error rather than a skip: silently ignoring a
// mistyped setting is how a user ends up convinced a feature is broken when
// the real answer is a missing "=".
func parseEnvLine(raw string) (string, error) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil
	}
	// `export KEY=value` is what a user copying from shell instructions will
	// paste, so accept it rather than rejecting a reasonable habit.
	line = strings.TrimPrefix(line, "export ")

	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", fmt.Errorf("expected KEY=value, got %q", raw)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("missing a name before '=' in %q", raw)
	}

	value = strings.TrimSpace(value)
	// Quotes are how a value with spaces is written; strip one matched pair.
	if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' ||
		value[0] == '\'' && value[len(value)-1] == '\'') {
		value = value[1 : len(value)-1]
	}
	return key + "=" + value, nil
}

// ensureEnvFile writes the annotated template on first run and leaves an
// existing file alone, so a user's settings are never overwritten.
func ensureEnvFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(envTemplate), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// envTemplate documents every setting a desktop installation can turn on.
//
// Everything is commented out, so a fresh install runs the safe defaults and
// the file doubles as the reference for what is even possible — a user should
// not have to read Go source to discover that Gmail capture exists.
const envTemplate = `# Margince settings.
#
# Uncomment a line to turn a feature on, then restart Margince.
# This file holds secrets: it is created readable only by you (chmod 600).
# It is never overwritten by an update.

# ---------------------------------------------------------------------------
# Where Margince listens. Keep this stable so your browser bookmark keeps
# working. Margince refuses to start if the port is already in use rather
# than silently moving.
# ---------------------------------------------------------------------------
# MARGINCE_PORT=8800

# ---------------------------------------------------------------------------
# AI (bring your own key)
#
# Without a key the AI surfaces run on the offline fake model: they respond,
# but the answers are canned. Set the key for the provider you use AND create
# ai-routing.yaml next to this file to bind tasks to models.
# ---------------------------------------------------------------------------
# ANTHROPIC_API_KEY=
# OPENAI_API_KEY=
# GEMINI_API_KEY=
# OPENAI_COMPATIBLE_API_KEY=

# ---------------------------------------------------------------------------
# Attachments and logos (S3-compatible object storage)
#
# Unset, uploads have nowhere to go. Any S3-compatible endpoint works.
# ---------------------------------------------------------------------------
# MARGINCE_BLOBSTORE_ENDPOINT=
# MARGINCE_BLOBSTORE_ACCESS_KEY=
# MARGINCE_BLOBSTORE_SECRET_KEY=
# MARGINCE_BLOBSTORE_BUCKET=margince
# MARGINCE_BLOBSTORE_REGION=
# MARGINCE_BLOBSTORE_USE_SSL=true

# ---------------------------------------------------------------------------
# Public address
#
# Required to send marketing email (the unsubscribe link must be reachable)
# and for any OAuth callback. A machine only you can reach cannot serve
# either, so this needs a real externally-reachable URL.
# ---------------------------------------------------------------------------
# MARGINCE_PUBLIC_BASE_URL=

# ---------------------------------------------------------------------------
# Email + calendar capture (Gmail / Outlook)
#
# Needs OAuth credentials from Google or Microsoft, the signing key below,
# and MARGINCE_PUBLIC_BASE_URL above.
# MARGINCE_CONNECTOR_STATE_KEY must be at least 32 characters; generate one
# with:  openssl rand -hex 32
# ---------------------------------------------------------------------------
# MARGINCE_CONNECTOR_STATE_KEY=
# MARGINCE_GMAIL_CLIENT_ID=
# MARGINCE_GMAIL_CLIENT_SECRET=
# MARGINCE_GMAIL_PUBSUB_TOPIC=
# MARGINCE_GMAIL_PUSH_TOKEN=
# MARGINCE_GMAIL_PUSH_AUDIENCE=
# MARGINCE_GMAIL_PUSH_SERVICE_ACCOUNT=
# MARGINCE_GRAPH_CLIENT_ID=
# MARGINCE_GRAPH_CLIENT_SECRET=
# MARGINCE_GRAPH_TENANT=

# ---------------------------------------------------------------------------
# Stored connector credentials
#
# Encrypts the OAuth tokens Margince holds for the connectors above. Set this
# BEFORE connecting an account: changing it later makes existing stored
# credentials unreadable. Generate with:  openssl rand -base64 32
# ---------------------------------------------------------------------------
# MARGINCE_KEYVAULT_ROOT_KEY=

# ---------------------------------------------------------------------------
# Outbound webhooks
#
# Unset, the webhook subscription endpoints answer 503.
# Generate with:  openssl rand -base64 32
# ---------------------------------------------------------------------------
# MARGINCE_WEBHOOK_KEY=

# ---------------------------------------------------------------------------
# Incumbent CRM mirror (HubSpot overlay)
# ---------------------------------------------------------------------------
# MARGINCE_HUBSPOT_APP_SECRET=

# ---------------------------------------------------------------------------
# Diagnostics
#
# Logs are written to data/logs/ regardless; this controls how much detail.
# ---------------------------------------------------------------------------
# MARGINCE_LOG_LEVEL=info
# MARGINCE_LOG_FORMAT=text

# ---------------------------------------------------------------------------
# Agent access tokens (MCP). 0 = the 30-day default, maximum 2160h (90 days).
# ---------------------------------------------------------------------------
# MARGINCE_OAUTH_ACCESS_TOKEN_TTL=720h
`
