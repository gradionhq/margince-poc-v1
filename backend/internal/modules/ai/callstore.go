// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Call is one completion terminal's trace record (Layer 1): who routed
// where, how much it cost, and whether it was served, cached, or failed.
// It is telemetry, not a domain write — no audit/outbox ride-along.
type Call struct {
	CorrelationID      *ids.UUID
	Task               Task
	Tier               Tier
	Provider           string
	ModelID            string
	RequestFingerprint string
	TokensIn           int
	TokensOut          int
	ReasoningTokens    int
	CachedTokens       int
	LatencyMS          int64
	CacheHit           bool
	// CacheOff records that the serving Router had the result cache
	// disabled (ai.WithoutResultCache — the cert lane and scripted tests
	// that must observe every repeat call, not a collapsed cache hit).
	// In-memory only this phase: the ai_call.cache_off column and the
	// CallMeter write land in Phase 3; CallMeter ignores this field until
	// then.
	CacheOff bool
	// ServedModel is the provider-reported identity of the model that
	// actually answered (model.Response.ServedModel), and ServedIdentitySource
	// names how that identity was obtained: "response" when the adapter reads
	// it off the wire response body, "echo" when the generic OpenAI-compatible
	// wire merely echoes back the requested model rather than confirming what
	// served it, "configured" when the provider reported no identity at all
	// and the trace falls back to the tier's configured binding. In-memory
	// only for now — the ai_call columns and the CallMeter write land in a
	// later migration; CallMeter ignores these fields until then.
	ServedModel          string
	ServedIdentitySource string
	Degraded             bool
	ErrorSentinel        string
	AgentRunID           *ids.UUID
	// Payload, when non-nil, carries the opt-in post-stripper content
	// (Layer 3). It is written to ai_call_payload in the SAME transaction
	// so the content row can never outlive its metadata row.
	Payload *Payload
}

// Payload is the Layer-3 opt-in content: the post-SecretStripper request
// (system + messages) and the model's response text. Special-category-
// adjacent — retention-aged and erasure-cascaded, never in audit_log.
type Payload struct {
	Request  json.RawMessage
	Response json.RawMessage
}

// CallRecorder is what the router needs to trace a call; the interface
// keeps router unit tests off Postgres while CallMeter is the one real
// impl. Exported so the DB-less local router seam (ai.WithCallStore) can
// take a caller-supplied recorder (an in-memory store for a cert run or a
// test) without reaching into Postgres.
type CallRecorder interface {
	Record(ctx context.Context, c Call) error
}

// callStore is the pre-existing internal name, kept as an alias so every
// call site inside this package (Router.calls, NewRouter, assembleRouter)
// compiles unchanged.
type callStore = CallRecorder

// CallMeter writes ai_call (+ ai_call_payload when capture is on). It rides
// the workspace GUC transaction like every tenant write.
type CallMeter struct {
	pool *pgxpool.Pool
}

// NewCallMeter constructs the CallMeter that writes ai_call trace rows
// (and, when payload capture is on, the linked ai_call_payload row).
func NewCallMeter(pool *pgxpool.Pool) *CallMeter { return &CallMeter{pool: pool} }

// Record writes the ai_call row — and, when c.Payload is set, the
// ai_call_payload row — in one workspace transaction, so the content row
// can never outlive its metadata row.
func (m *CallMeter) Record(ctx context.Context, c Call) error {
	return database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		var callID ids.UUID
		err := tx.QueryRow(
			ctx, `
			INSERT INTO ai_call (
			  workspace_id, correlation_id, task, tier, provider, model_id,
			  request_fingerprint, tokens_in, tokens_out, reasoning_tokens,
			  cached_tokens, latency_ms, cache_hit, degraded, error_sentinel, agent_run_id)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),$15)
			RETURNING id`,
			c.CorrelationID, string(c.Task), string(c.Tier), c.Provider, c.ModelID,
			c.RequestFingerprint, c.TokensIn, c.TokensOut, c.ReasoningTokens,
			c.CachedTokens, c.LatencyMS, c.CacheHit, c.Degraded, c.ErrorSentinel, c.AgentRunID,
		).Scan(&callID)
		if err != nil {
			return fmt.Errorf("ai: recording call: %w", err)
		}
		if c.Payload == nil {
			return nil
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO ai_call_payload (workspace_id, ai_call_id, request_payload, response_payload)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)`,
			callID, c.Payload.Request, c.Payload.Response)
		if err != nil {
			return fmt.Errorf("ai: recording call payload: %w", err)
		}
		return nil
	})
}

// errMeteringFailed marks the terminal where a call was SERVED but the
// usage meter's write failed. It is distinct from a provider error: the
// model answered, only the metering-DB write did not — classifyError maps
// it to its own sentinel so the trace does not mislabel a successful call.
var errMeteringFailed = errors.New("ai: metering failed")

// classifyError maps a completion terminal error to a short, stable code
// for ai_call.error_sentinel. It never stores raw error text — that could
// leak provider internals into the trace store; the code is enough to
// spot patterns, and the failing call's own logs carry the detail.
func classifyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrBudgetExhausted):
		return "budget_exhausted"
	case errors.Is(err, errMeteringFailed):
		return "metering_failed"
	default:
		return "provider_error"
	}
}
