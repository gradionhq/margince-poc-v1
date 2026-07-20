// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// CallReadStore serves the admin-only AI trace without loading payload
// content into list responses.
type CallReadStore struct{ pool *pgxpool.Pool }

func NewCallReadStore(pool *pgxpool.Pool) *CallReadStore { return &CallReadStore{pool: pool} }

type CallSummary struct {
	ID              ids.UUID
	OccurredAt      time.Time
	Task            string
	Tier            string
	Provider        string
	ModelID         string
	ServedModel     string
	Attempt         int
	TokensIn        int64
	TokensOut       int64
	ReasoningTokens int64
	CachedTokens    int64
	LatencyMS       int64
	CacheHit        bool
	Degraded        bool
	ErrorSentinel   *string
	HasPayload      bool
}

type CallAttempt struct {
	Attempt       int
	IsTerminal    bool
	AttemptReason string
	ErrorSentinel *string
	TokensIn      int64
	TokensOut     int64
	LatencyMS     int64
	OccurredAt    time.Time
}

type CallDetail struct {
	CallSummary
	CorrelationID        *ids.UUID
	AgentRunID           *ids.UUID
	ServedIdentitySource string
	ConfigHash           *string
	ContextScopes        []string
	ContextFingerprint   string
	Attempts             []CallAttempt
	Payload              *Payload
}

type CallPage struct {
	Items      []CallSummary
	NextCursor string
	HasMore    bool
}

// The payload existence check keeps list reads independent of captured
// content size. Its alias stays distinct from the detail join alias.
const callSummaryColumns = `c.id, c.occurred_at, c.task, c.tier, c.provider, c.model_id,
	c.served_model, c.attempt, c.tokens_in, c.tokens_out, c.reasoning_tokens,
	c.cached_tokens, c.latency_ms, c.cache_hit, c.degraded, c.error_sentinel,
	EXISTS (SELECT 1 FROM ai_call_payload pp WHERE pp.ai_call_id = c.id)`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCallSummary(row rowScanner) (CallSummary, error) {
	var summary CallSummary
	err := row.Scan(&summary.ID, &summary.OccurredAt, &summary.Task, &summary.Tier,
		&summary.Provider, &summary.ModelID, &summary.ServedModel, &summary.Attempt,
		&summary.TokensIn, &summary.TokensOut, &summary.ReasoningTokens,
		&summary.CachedTokens, &summary.LatencyMS, &summary.CacheHit, &summary.Degraded,
		&summary.ErrorSentinel, &summary.HasPayload)
	return summary, err
}

// ListCalls returns terminal attempts newest-first. Retry siblings remain
// available through the detail ladder, not as duplicate list entries.
func (s *CallReadStore) ListCalls(
	ctx context.Context,
	cursor *string,
	limit *int,
	task *string,
) (CallPage, error) {
	if err := auth.Require(ctx, "automation", principal.ActionUpdate); err != nil {
		return CallPage{}, err
	}
	n := storekit.ClampLimit(limit)
	where := "c.is_terminal"
	args := []any{}
	addArg := func(value any) int {
		args = append(args, value)
		return len(args)
	}
	if task != nil && *task != "" {
		where += fmt.Sprintf(" AND c.task = $%d", addArg(*task))
	}
	if cursor != nil && *cursor != "" {
		decoded, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return CallPage{}, err
		}
		where += fmt.Sprintf(
			" AND (c.occurred_at, c.id) < ($%d, $%d)",
			addArg(decoded.CreatedAt), addArg(decoded.ID),
		)
	}

	var page CallPage
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, storekit.SQLf(
			`SELECT %s FROM ai_call c WHERE %s ORDER BY c.occurred_at DESC, c.id DESC LIMIT %d`,
			callSummaryColumns, where, n+1,
		), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanCallSummary(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return CallPage{}, err
	}
	if len(page.Items) > n {
		page.Items = page.Items[:n]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = storekit.EncodeCursor(last.OccurredAt, last.ID)
		page.HasMore = true
	}
	return page, nil
}

// GetCall returns a terminal call and its complete attempt ladder. RLS
// makes a missing and a foreign-workspace identifier indistinguishable.
func (s *CallReadStore) GetCall(ctx context.Context, id ids.UUID) (CallDetail, error) {
	if err := auth.Require(ctx, "automation", principal.ActionUpdate); err != nil {
		return CallDetail{}, err
	}
	var detail CallDetail
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s, c.correlation_id, c.agent_run_id, c.served_identity_source,
				c.config_hash, c.context_scopes, c.context_fingerprint, c.logical_call_id,
				p.request_payload, p.response_payload
			 FROM ai_call c
			 LEFT JOIN ai_call_payload p ON p.ai_call_id = c.id
			 WHERE c.is_terminal AND c.id = $1`, callSummaryColumns,
		), id)
		var logicalID ids.UUID
		var requestPayload, responsePayload []byte
		err := row.Scan(&detail.ID, &detail.OccurredAt, &detail.Task, &detail.Tier,
			&detail.Provider, &detail.ModelID, &detail.ServedModel, &detail.Attempt,
			&detail.TokensIn, &detail.TokensOut, &detail.ReasoningTokens,
			&detail.CachedTokens, &detail.LatencyMS, &detail.CacheHit, &detail.Degraded,
			&detail.ErrorSentinel, &detail.HasPayload, &detail.CorrelationID,
			&detail.AgentRunID, &detail.ServedIdentitySource, &detail.ConfigHash,
			&detail.ContextScopes, &detail.ContextFingerprint, &logicalID,
			&requestPayload, &responsePayload)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if requestPayload != nil && responsePayload != nil {
			detail.Payload = &Payload{Request: requestPayload, Response: responsePayload}
		}

		rows, err := tx.Query(ctx, `
			SELECT attempt, is_terminal, attempt_reason, error_sentinel,
				tokens_in, tokens_out, latency_ms, occurred_at
			FROM ai_call WHERE logical_call_id = $1 ORDER BY attempt ASC`, logicalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var attempt CallAttempt
			if err := rows.Scan(&attempt.Attempt, &attempt.IsTerminal, &attempt.AttemptReason,
				&attempt.ErrorSentinel, &attempt.TokensIn, &attempt.TokensOut,
				&attempt.LatencyMS, &attempt.OccurredAt); err != nil {
				return err
			}
			detail.Attempts = append(detail.Attempts, attempt)
		}
		return rows.Err()
	})
	if err != nil {
		return CallDetail{}, err
	}
	return detail, nil
}
