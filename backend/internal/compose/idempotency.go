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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const idempotencyKeyHeader = "Idempotency-Key"

// replayWindow is how long a settled claim stays replayable — the contract's
// 24h. Spelled once: claimKey re-claims a row past it, and the retention sweep
// (idempotencyretention.go) deletes one past it, and those two must be talking
// about the same moment or the sweep would delete rows a replay could still
// legitimately serve.
const replayWindow = 24 * time.Hour

// claimOutcome is what the claim transaction decided.
type claimOutcome int

const (
	claimFresh      claimOutcome = iota // this request executes
	claimReplay                         // recorded response is returned
	claimInProgress                     // first attempt has not finished
	claimMismatch                       // same key, different request digest
)

// The claim itself — claimKey, settleClaim, releaseClaim — takes a CONTEXT and
// the four values that identify a claim, never an *http.Request: the governed
// tool surface claims keys through the same three functions for a tools/call
// (agentidempotency.go), and two transports each holding their own claim
// transaction would be two answers to the question "is this the same call".
//
// idempotency is a contract-router middleware; it rides inside the
// session middleware, so workspace and principal are bound (the claim
// table is RLS-guarded and scoped per principal).
func idempotency(pool *pgxpool.Pool, probes map[string]replayProbe) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(idempotencyKeyHeader)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			route := r.Method + " " + chi.RouteContext(r.Context()).RoutePattern()
			if _, replayable := replayableOperations[route]; !replayable {
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

			outcome, stored, err := claimKey(r.Context(), pool, actor.ID, key, endpoint, digest)
			if err != nil {
				// Degraded, not refused: idempotency is a retry-safety layer,
				// and refusing the request because the layer itself hiccupped
				// would make retries LESS safe than not sending the header at
				// all. The tool surface takes the same posture for the same
				// reason (agents.Registry.runOnce).
				slog.ErrorContext(r.Context(), "idempotency claim failed; executing without replay protection", "err", err)
				outcome, stored = claimFresh, storedResponse{}
			}
			if outcome != claimFresh {
				writeClaimOutcome(w, r, pool, probes, route, outcome, stored)
				return
			}

			rec := &replayRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			if err := settleClaim(r.Context(), pool, actor.ID, key, endpoint,
				rec.status, rec.buf.String(), rec.Header().Get("Content-Type")); err != nil {
				slog.ErrorContext(r.Context(), "idempotency claim settlement failed", "err", err)
			}
		})
	}
}

// writeClaimOutcome answers a claim that did not win the race: a replay
// (gated), a first attempt still in flight, or the same key reused for a
// different body.
func writeClaimOutcome(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, probes map[string]replayProbe, route string, outcome claimOutcome, stored storedResponse) {
	switch outcome {
	case claimReplay:
		// A replay is a read (API-CC-8): the recorded body only goes back if
		// the caller can still see the record it carries.
		if err := ensureReplayVisible(r.Context(), pool, probes, route, stored.body); err != nil {
			httperr.Write(w, r, err)
			return
		}
		// The replay repeats the ORIGINAL response verbatim — status, body,
		// and the media type recorded with it (0069), never a restamped
		// Content-Type.
		if stored.contentType != "" {
			w.Header().Set("Content-Type", stored.contentType)
		}
		w.WriteHeader(stored.status)
		if stored.body != "" {
			_, _ = io.WriteString(w, stored.body)
		}
	case claimInProgress:
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "idempotency_key_conflict",
			Detail: "a request with this idempotency key is still in progress",
		})
	case claimMismatch:
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "idempotency_key_conflict",
			Detail: "this idempotency key was already used with a different request body",
		})
	case claimFresh:
		// Unreachable: the caller returns early only for a non-fresh claim.
	}
}

type storedResponse struct {
	status      int
	body        string
	contentType string
}

// claimKey runs the insert-first claim. Any claim-infrastructure failure
// degrades to claimFresh: idempotency is a retry-safety layer, and
// refusing the request because the layer itself hiccupped would make
// retries LESS safe than not sending the header at all.
func claimKey(ctx context.Context, pool *pgxpool.Pool, principalID, key, endpoint, digest string) (claimOutcome, storedResponse, error) {
	outcome := claimFresh
	var stored storedResponse
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		claimed, err := insertClaim(ctx, tx, principalID, key, endpoint, digest)
		if err != nil {
			return err
		}
		if claimed {
			return nil // fresh claim
		}
		outcome, stored, err = resolveExistingClaim(ctx, tx, principalID, key, endpoint, digest)
		return err
	})
	if err != nil {
		return claimFresh, storedResponse{}, err
	}
	return outcome, stored, nil
}

// insertClaim writes the claim row insert-first and reports whether THIS
// attempt is the one that claimed the key: false means a row for the same
// (workspace, principal, key, endpoint) already exists.
func insertClaim(ctx context.Context, tx pgx.Tx, principalID, key, endpoint, digest string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_key (workspace_id, principal_id, key, endpoint, request_digest)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)
		ON CONFLICT (workspace_id, principal_id, key, endpoint) DO NOTHING`,
		principalID, key, endpoint, digest)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// resolveExistingClaim decides what an already-claimed key means for this
// attempt: a replay of the recorded response, a first attempt still in flight,
// the same key reused for a different body, or — past the replay window — a
// re-claim in place. The row is read FOR UPDATE inside the caller's
// transaction, so two concurrent attempts under one key cannot both execute.
func resolveExistingClaim(ctx context.Context, tx pgx.Tx, principalID, key, endpoint, digest string) (claimOutcome, storedResponse, error) {
	var storedDigest, contentType string
	var status *int
	var respBody *string
	var expired bool
	err := tx.QueryRow(ctx, `
		SELECT request_digest, response_status, response_body, response_content_type,
		       created_at < now() - make_interval(secs => $4)
		FROM idempotency_key
		WHERE principal_id = $1 AND key = $2 AND endpoint = $3
		FOR UPDATE`,
		principalID, key, endpoint, replayWindow.Seconds()).Scan(&storedDigest, &status, &respBody, &contentType, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		// The retention sweep removed the row between the INSERT above and
		// this read. It was past the replay window, so it was protecting
		// nothing — but simply erroring here would degrade to executing
		// with NO claim recorded, and two concurrent retries of one key
		// would then both execute. Claim it again instead.
		claimed, err := insertClaim(ctx, tx, principalID, key, endpoint, digest)
		if err != nil {
			return claimFresh, storedResponse{}, err
		}
		if claimed {
			return claimFresh, storedResponse{}, nil
		}
		// Someone re-created it in the same instant; the honest answer is
		// that this attempt is still in flight elsewhere.
		return claimInProgress, storedResponse{}, nil
	}
	if err != nil {
		return claimFresh, storedResponse{}, err
	}
	if expired {
		// Past the retention window the key means nothing anymore:
		// re-claim it in place for this attempt.
		_, err := tx.Exec(ctx, `
			UPDATE idempotency_key
			SET request_digest = $4, response_status = NULL, response_body = NULL,
			    response_content_type = DEFAULT, created_at = now()
			WHERE principal_id = $1 AND key = $2 AND endpoint = $3`,
			principalID, key, endpoint, digest)
		return claimFresh, storedResponse{}, err
	}
	switch {
	case storedDigest != digest:
		return claimMismatch, storedResponse{}, nil
	case status == nil:
		return claimInProgress, storedResponse{}, nil
	default:
		stored := storedResponse{status: *status, contentType: contentType}
		if respBody != nil {
			stored.body = *respBody
		}
		return claimReplay, stored, nil
	}
}

// settleClaim records a 2xx outcome for replay and releases the claim on
// anything else (see the package comment for why failures are not
// replayed).
func settleClaim(ctx context.Context, pool *pgxpool.Pool, principalID, key, endpoint string,
	status int, body, contentType string,
) error {
	if status < 200 || status >= 300 {
		return releaseClaim(ctx, pool, principalID, key, endpoint)
	}
	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE idempotency_key SET response_status = $4, response_body = $5, response_content_type = $6
			WHERE principal_id = $1 AND key = $2 AND endpoint = $3`,
			principalID, key, endpoint, status, body, contentType)
		return err
	})
}

// releaseClaim gives an unsettled key back, so the caller may retry it. Only a
// claim with no recorded response is released: a settled one is a result
// somebody may still replay, and deleting it here would turn a completed call
// into one that executes a second time.
func releaseClaim(ctx context.Context, pool *pgxpool.Pool, principalID, key, endpoint string) error {
	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			DELETE FROM idempotency_key
			WHERE principal_id = $1 AND key = $2 AND endpoint = $3 AND response_status IS NULL`,
			principalID, key, endpoint)
		return err
	})
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
