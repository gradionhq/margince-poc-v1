// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The rate-refresh proposal effects over real Postgres: a staged proposal is
// decidable by an admin (decisionGrants + targetVisible), approving it applies
// the effective-dated row through the Phase-1 store method (fx as-is, model
// USD->µUSD), rejecting writes nothing, and edit-before-approve applies the
// edited value (payloads are strings, so the inbox can edit them).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func rateSvc(e *integration.Env) *approvals.Service {
	svc := approvals.NewService(e.Pool)
	svc.WithEffect(fxRateProposalKind, fxRateAcceptEffect(svc, deals.NewStore(e.Pool)))
	svc.WithEffect(aiModelRateProposalKind, aiModelRateAcceptEffect(svc, ai.NewRateStore(e.Pool)))
	return svc
}

//craft:ignore naked-any payload is any JSON-marshalable proposal struct; a test helper mirroring stageRateProposal
func stageProposal(ctx context.Context, t *testing.T, svc *approvals.Service, kind, targetType string, ws ids.UUID, payload any, summary string) ids.ApprovalID {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	digest := sha256.Sum256(raw)
	id, err := svc.Stage(ctx, approvals.StageInput{
		Kind: kind, ProposedChange: raw, DiffHash: hex.EncodeToString(digest[:]),
		TargetType: targetType, TargetID: ws, Summary: summary,
	})
	if err != nil {
		t.Fatalf("stage %s: %v", kind, err)
	}
	return id
}

func TestFxRateProposalApprovalAppliesRow(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	svc := rateSvc(e)

	id := stageProposal(ctx, t, svc, fxRateProposalKind, fxRateTargetType, e.WS,
		map[string]string{"from_currency": "USD", "rate": "0.9"}, "USD → EUR 0.9")
	if _, err := svc.Decide(ctx, id, true, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM fx_rate WHERE from_currency='USD' AND rate=0.9`); n != 1 {
		t.Fatalf("USD@0.9 rows = %d, want 1 (effect applied via SetFxRate)", n)
	}
}

func TestModelRateProposalApprovalConvertsAndApplies(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	svc := rateSvc(e)

	id := stageProposal(ctx, t, svc, aiModelRateProposalKind, aiModelRateTargetType, e.WS,
		map[string]string{
			"provider": "anthropic", "model_id": "m",
			"input_per_mtok": "5", "output_per_mtok": "25",
			"cache_read_per_mtok": "0", "cache_write_per_mtok": "0",
		}, "anthropic/m input 5")
	if _, err := svc.Decide(ctx, id, true, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// USD/MTok 5 -> µUSD 5_000_000.
	if n := e.WsCount(t, `SELECT count(*) FROM ai_model_rate WHERE provider='anthropic' AND model_id='m' AND input_per_mtok_microusd=5000000`); n != 1 {
		t.Fatalf("model row = %d, want 1 with 5_000_000 µUSD", n)
	}
}

func TestRateProposalRejectWritesNothing(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	svc := rateSvc(e)

	id := stageProposal(ctx, t, svc, fxRateProposalKind, fxRateTargetType, e.WS,
		map[string]string{"from_currency": "GBP", "rate": "1.1"}, "GBP → EUR 1.1")
	if _, err := svc.Decide(ctx, id, false, nil); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM fx_rate WHERE from_currency='GBP'`); n != 0 {
		t.Fatalf("GBP rows = %d, want 0 (reject writes nothing)", n)
	}
}

func TestFxRateProposalEditBeforeApprove(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	svc := rateSvc(e)

	id := stageProposal(ctx, t, svc, fxRateProposalKind, fxRateTargetType, e.WS,
		map[string]string{"from_currency": "CHF", "rate": "1.0"}, "CHF → EUR 1.0")
	edited, err := json.Marshal(map[string]string{"from_currency": "CHF", "rate": "1.5"})
	if err != nil {
		t.Fatalf("marshal edited: %v", err)
	}
	if _, err := svc.DecideEdited(ctx, id, edited); err != nil {
		t.Fatalf("decide edited: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM fx_rate WHERE from_currency='CHF' AND rate=1.5`); n != 1 {
		t.Fatalf("CHF@1.5 rows = %d, want 1 (edit-before-approve applied)", n)
	}
}

// The staged diff was computed against a prior rate; if the sheet moved
// since (manual write, competing approval), applying would restore a stale
// value — the effect must refuse with version skew, leave the approval
// approved-unconsumed, and write nothing.
func TestFxRateProposalApplyRefusesWhenPriorMoved(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	svc := rateSvc(e)
	store := deals.NewStore(e.Pool)

	if _, err := store.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "GBP", Rate: "1.0"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := stageProposal(ctx, t, svc, fxRateProposalKind, fxRateTargetType, e.WS,
		map[string]string{"from_currency": "GBP", "rate": "1.2", "expected_prior_rate": "1.0"}, "GBP")
	// The sheet moves after the diff was staged.
	if _, err := store.SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "GBP", Rate: "1.1"}); err != nil {
		t.Fatalf("move: %v", err)
	}

	_, err := svc.Decide(ctx, id, true, nil)
	if !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("approve err = %v, want ErrVersionSkew", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM fx_rate WHERE from_currency='GBP' AND rate=1.2`); n != 0 {
		t.Fatal("stale proposal applied despite moved prior")
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM approval WHERE id=$1 AND status='approved' AND consumed_at IS NULL`, id); n != 1 {
		t.Fatal("approval must stay approved-unconsumed after a refused apply")
	}
}

// A proposal diffed when NO rate was in force (expected_prior_rate empty —
// also the shape of any pre-existing pending payload without the field)
// must refuse once a rate exists: the world it diffed against is gone.
func TestFxRateProposalApplyRefusesWhenPriorAppeared(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	svc := rateSvc(e)

	id := stageProposal(ctx, t, svc, fxRateProposalKind, fxRateTargetType, e.WS,
		map[string]string{"from_currency": "NOK", "rate": "0.09"}, "NOK")
	if _, err := deals.NewStore(e.Pool).SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "NOK", Rate: "0.088"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Decide(ctx, id, true, nil); !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("approve err = %v, want ErrVersionSkew", err)
	}
}

// The precondition is scale-blind: "1.0" diffed against a stored
// numeric(20,10) ("1.0000000000") still matches, and the apply proceeds.
func TestFxRateProposalApplyMatchingPriorApplies(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	svc := rateSvc(e)

	if _, err := deals.NewStore(e.Pool).SetFxRate(ctx, deals.SetFxRateInput{FromCurrency: "DKK", Rate: "0.134"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := stageProposal(ctx, t, svc, fxRateProposalKind, fxRateTargetType, e.WS,
		map[string]string{"from_currency": "DKK", "rate": "0.135", "expected_prior_rate": "0.134"}, "DKK")
	if _, err := svc.Decide(ctx, id, true, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM fx_rate WHERE from_currency='DKK' AND rate=0.135`); n != 1 {
		t.Fatal("matching-prior proposal must apply")
	}
}
