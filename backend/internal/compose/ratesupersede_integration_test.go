// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Identity-keyed staging over real Postgres: a fresher diff for one logical
// identity supersedes the stale pending proposal (forced expiry, audited, no
// longer decidable), an identical re-stage still joins, and other identities
// are untouched.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func stageFxIdentity(ctx context.Context, t *testing.T, svc *approvals.Service, ws ids.UUID, cur, rate, prior string) ids.ApprovalID {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"from_currency": cur, "rate": rate, "expected_prior_rate": prior})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	identity, err := json.Marshal(map[string]string{"from_currency": cur})
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	digest := sha256.Sum256(raw)
	id, err := svc.Stage(ctx, approvals.StageInput{
		Kind: fxRateProposalKind, ProposedChange: raw, DiffHash: hex.EncodeToString(digest[:]),
		TargetType: fxRateTargetType, TargetID: ws, Summary: cur,
		JoinPending: true, Identity: identity,
	})
	if err != nil {
		t.Fatalf("stage %s@%s: %v", cur, rate, err)
	}
	return id
}

func TestIdentityStagingSupersedesStalePending(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	svc := rateSvc(e)

	stale := stageFxIdentity(ctx, t, svc, e.WS, "GBP", "1.1", "1.0")
	other := stageFxIdentity(ctx, t, svc, e.WS, "USD", "0.9", "0.8")
	fresh := stageFxIdentity(ctx, t, svc, e.WS, "GBP", "1.2", "1.0")
	if stale == fresh {
		t.Fatal("distinct diffs must stage distinct approvals")
	}

	live := func(id ids.ApprovalID) int {
		return e.WsCount(t,
			`SELECT count(*) FROM approval WHERE id = $1 AND status = 'pending' AND expires_at > now()`, id)
	}
	if live(stale) != 0 {
		t.Fatal("stale GBP proposal still live, want superseded (forced expiry)")
	}
	if live(fresh) != 1 || live(other) != 1 {
		t.Fatalf("fresh GBP live=%d, USD live=%d, want 1 and 1", live(fresh), live(other))
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type='approval' AND entity_id=$1 AND evidence->>'superseded_by'=$2`,
		stale.UUID, fresh.String()); n != 1 {
		t.Fatalf("superseded audit rows = %d, want 1", n)
	}
	// The withdrawn authority is dead: deciding it reads as already-expired.
	if _, err := svc.Decide(ctx, stale, true, nil); err == nil {
		t.Fatal("deciding a superseded proposal succeeded, want expired rejection")
	}
	// An identical re-stage still joins the survivor instead of duplicating it.
	if again := stageFxIdentity(ctx, t, svc, e.WS, "GBP", "1.2", "1.0"); again != fresh {
		t.Fatalf("identical re-stage created %s, want join of %s", again, fresh)
	}
}
