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

// callKindCompletion and callKindEmbedding are the ai_call.kind vocabulary
// (0100): a chat-ladder call versus one embed-lane call. Every Call this
// package builds sets one explicitly — the column carries NOT NULL with a
// CHECK, so an unset Kind would reach the database as an empty string and
// fail the constraint, not fall back to the SQL-side DEFAULT.
const (
	callKindCompletion = "completion"
	callKindEmbedding  = "embedding"
)

// Call is one ATTEMPT's trace record (Layer 1, spec §4): who routed
// where, how much it cost, and whether it was served, cached, or failed.
// A single logical call — one Complete/CompleteStructured/Embed invocation
// from the caller's point of view — can span several Call rows sharing one
// LogicalCallID when the router retries, degrades, or escalates: every
// rung it actually walked lands its own row, and IsTerminal names the one
// whose response the caller received. It is telemetry, not a domain
// write — no audit/outbox ride-along.
type Call struct {
	// LogicalCallID groups every attempt of one served-or-failed decision.
	// Minted once per logical call (ids.NewV7()) and shared by every Call
	// appended under it.
	LogicalCallID ids.UUID
	// Attempt is this row's 1-based position within its logical call.
	Attempt int
	// IsTerminal marks the attempt whose outcome the caller actually got —
	// the served response or the final failure. Exactly one row per
	// logical call carries it.
	IsTerminal bool
	// AttemptReason names why THIS attempt ran, distinct from the first:
	// "provider_error" (a ladder rung failed and the walk moved to the
	// next), "schema_invalid" (a structured-output retry or escalation),
	// "budget_degrade" (the budget guardrail forced a demoted ladder on
	// what is still attempt 1). Empty for an ordinary first attempt.
	AttemptReason string
	// Kind distinguishes a chat-ladder attempt from an embed-lane call —
	// callKindCompletion or callKindEmbedding.
	Kind               string
	CorrelationID      *ids.UUID
	Task               Task
	Tier               Tier
	Provider           string
	ModelID            string
	RequestFingerprint string
	ContextScopes      []string
	ContextFingerprint string
	TokensIn           int
	TokensOut          int
	ReasoningTokens    int
	CachedTokens       int
	LatencyMS          int64
	CacheHit           bool
	// CacheOff records that the serving Router had the result cache
	// disabled (ai.WithoutResultCache — the cert lane and scripted tests
	// that must observe every repeat call, not a collapsed cache hit).
	CacheOff bool
	// ServedModel is the provider-reported identity of the model that
	// actually answered (model.Response.ServedModel), and ServedIdentitySource
	// names how that identity was obtained: "response" when the adapter reads
	// it off the wire response body, "echo" when the generic OpenAI-compatible
	// wire merely echoes back the requested model rather than confirming what
	// served it, "configured" when the provider reported no identity at all
	// and the trace falls back to the tier's configured binding.
	ServedModel          string
	ServedIdentitySource string
	Degraded             bool
	ErrorSentinel        string
	AgentRunID           *ids.UUID
	// ConfigHash points at the ai_call_config row describing the task
	// contract, routing config, and prompt version that produced this
	// attempt. Nil when the serving Router never installed a config
	// snapshot (a DB-less local router with no CallRecorder wired).
	ConfigHash *string
	// EstimatedCostMicroUSD is the attempt's estimated spend in micro-USD
	// (1e-6 USD); nil until a cost model prices the call.
	EstimatedCostMicroUSD *int64
	// Payload, when non-nil, carries the opt-in post-stripper content
	// (Layer 3). It is written to ai_call_payload in the SAME transaction
	// so the content row can never outlive its metadata row. Only the
	// terminal attempt of a logical call ever carries one — the router
	// strips it from any row a later attempt supersedes before flushing.
	Payload *Payload
}

// Payload is the Layer-3 opt-in content: the post-SecretStripper request
// (system + messages) and the model's response text. Special-category-
// adjacent — retention-aged and erasure-cascaded, never in audit_log.
type Payload struct {
	Request  json.RawMessage
	Response json.RawMessage
}

// ConfigSnapshot is one row of the ai_call_config dimension (spec §4): the
// task contract, routing config, and prompt version combination a batch of
// ai_call rows was produced under. Hash is the sha256 digest of the other
// four fields (computeConfigHash) — the append-only table's primary key,
// so the same combination collapses onto one row across every workspace.
type ConfigSnapshot struct {
	Hash              string
	TaskContractHash  string
	RoutingConfigHash string
	PromptVersion     string
	ProviderParams    json.RawMessage
}

// CallRecorder is what the router needs to trace calls; the interface
// keeps router unit tests off Postgres while CallMeter is the one real
// impl. Exported so the DB-less local router seam (ai.WithCallStore) can
// take a caller-supplied recorder (an in-memory store for a cert run or a
// test) without reaching into Postgres.
type CallRecorder interface {
	// Record writes every attempt of one logical call in ONE transaction —
	// the store must never observe a logical call half-written.
	Record(ctx context.Context, attempts []Call) error
	// EnsureConfig plants snap's row if no row for its Hash exists yet
	// (INSERT … ON CONFLICT DO NOTHING) — idempotent, safe to call once per
	// flush regardless of whether this combination was already seen.
	EnsureConfig(ctx context.Context, snap ConfigSnapshot) error
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

// Record writes every attempt's ai_call row — and, for whichever attempt
// carries a Payload (only ever the terminal one), the ai_call_payload
// row — in ONE workspace transaction, so a logical call is never observed
// half-written and a content row can never outlive its metadata row.
func (m *CallMeter) Record(ctx context.Context, attempts []Call) error {
	if len(attempts) == 0 {
		return nil
	}
	return database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		for _, c := range attempts {
			// kind and served_identity_source carry a CHECK constraint, not
			// just a SQL DEFAULT — the columns are always listed explicitly
			// below, so an unset Go zero-value would reach the constraint as
			// '""', not fall back to the schema's default. Mirror the
			// schema's own defaults here so a caller that does not care
			// about these fields (most non-router test fixtures) still
			// writes a valid row.
			kind := c.Kind
			if kind == "" {
				kind = callKindCompletion
			}
			servedSource := c.ServedIdentitySource
			if servedSource == "" {
				servedSource = servedIdentitySourceConfigured
			}
			contextScopes := c.ContextScopes
			if contextScopes == nil {
				contextScopes = []string{}
			}
			var callID ids.UUID
			err := tx.QueryRow(
				ctx, `
				INSERT INTO ai_call (
				  workspace_id, correlation_id, task, tier, provider, model_id,
				  request_fingerprint, context_scopes, context_fingerprint,
				  tokens_in, tokens_out, reasoning_tokens,
				  cached_tokens, latency_ms, cache_hit, degraded, error_sentinel, agent_run_id,
				  logical_call_id, attempt, is_terminal, attempt_reason, kind,
				  served_model, served_identity_source, cache_off, config_hash, estimated_cost_microusd)
				VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
				  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''),$17,
				  $18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
				RETURNING id`,
				c.CorrelationID, string(c.Task), string(c.Tier), c.Provider, c.ModelID,
				c.RequestFingerprint, contextScopes, c.ContextFingerprint,
				c.TokensIn, c.TokensOut, c.ReasoningTokens,
				c.CachedTokens, c.LatencyMS, c.CacheHit, c.Degraded, c.ErrorSentinel, c.AgentRunID,
				c.LogicalCallID, c.Attempt, c.IsTerminal, c.AttemptReason, kind,
				c.ServedModel, servedSource, c.CacheOff, c.ConfigHash, c.EstimatedCostMicroUSD,
			).Scan(&callID)
			if err != nil {
				return fmt.Errorf("ai: recording call: %w", err)
			}
			if c.Payload == nil {
				continue
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO ai_call_payload (workspace_id, ai_call_id, request_payload, response_payload)
				VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)`,
				callID, c.Payload.Request, c.Payload.Response)
			if err != nil {
				return fmt.Errorf("ai: recording call payload: %w", err)
			}
		}
		return nil
	})
}

// EnsureConfig plants snap's row in ai_call_config if it does not already
// exist.
func (m *CallMeter) EnsureConfig(ctx context.Context, snap ConfigSnapshot) error {
	// rls-exempt: ai_call_config is a global config-snapshot dimension (spec §4) — no workspace_id, no RLS policy, so this write must not ride the per-workspace GUC transaction.
	_, err := m.pool.Exec(ctx, `
		INSERT INTO ai_call_config (hash, task_contract_hash, routing_config_hash, prompt_version, provider_params)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (hash) DO NOTHING`,
		snap.Hash, snap.TaskContractHash, snap.RoutingConfigHash, snap.PromptVersion, []byte(snap.ProviderParams))
	if err != nil {
		return fmt.Errorf("ai: ensuring config snapshot: %w", err)
	}
	return nil
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
	case errors.Is(err, ErrBudgetDeferred):
		return "budget_deferred"
	case errors.Is(err, errMeteringFailed):
		return "metering_failed"
	default:
		return "provider_error"
	}
}
