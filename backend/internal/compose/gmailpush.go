// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Gmail Pub/Sub push webhook: the consuming half of the users.watch
// registration the renewal sweep maintains. A push names a mailbox and a
// historyId; this endpoint verifies the shared subscription token, bumps the
// matching connection's pacing clock, and enqueues its sync — making capture
// push-driven with the poll demoted to a safety net (CAP-PARAM-1's 60s p95
// is unreachable on a poll alone). It sits on the shared webhook chassis
// (webhook.go, design §6.5): admission and response discipline are the
// chassis's job, this file declares only what is genuinely Gmail-specific.
//
// Verification is layered: the Pub/Sub push token (constant-time compared,
// minted by the operator, carried as ?token= on the subscription's push
// endpoint) always; and, when the deployment configures the push identity
// (audience + push service account), the Google-signed OIDC ID token on the
// Authorization header as well — a forged POST then needs Google's private
// key, not just a leaked URL.
//
// A webhook that carries a hint may be dropped. A webhook that carries the
// only copy may not. Gmail's push names a mailbox and a historyId — a
// re-fetchable pointer into the history API — never message content, so it
// is handled entirely in memory: no raw persisted, no EnqueueTx. Telegram's
// webhook is the opposite (it carries the only copy of the message) and
// gets the transactional treatment where that file is built.

package compose

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// errNoEmailAddress is the one gmailNotification field the push handler
// cannot route without — Gmail always sends it, so its absence marks the
// payload as malformed rather than valid-but-unroutable.
var errNoEmailAddress = errors.New("gmail push: notification carries no emailAddress")

// pushEnvelope is the Pub/Sub push wrapper; Message.Data is base64 JSON.
type pushEnvelope struct {
	Message struct {
		Data string `json:"data"`
	} `json:"message"`
}

// gmailNotification is Gmail's watch payload inside the envelope. Gmail
// quotes historyId in push payloads, so it decodes as json.Number (either
// form), not uint64.
type gmailNotification struct {
	EmailAddress string      `json:"emailAddress"` //nolint:tagliatelle // Google names this field
	HistoryID    json.Number `json:"historyId"`    //nolint:tagliatelle // Google names this field
}

// GmailPushConfig is the push subscription's identity. Token is the shared
// URL secret and is required — empty leaves the route absent. Audience (this
// endpoint's public URL) and ServiceAccount (the Google account signing the
// push OIDC token) are set together to add OIDC verification; JWKSURL
// overrides Google's key endpoint for tests only.
type GmailPushConfig struct {
	Token          string
	Audience       string
	ServiceAccount string
	JWKSURL        string
}

// OIDC reports whether the config carries the full push identity needed to
// verify Google's OIDC token in addition to the shared URL secret.
func (c GmailPushConfig) OIDC() bool { return c.Audience != "" && c.ServiceAccount != "" }

// WithGmailPush mounts POST /webhooks/gmail. An empty token disables the
// endpoint entirely (the route is absent, not open); a full push identity
// upgrades it to OIDC-verified.
func WithGmailPush(inserter *jobs.Runner, cfg GmailPushConfig) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if cfg.Token == "" || inserter == nil {
			return
		}
		var verifier *googleOIDCVerifier
		if cfg.OIDC() {
			verifier = newGoogleOIDCVerifier(cfg.JWKSURL, cfg.Audience, cfg.ServiceAccount)
		}
		s.gmailPush = Webhook(gmailPushSpec(pool, inserter, cfg.Token, verifier, s.log), s.log)
	}
}

// gmailPushSpec declares Gmail's side of the chassis (design §6.5): one
// operator token shared by every mailbox in the deployment (Pub/Sub pushes
// to one URL per subscription — there is no per-mailbox path to key on), an
// optional Google-signed OIDC bearer as the second factor, and a Handle that
// never persists the payload it is handed — see the file comment for why.
func gmailPushSpec(pool *pgxpool.Pool, inserter *jobs.Runner, token string, verifier *googleOIDCVerifier, log *slog.Logger) WebhookSpec {
	spec := WebhookSpec{
		Provider: "gmail",
		MaxBody:  1 << 20,
		Secret: func(r *http.Request) (want, got string) {
			return token, r.URL.Query().Get("token")
		},
		Handle:   handleGmailPush(pool, inserter, log),
		OnAccept: http.StatusNoContent,
	}
	if verifier != nil {
		spec.Verify = func(ctx context.Context, r *http.Request) error {
			return verifier.Verify(ctx, httpserver.BearerToken(r.Header.Get("Authorization")))
		}
	}
	return spec
}

// handleGmailPush decodes the Pub/Sub envelope and routes the notification
// onto the sweep. A malformed envelope is Poison: the same bytes would fail
// identically on redelivery, so there is nothing a retry could fix. A
// routing or enqueue failure is Transient: redelivery is exactly the
// recovery path, because Gmail's history API can always be asked again —
// the push is a hint, never the only copy (see the file comment).
func handleGmailPush(pool *pgxpool.Pool, inserter *jobs.Runner, log *slog.Logger) func(context.Context, *http.Request, []byte) (Disposition, error) {
	return func(ctx context.Context, _ *http.Request, body []byte) (Disposition, error) {
		var env pushEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return Poison, err
		}
		data, err := base64.StdEncoding.DecodeString(env.Message.Data)
		if err != nil {
			// Pub/Sub may use URL-safe encoding; accept either before refusing.
			if data, err = base64.URLEncoding.DecodeString(env.Message.Data); err != nil {
				return Poison, err
			}
		}
		var note gmailNotification
		if err := json.Unmarshal(data, &note); err != nil {
			return Poison, err
		}
		if note.EmailAddress == "" {
			return Poison, errNoEmailAddress
		}

		// Route by the provider-owned identity in the connector's own cursor;
		// enqueue directly so the sync starts now, not at the next scan. The
		// bump+enqueue is idempotent (unique-by-args while incomplete), so a
		// Transient-triggered redelivery cannot double-run it.
		hits, err := capture.BumpDueByMailbox(ctx, pool, "gmail", note.EmailAddress)
		if err != nil {
			return Transient, err
		}
		for _, d := range hits {
			if err := inserter.Enqueue(ctx, CaptureSyncArgs{
				Workspace:    d.Workspace.UUID,
				ConnectionID: d.ID.String(),
				Provider:     "gmail",
			}, &river.InsertOpts{
				// river's default uniqueness window includes completed jobs;
				// activeSweepStates deliberately excludes them, so this must
				// stay exactly as-is — dropping ByState would suppress a
				// legitimate re-sync any time the prior one had finished.
				UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates},
			}); err != nil {
				log.ErrorContext(ctx, "gmail push: enqueueing sync", "connection", d.ID.String(), "err", err)
				return Transient, err
			}
		}
		// A mailbox nobody connected is Accepted too: nothing here a
		// redelivery would fix, and Pub/Sub must stop retrying.
		return Accepted, nil
	}
}
