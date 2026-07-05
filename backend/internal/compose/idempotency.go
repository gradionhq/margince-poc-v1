// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Transport-level idempotency (crm.yaml `IdempotencyKey`): a mutating
// request carrying an Idempotency-Key is safe to retry — the first
// attempt claims the key inside the caller's workspace scope, a replay
// within 24h returns the recorded response verbatim, and the same key
// with a DIFFERENT body is refused (409 idempotency_key_conflict, never
// a silent replay of mismatched intent). The claim row is written
// insert-first, so two concurrent attempts under one key can never both
// execute: the loser sees the claim and answers 409 while the first is
// in flight. Only a 2xx outcome is recorded; a failed attempt releases
// the claim so the client may retry the same key — replaying stored
// failures would pin transient faults for 24h and would break the
// stage-then-redeem approval flow, whose retry is the same request.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const idempotencyKeyHeader = "Idempotency-Key"

// idempotentOperations mirrors the contract operations that declare the
// IdempotencyKey parameter (grep api/crm.yaml for IdempotencyKey), keyed
// by "METHOD <chi route pattern>" exactly like agentPolicies. Requests
// outside this set pass through untouched even when they carry the
// header — the contract scopes the promise, not the client.
var idempotentOperations = map[string]bool{
	"POST /v1/people":                     true,
	"PATCH /v1/people/{id}":               true,
	"POST /v1/people/{id}/merge":          true,
	"POST /v1/organizations":              true,
	"PATCH /v1/organizations/{id}":        true,
	"POST /v1/organizations/{id}/merge":   true,
	"POST /v1/deals":                      true,
	"PATCH /v1/deals/{id}":                true,
	"POST /v1/deals/{id}/advance":         true,
	"POST /v1/pipelines":                  true,
	"PATCH /v1/pipelines/{id}":            true,
	"POST /v1/stages":                     true,
	"PATCH /v1/stages/{id}":               true,
	"POST /v1/activities":                 true,
	"PATCH /v1/activities/{id}":           true,
	"POST /v1/activities/{id}/relink":     true,
	"POST /v1/activities/{id}/send-email": true,
	"POST /v1/bookings":                   true,
	"POST /v1/leads":                      true,
	"PATCH /v1/leads/{id}":                true,
	"POST /v1/leads/{id}/promote":         true,
	"POST /v1/approvals/{id}/approve":     true,
	"POST /v1/data-subject-requests":      true,
	"POST /v1/people/{id}/consent":        true,
	"POST /v1/record-grants":              true,
}

// claimOutcome is what the claim transaction decided.
type claimOutcome int

const (
	claimFresh      claimOutcome = iota // this request executes
	claimReplay                         // recorded response is returned
	claimInProgress                     // first attempt has not finished
	claimMismatch                       // same key, different request digest
)

// idempotency is a contract-router middleware; it rides inside the
// session middleware, so workspace and principal are bound (the claim
// table is RLS-guarded and scoped per principal).
func idempotency(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(idempotencyKeyHeader)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			pattern := chi.RouteContext(r.Context()).RoutePattern()
			if !idempotentOperations[r.Method+" "+pattern] {
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > 255 {
				httperr.Write(w, r, httperr.Validation(idempotencyKeyHeader, "too_long", "Idempotency-Key exceeds 255 characters"))
				return
			}
			actor, ok := principal.Actor(r.Context())
			if !ok {
				next.ServeHTTP(w, r) // unauthenticated requests fail auth downstream
				return
			}

			// Bound the buffer at the site (the chassis LimitBodies cap also
			// applies, but the invariant should be visible here, as it is in
			// the agent gate's maxGatedBody read).
			body, err := io.ReadAll(io.LimitReader(r.Body, maxGatedBody+1))
			if err != nil || len(body) > maxGatedBody {
				httperr.Write(w, r, httperr.Validation("body", "unreadable", "request body unreadable or too large"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			sum := sha256.Sum256(body)
			digest := hex.EncodeToString(sum[:])
			// The concrete path, not the pattern: the contract scopes the
			// key per request-path, so /deals/A and /deals/B never collide.
			endpoint := r.Method + " " + r.URL.Path

			outcome, stored := claimKey(r, pool, actor.ID, key, endpoint, digest)
			switch outcome {
			case claimReplay:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(stored.status)
				if stored.body != "" {
					_, _ = io.WriteString(w, stored.body)
				}
				return
			case claimInProgress:
				httperr.Write(w, r, &httperr.DetailedError{
					Status: http.StatusConflict,
					Code:   "idempotency_key_conflict",
					Detail: "a request with this idempotency key is still in progress",
				})
				return
			case claimMismatch:
				httperr.Write(w, r, &httperr.DetailedError{
					Status: http.StatusConflict,
					Code:   "idempotency_key_conflict",
					Detail: "this idempotency key was already used with a different request body",
				})
				return
			}

			rec := &replayRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			settleClaim(r, pool, actor.ID, key, endpoint, rec)
		})
	}
}

type storedResponse struct {
	status int
	body   string
}

// claimKey runs the insert-first claim. Any claim-infrastructure failure
// degrades to claimFresh: idempotency is a retry-safety layer, and
// refusing the request because the layer itself hiccupped would make
// retries LESS safe than not sending the header at all.
func claimKey(r *http.Request, pool *pgxpool.Pool, principalID, key, endpoint, digest string) (claimOutcome, storedResponse) {
	outcome := claimFresh
	var stored storedResponse
	err := database.WithWorkspaceTx(r.Context(), pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `
			INSERT INTO idempotency_key (workspace_id, principal_id, key, endpoint, request_digest)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)
			ON CONFLICT (workspace_id, principal_id, key, endpoint) DO NOTHING`,
			principalID, key, endpoint, digest)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil // fresh claim
		}
		var storedDigest string
		var status *int
		var respBody *string
		var expired bool
		if err := tx.QueryRow(r.Context(), `
			SELECT request_digest, response_status, response_body, created_at < now() - interval '24 hours'
			FROM idempotency_key
			WHERE principal_id = $1 AND key = $2 AND endpoint = $3
			FOR UPDATE`,
			principalID, key, endpoint).Scan(&storedDigest, &status, &respBody, &expired); err != nil {
			return err
		}
		if expired {
			// Past the retention window the key means nothing anymore:
			// re-claim it in place for this attempt.
			_, err := tx.Exec(r.Context(), `
				UPDATE idempotency_key
				SET request_digest = $4, response_status = NULL, response_body = NULL, created_at = now()
				WHERE principal_id = $1 AND key = $2 AND endpoint = $3`,
				principalID, key, endpoint, digest)
			return err
		}
		switch {
		case storedDigest != digest:
			outcome = claimMismatch
		case status == nil:
			outcome = claimInProgress
		default:
			outcome = claimReplay
			stored.status = *status
			if respBody != nil {
				stored.body = *respBody
			}
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "idempotency claim failed; executing without replay protection", "err", err)
		return claimFresh, storedResponse{}
	}
	return outcome, stored
}

// settleClaim records a 2xx outcome for replay and releases the claim on
// anything else (see the package comment for why failures are not
// replayed).
func settleClaim(r *http.Request, pool *pgxpool.Pool, principalID, key, endpoint string, rec *replayRecorder) {
	err := database.WithWorkspaceTx(r.Context(), pool, func(tx pgx.Tx) error {
		if rec.status >= 200 && rec.status < 300 {
			_, err := tx.Exec(r.Context(), `
				UPDATE idempotency_key SET response_status = $4, response_body = $5
				WHERE principal_id = $1 AND key = $2 AND endpoint = $3`,
				principalID, key, endpoint, rec.status, rec.buf.String())
			return err
		}
		_, err := tx.Exec(r.Context(), `
			DELETE FROM idempotency_key
			WHERE principal_id = $1 AND key = $2 AND endpoint = $3 AND response_status IS NULL`,
			principalID, key, endpoint)
		return err
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "idempotency claim settlement failed", "err", err)
	}
}

// replayRecorder tees the response so a later replay can repeat it.
type replayRecorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *replayRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *replayRecorder) Write(p []byte) (int, error) {
	r.buf.Write(p)
	return r.ResponseWriter.Write(p)
}
