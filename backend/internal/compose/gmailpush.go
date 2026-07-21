// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Gmail Pub/Sub push webhook: the consuming half of the users.watch
// registration the renewal sweep maintains. A push names a mailbox and a
// historyId; this endpoint verifies the shared subscription token, bumps the
// matching connection's pacing clock, and enqueues its sync — making capture
// push-driven with the poll demoted to a safety net (CAP-PARAM-1's 60s p95
// is unreachable on a poll alone).
//
// Verification is layered: the Pub/Sub push token (constant-time compared,
// minted by the operator, carried as ?token= on the subscription's push
// endpoint) always; and, when the deployment configures the push identity
// (audience + push service account), the Google-signed OIDC ID token on the
// Authorization header as well — a forged POST then needs Google's private
// key, not just a leaked URL.

package compose

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

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

type gmailPushHandler struct {
	pool     *pgxpool.Pool
	inserter *jobs.Runner
	token    string
	verifier *googleOIDCVerifier
	log      *slog.Logger
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

// WithGmailPush mounts POST /webhooks/gmail-push. An empty token disables
// the endpoint entirely (the route is absent, not open); a full push
// identity upgrades it to OIDC-verified.
func WithGmailPush(inserter *jobs.Runner, cfg GmailPushConfig) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if cfg.Token == "" || inserter == nil {
			return
		}
		h := &gmailPushHandler{pool: pool, inserter: inserter, token: cfg.Token, log: s.log}
		if cfg.OIDC() {
			h.verifier = newGoogleOIDCVerifier(cfg.JWKSURL, cfg.Audience, cfg.ServiceAccount)
		}
		s.gmailPush = h
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <t>" header;
// anything else — wrong scheme, bare token, empty — yields "".
func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func (h *gmailPushHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Wrong/missing token → 403 and Pub/Sub stops delivering to a
	// misconfigured (or hostile) subscription after its retry budget. The
	// digests equalize length first, so the compare leaks neither content
	// nor token length.
	got := sha256.Sum256([]byte(r.URL.Query().Get("token")))
	want := sha256.Sum256([]byte(h.token))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	// With a configured push identity, the request must also carry Google's
	// OIDC token. 401 (not 403): Pub/Sub re-mints and retries, so a key
	// rotation blip heals; the rejection detail stays in server logs.
	if h.verifier != nil {
		if err := h.verifier.Verify(r.Context(), bearerToken(r.Header.Get("Authorization"))); err != nil {
			h.log.WarnContext(r.Context(), "gmail push: OIDC verification failed", "err", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var env pushEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data, err := base64.StdEncoding.DecodeString(env.Message.Data)
	if err != nil {
		// Pub/Sub may use URL-safe encoding; accept either before refusing.
		if data, err = base64.URLEncoding.DecodeString(env.Message.Data); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	var note gmailNotification
	if err := json.Unmarshal(data, &note); err != nil || note.EmailAddress == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Route by the provider-owned identity in the connector's own cursor;
	// enqueue directly so the sync starts now, not at the next scan. Failures
	// here answer 500 so Pub/Sub redelivers — the bump+enqueue is idempotent
	// (unique-by-args while incomplete), so a redelivery cannot double-run.
	hits, err := capture.BumpDueByMailbox(r.Context(), h.pool, "gmail", note.EmailAddress)
	if err != nil {
		h.log.ErrorContext(r.Context(), "gmail push: routing notification", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for _, d := range hits {
		if err := h.inserter.Enqueue(r.Context(), CaptureSyncArgs{
			Workspace:    d.Workspace.String(),
			ConnectionID: d.ID.String(),
			Provider:     "gmail",
		}, &river.InsertOpts{
			UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates},
		}); err != nil {
			h.log.ErrorContext(r.Context(), "gmail push: enqueueing sync", "connection", d.ID.String(), "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	// 204 also for a mailbox nobody connected: nothing here a redelivery
	// would fix, and Pub/Sub must stop retrying.
	w.WriteHeader(http.StatusNoContent)
}
