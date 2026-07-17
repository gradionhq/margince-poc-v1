// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The Gmail Pub/Sub push webhook (CAP-WIRE-N-4, ADR-0062): an operational,
// session-less edge Google POSTs a change notification to. It is NOT a CRM
// write surface (CAP-WIRE-N-1 holds) — it OIDC-verifies Google's push token,
// decodes {emailAddress}, and enqueues an incremental sync the worker runs;
// no CRM data comes from the request body. The pushed historyId is ignored
// (SyncOnce owns the cursor). Unconfigured, it answers the repo's standard 501.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// GmailPushConfig is the deployment's Pub/Sub push identity: the audience the
// subscription mints its OIDC token for (this endpoint's public URL) and the
// push service account that signs it. JWKSURL defaults to Google's when empty.
type GmailPushConfig struct {
	Audience       string
	ServiceAccount string
	JWKSURL        string
}

func (c GmailPushConfig) configured() bool {
	return c.Audience != "" && c.ServiceAccount != ""
}

// gmailPushHandler verifies then enqueues. enqueue is a seam so the transport
// is unit-testable without a DB; WithGmailPush fills it with the River inserter.
type gmailPushHandler struct {
	verifier *googleOIDCVerifier
	enqueue  func(ctx context.Context, email string) error
}

func (h gmailPushHandler) wired() bool { return h.verifier != nil && h.enqueue != nil }

func (h gmailPushHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.wired() {
		httperr.NotImplemented(w, r, "gmailPush")
		return
	}
	ctx := r.Context()
	if err := h.verifier.Verify(ctx, bearerToken(r.Header.Get("Authorization"))); err != nil {
		slog.WarnContext(ctx, "gmail push: verification failed", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized,
			Code:   "unauthorized",
			Detail: "push verification failed",
		})
		return
	}
	email, err := decodePushEmail(r.Body)
	if err != nil {
		// An authentic Google envelope we can't decode won't parse on retry
		// either — ack so Pub/Sub stops redelivering, and log for ops.
		slog.WarnContext(ctx, "gmail push: undecodable payload", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if email == "" {
		w.WriteHeader(http.StatusOK) // nothing to sync
		return
	}
	if err := h.enqueue(ctx, email); err != nil {
		// A durable-enqueue failure SHOULD be retried by Pub/Sub.
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// bearerToken extracts the token from an "Authorization: Bearer <t>" header,
// or "" when the scheme is absent/wrong.
func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return header[len(prefix):]
	}
	return ""
}

// pushEnvelope is the Pub/Sub push wrapper; data is base64 of the Gmail
// notification payload.
type pushEnvelope struct {
	Message struct {
		Data string `json:"data"`
	} `json:"message"`
}

type pushPayload struct {
	EmailAddress string `json:"emailAddress"` //nolint:tagliatelle // Gmail's wire format (camelCase)
}

// decodePushEmail reads the Pub/Sub push envelope and returns the notified
// mailbox address. The data field is standard (not URL-safe) base64.
func decodePushEmail(body io.Reader) (string, error) {
	var env pushEnvelope
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&env); err != nil {
		return "", fmt.Errorf("gmail push: decoding envelope: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(env.Message.Data)
	if err != nil {
		return "", fmt.Errorf("gmail push: decoding message data: %w", err)
	}
	var p pushPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("gmail push: decoding notification: %w", err)
	}
	return p.EmailAddress, nil
}

// WithGmailPush wires the Gmail Pub/Sub push webhook (api role). It requires
// the push config (audience + service account) and a River inserter; absent
// either, the /hooks/gmail/push route keeps its declared 501 by omission.
func WithGmailPush(inserter *jobs.Runner, c GmailPushConfig) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		if !c.configured() || inserter == nil {
			return
		}
		s.gmailPush = gmailPushHandler{
			verifier: newGoogleOIDCVerifier(c.JWKSURL, c.Audience, c.ServiceAccount),
			enqueue: func(ctx context.Context, email string) error {
				return inserter.Insert(ctx, GmailPushArgs{EmailAddress: email}, gmailPushInsertOpts())
			},
		}
	}
}
