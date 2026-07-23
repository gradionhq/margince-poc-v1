// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
	// ExpectedPriorRate is the rate in force (as of the staging day) the diff
	// was computed against; empty = no rate was in force. The apply effect
	// re-reads and refuses on mismatch (ErrVersionSkew) so an old proposal can
	// never overwrite a newer manual or approved rate.
	ExpectedPriorRate string `json:"expected_prior_rate,omitempty"`
}

// aiModelRatePrior carries the four per-MTok USD buckets in force (as of the
// staging day) a model-price diff was computed against; the apply effect
// re-reads and must match. Absent = the model was unpriced when diffed.
type aiModelRatePrior struct {
	InputUsd      string `json:"input_per_mtok"`
	OutputUsd     string `json:"output_per_mtok"`
	CacheReadUsd  string `json:"cache_read_per_mtok"`
	CacheWriteUsd string `json:"cache_write_per_mtok"`
}

type aiModelRateProposal struct {
	Provider      string            `json:"provider"`
	ModelID       string            `json:"model_id"`
	InputUsd      string            `json:"input_per_mtok"`
	OutputUsd     string            `json:"output_per_mtok"`
	CacheReadUsd  string            `json:"cache_read_per_mtok"`
	CacheWriteUsd string            `json:"cache_write_per_mtok"`
	ExpectedPrior *aiModelRatePrior `json:"expected_prior,omitempty"`
}

// sameRate reports numeric equality of two decimal strings — numeric(20,10)
// text and a source's text may spell one value at different scales.
func sameRate(a, b string) bool {
	ra, okA := new(big.Rat).SetString(a)
	rb, okB := new(big.Rat).SetString(b)
	return okA && okB && ra.Cmp(rb) == 0
}

// stageRateProposal marshals a proposal, computes its identity-bearing diff
// hash (sha256 over the payload, per the scrape.go shape), and stages it under
// JoinPending with the sheet row's logical identity — the atomic
// advisory-locked path that collapses an identical live proposal to a no-op
// AND withdraws a stale pending diff for the same identity (two refreshes
// fetching different values must not leave competing proposals whose late
// approval restores the older value).
//
//craft:ignore naked-any payload is any JSON-marshalable proposal struct (fx or model); the concrete type rides through json.Marshal
func stageRateProposal(ctx context.Context, svc *approvals.Service, kind, targetType string, ws ids.UUID, payload any, identity json.RawMessage, summary string) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("compose: marshal %s: %w", kind, err)
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	_, err = svc.Stage(ctx, approvals.StageInput{
		Kind: kind, ProposedChange: raw, DiffHash: hash,
		TargetType: targetType, TargetID: ws, Summary: summary,
		JoinPending: true, Identity: identity,
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
			// The diff was computed against the rate then in force; if the sheet
			// moved since (manual write, competing approval), applying would
			// silently restore a stale value — refuse and roll back instead. The
			// decision itself stays on record; the remedy is a fresh refresh.
			prior, found, err := store.EffectiveFxRateInTx(execCtx, tx, p.FromCurrency)
			if err != nil {
				return err
			}
			if err := fxPriorMatches(p, prior, found); err != nil {
				return err
			}
			_, err = store.SetFxRateInTx(execCtx, tx, deals.SetFxRateInput{
				FromCurrency: p.FromCurrency, Rate: p.Rate,
			})
			return err
		})
	}
}

// fxPriorMatches enforces the proposal's precondition: the rate in force now
// must be exactly the one the diff was computed against (numerically — the
// sheet stores scale-10 text). An empty ExpectedPriorRate asserts "none was
// in force", which is also how a payload staged before the precondition
// existed reads — such a proposal fails closed onto a re-diff.
func fxPriorMatches(p fxRateProposal, prior string, found bool) error {
	switch {
	case !found && p.ExpectedPriorRate == "":
		return nil
	case found && p.ExpectedPriorRate != "" && sameRate(prior, p.ExpectedPriorRate):
		return nil
	case !found:
		return fmt.Errorf("the %s rate the proposal was diffed against is no longer in force — re-run the refresh: %w",
			p.FromCurrency, apperrors.ErrVersionSkew)
	default:
		return fmt.Errorf("the %s rate changed since the proposal was diffed (now %s) — re-run the refresh: %w",
			p.FromCurrency, prior, apperrors.ErrVersionSkew)
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
			// Same precondition as the fx effect: the price in force must still
			// be the one the diff was computed against, or applying restores a
			// stale value — refuse and roll back, keep the decision on record.
			cur, err := rates.EffectiveModelRateInTx(execCtx, tx, p.Provider, p.ModelID)
			if err != nil {
				return err
			}
			if err := modelPriorMatches(p, cur); err != nil {
				return err
			}
			_, err = rates.SetModelRateInTx(execCtx, tx, ai.SetModelRateInput{
				Provider: p.Provider, ModelID: p.ModelID,
				InputUsd: p.InputUsd, OutputUsd: p.OutputUsd,
				CacheReadUsd: p.CacheReadUsd, CacheWriteUsd: p.CacheWriteUsd,
			})
			return err
		})
	}
}

// modelPriorMatches enforces the proposal's precondition against the price in
// force now, comparing in µUSD (the sheet's storage unit) so wire-scale
// differences cannot false-skew. A nil ExpectedPrior asserts "unpriced" —
// also how a payload staged before the precondition existed reads, so such a
// proposal fails closed onto a re-diff once the model is priced.
func modelPriorMatches(p aiModelRateProposal, cur *ai.ModelRate) error {
	moved := fmt.Errorf("the %s/%s price changed since the proposal was diffed — re-run the refresh: %w",
		p.Provider, p.ModelID, apperrors.ErrVersionSkew)
	if p.ExpectedPrior == nil {
		if cur != nil {
			return moved
		}
		return nil
	}
	if cur == nil {
		return moved
	}
	in, e1 := ai.UsdPerMTokToMicroUSD("input_per_mtok", p.ExpectedPrior.InputUsd)
	out, e2 := ai.UsdPerMTokToMicroUSD("output_per_mtok", p.ExpectedPrior.OutputUsd)
	cr, e3 := ai.UsdPerMTokToMicroUSD("cache_read_per_mtok", p.ExpectedPrior.CacheReadUsd)
	cw, e4 := ai.UsdPerMTokToMicroUSD("cache_write_per_mtok", p.ExpectedPrior.CacheWriteUsd)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return moved // an unparseable expected prior can never match — fail closed onto a re-diff
	}
	if in != cur.InputPerMTokMicroUSD || out != cur.OutputPerMTokMicroUSD ||
		cr != cur.CacheReadPerMTokMicroUSD || cw != cur.CacheWritePerMTokMicroUSD {
		return moved
	}
	return nil
}
