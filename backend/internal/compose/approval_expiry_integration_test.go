// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The two time gates on withheld authority (ADR-0036): a pending staging
// dies at its expiry and can never be approved afterwards, and a human's
// yes dies at the redemption window — a stale decision no longer
// authorizes the replay. Both windows are properties of stored
// timestamps, so the tests move the timestamps through the owner
// connection instead of sleeping.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ownerConn opens the schema-owner connection tests use to shift
// timestamps the app role's RLS-bound path could never touch.
func ownerConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	return conn
}

// stageAdvance stages one advance_deal proposal and returns its id and
// the diff hash a redemption must repeat.
func stageAdvance(t *testing.T, svc *approvals.Service, e *authzEnv, deal ids.UUID) (ids.UUID, string) {
	t.Helper()
	diffHash := "h-" + ids.NewV7().String()
	id, err := svc.Stage(e.agentCtx(), approvals.StageInput{
		Kind: "advance_deal", ProposedChange: json.RawMessage(`{}`), DiffHash: diffHash,
		TargetType: "deal", TargetID: deal, Summary: "expiry test staging",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id, diffHash
}

// An expired pending staging is dead authority: it cannot be approved,
// rejected, or redeemed — a week-old agent intention must be re-proposed
// against fresh state, not green-lit from a stale inbox row.
func TestApprovalExpiryClosesTheDecisionGate(t *testing.T) {
	e := setupAuthz(t)
	owner := ownerConn(t)
	pipeline, open, _ := dealFixture(t, e)
	svc := approvals.NewService(e.pool)

	deal := e.seedDeal(t, "Mine", pipeline, open, &e.rep1)
	approvalID, diffHash := stageAdvance(t, svc, e, deal)

	if _, err := owner.Exec(context.Background(),
		`UPDATE approval SET expires_at = now() - interval '1 minute' WHERE id = $1`, approvalID); err != nil {
		t.Fatal(err)
	}

	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPerms)
	var decided *approvals.AlreadyDecidedError
	if _, err := svc.Decide(rep, approvalID, true, nil); !errors.As(err, &decided) || decided.Status != "expired" {
		t.Fatalf("approving an expired staging → %v, want AlreadyDecidedError{expired}", err)
	}
	if _, err := svc.Decide(rep, approvalID, false, strPtr("late no")); !errors.As(err, &decided) || decided.Status != "expired" {
		t.Fatalf("rejecting an expired staging → %v, want AlreadyDecidedError{expired}", err)
	}
	// Never approved, so redemption is asserting authority that does not
	// exist — refused as an invalid token, and the refusal names the
	// expired state, not a raw pending one.
	err := svc.Redeem(e.agentCtx(), approvalID, "advance_deal", diffHash)
	if !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("redeeming an expired pending staging → %v, want ErrApprovalTokenInvalid", err)
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("refusal reads %q, want it to name the expired state", err)
	}
}

// The approve→redeem window bounds a human's yes: a decision older than
// the redemption TTL no longer authorizes the replay, while a fresh
// decision does — exactly once.
func TestRedemptionWindowExpiresTheDecision(t *testing.T) {
	e := setupAuthz(t)
	owner := ownerConn(t)
	pipeline, open, _ := dealFixture(t, e)
	svc := approvals.NewService(e.pool)

	deal := e.seedDeal(t, "Mine", pipeline, open, &e.rep1)
	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPerms)

	staleID, staleHash := stageAdvance(t, svc, e, deal)
	freshID, freshHash := stageAdvance(t, svc, e, deal)
	for _, id := range []ids.UUID{staleID, freshID} {
		if _, err := svc.Decide(rep, id, true, nil); err != nil {
			t.Fatalf("approve: %v", err)
		}
	}

	// The stale decision predates the 15-minute redemption window.
	if _, err := owner.Exec(context.Background(),
		`UPDATE approval SET decided_at = now() - interval '16 minutes' WHERE id = $1`, staleID); err != nil {
		t.Fatal(err)
	}

	err := svc.Redeem(e.agentCtx(), staleID, "advance_deal", staleHash)
	if !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("redeeming past the redemption window → %v, want ErrApprovalTokenInvalid", err)
	}
	if !strings.Contains(err.Error(), "after decision") {
		t.Errorf("refusal reads %q, want it to name the redemption window", err)
	}

	// The window narrows time, it does not break the feature: a fresh
	// decision redeems — and only once.
	if err := svc.Redeem(e.agentCtx(), freshID, "advance_deal", freshHash); err != nil {
		t.Fatalf("redeeming a fresh decision → %v, want ok", err)
	}
	if err := svc.Redeem(e.agentCtx(), freshID, "advance_deal", freshHash); !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("second redemption → %v, want ErrApprovalTokenInvalid (single-use)", err)
	}
}
