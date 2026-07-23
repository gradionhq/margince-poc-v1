// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The rate-refresh proposal kinds and their apply-on-approval effects. A
// producer (Task 3/4) stages one proposal per changed value; on human
// approval the effect redeems and writes through the Phase-1 store method as
// a system principal on behalf of the deciding admin. The proposals target
// the workspace (config, no row scope) and carry the logical identity in the
// payload; the effect applies the rate effective TODAY (never a date pinned
// at staging time — a cross-midnight approval must not miss the past-date
// guard), so the payload carries no date.
const (
	fxRateProposalKind      = "fx_rate_proposal"
	aiModelRateProposalKind = "ai_model_rate_proposal"
	fxRateTargetType        = "fx_rate"
	aiModelRateTargetType   = "ai_model_rate"
)

type fxRateProposal struct {
	FromCurrency string `json:"from_currency"`
	Rate         string `json:"rate"`
}

type aiModelRateProposal struct {
	Provider      string `json:"provider"`
	ModelID       string `json:"model_id"`
	InputUsd      string `json:"input_per_mtok"`
	OutputUsd     string `json:"output_per_mtok"`
	CacheReadUsd  string `json:"cache_read_per_mtok"`
	CacheWriteUsd string `json:"cache_write_per_mtok"`
}

// stageRateProposal marshals a proposal, computes its identity-bearing diff
// hash (sha256 over the payload, per the scrape.go shape), and stages it under
// JoinPending — the atomic advisory-locked path that collapses an identical
// live proposal to a no-op. Two workers racing the same diff cannot both
// insert (the pre-check-then-stage window a plain HasPendingFor left open).
//
//craft:ignore naked-any payload is any JSON-marshalable proposal struct (fx or model); the concrete type rides through json.Marshal
func stageRateProposal(ctx context.Context, svc *approvals.Service, kind, targetType string, ws ids.UUID, payload any, summary string) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("compose: marshal %s: %w", kind, err)
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	_, err = svc.Stage(ctx, approvals.StageInput{
		Kind: kind, ProposedChange: raw, DiffHash: hash,
		TargetType: targetType, TargetID: ws, Summary: summary,
		JoinPending: true,
	})
	return err
}

// rateRefreshActor binds the system principal a rate-refresh effect applies
// under (bypasses auth.Require), on behalf of the deciding admin.
func rateRefreshActor(ctx context.Context) (context.Context, error) {
	decider, ok := principal.Actor(ctx)
	if !ok {
		return nil, fmt.Errorf("compose: rate refresh effect without a deciding principal")
	}
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "agent:rate-refresh",
		UserID: decider.UserID, OnBehalfOf: decider.UserID,
	}), nil
}

func fxRateAcceptEffect(svc *approvals.Service, store *deals.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		var p fxRateProposal
		if err := json.Unmarshal(proposedChange, &p); err != nil {
			return fmt.Errorf("compose: fx rate proposal payload: %w", err)
		}
		execCtx, err := rateRefreshActor(ctx)
		if err != nil {
			return err
		}
		// Redeem the authority object and apply the sheet write in ONE
		// transaction: a failed write leaves the approval unconsumed and the
		// job retryable, never permanently consumed with the sheet unchanged.
		// A zero EffectiveDate applies the rate effective today, derived inside
		// the store's write transaction (a cross-midnight approval must not
		// miss the past-date guard).
		return svc.RedeemAndApply(ctx, approvalID, fxRateProposalKind, diffHash, func(tx pgx.Tx) error {
			_, err := store.SetFxRateInTx(execCtx, tx, deals.SetFxRateInput{
				FromCurrency: p.FromCurrency, Rate: p.Rate,
			})
			return err
		})
	}
}

func aiModelRateAcceptEffect(svc *approvals.Service, rates *ai.RateStore) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		var p aiModelRateProposal
		if err := json.Unmarshal(proposedChange, &p); err != nil {
			return fmt.Errorf("compose: ai model rate proposal payload: %w", err)
		}
		execCtx, err := rateRefreshActor(ctx)
		if err != nil {
			return err
		}
		// Single-transaction redeem-and-apply (see fxRateAcceptEffect): a
		// failed write keeps the approval redeemable. Zero EffectiveDate ⇒
		// effective today, derived inside the store's write transaction.
		return svc.RedeemAndApply(ctx, approvalID, aiModelRateProposalKind, diffHash, func(tx pgx.Tx) error {
			_, err := rates.SetModelRateInTx(execCtx, tx, ai.SetModelRateInput{
				Provider: p.Provider, ModelID: p.ModelID,
				InputUsd: p.InputUsd, OutputUsd: p.OutputUsd,
				CacheReadUsd: p.CacheReadUsd, CacheWriteUsd: p.CacheWriteUsd,
			})
			return err
		})
	}
}
