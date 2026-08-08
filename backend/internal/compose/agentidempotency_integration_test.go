// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The tool surface's retry key against the REAL claim table: the lifecycle, and
// the three scopes that decide whether two calls are talking about one key.
//
// It exercises the adapter rather than a mock of it, because everything worth
// proving here is a property of the row — the insert-first race, the digest
// comparison, and the (workspace, principal, key, endpoint) primary key that
// keeps one agent's key out of another's way. A fake would agree with whatever
// the adapter believes.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// agentCtxAs binds an agent principal with a chosen actor id, which is what
// scopes a claim: a real passport call carries "agent:<passport_id>".
func agentCtxAs(e *integration.Env, actorID string) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: actorID, SeatType: principal.SeatFull,
	})
}

func claimState(ctx context.Context, t *testing.T, claims agents.Idempotency, tool, key, digest string) agents.Claim {
	t.Helper()
	claim, err := claims.Claim(ctx, tool, key, digest)
	if err != nil {
		t.Fatalf("claiming %s/%s: %v", tool, key, err)
	}
	return claim
}

func TestAToolsRetryKeyRunsItsCallOnceAndReplaysTheResult(t *testing.T) {
	e := integration.Setup(t)
	claims := toolIdempotency(e.Pool)
	ctx := agentCtxAs(e, "agent:"+ids.NewV7().String())
	recorded := json.RawMessage(`{"schema_version":"1.0.0","evidence":[],"data":{"sent":true}}`)

	if first := claimState(ctx, t, claims, "send_email", "k-1", "digest-a"); first.State != agents.ClaimFresh {
		t.Fatalf("the first claim = %v, want fresh", first.State)
	}
	// Before the first attempt settles, a concurrent retry must not run: the
	// claim row is written insert-first precisely so the loser sees it.
	if inFlight := claimState(ctx, t, claims, "send_email", "k-1", "digest-a"); inFlight.State != agents.ClaimInFlight {
		t.Fatalf("a retry mid-flight = %v, want in-flight", inFlight.State)
	}
	if err := claims.Settle(ctx, "send_email", "k-1", recorded); err != nil {
		t.Fatalf("settle: %v", err)
	}

	replay := claimState(ctx, t, claims, "send_email", "k-1", "digest-a")
	if replay.State != agents.ClaimReplay {
		t.Fatalf("the settled key = %v, want replay", replay.State)
	}
	if string(replay.Result) != string(recorded) {
		t.Fatalf("replayed %s, recorded %s", replay.Result, recorded)
	}
}

func TestAToolsRetryKeyRefusesADifferentCall(t *testing.T) {
	e := integration.Setup(t)
	claims := toolIdempotency(e.Pool)
	ctx := agentCtxAs(e, "agent:"+ids.NewV7().String())

	if first := claimState(ctx, t, claims, "update_record", "k-2", "digest-a"); first.State != agents.ClaimFresh {
		t.Fatalf("the first claim = %v, want fresh", first.State)
	}
	if err := claims.Settle(ctx, "update_record", "k-2", json.RawMessage(`{"data":{}}`)); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// Same key, different arguments. Replaying the first result here would
	// answer a call the caller never made; running would spend a key that is
	// already accounted for.
	mismatch := claimState(ctx, t, claims, "update_record", "k-2", "digest-b")
	if mismatch.State != agents.ClaimMismatch {
		t.Fatalf("a changed payload under one key = %v, want mismatch", mismatch.State)
	}
	if mismatch.Result != nil {
		t.Fatalf("the refused call was handed the original result: %s", mismatch.Result)
	}
}

// A failed attempt owes the key back: its retry is the same call, and the 🟡
// loop depends on it — a staged refusal releases, the human approves, and the
// retry executes under the key it already used.
func TestAReleasedKeyIsClaimableAgain(t *testing.T) {
	e := integration.Setup(t)
	claims := toolIdempotency(e.Pool)
	ctx := agentCtxAs(e, "agent:"+ids.NewV7().String())

	if first := claimState(ctx, t, claims, "archive_record", "k-3", "digest-a"); first.State != agents.ClaimFresh {
		t.Fatalf("the first claim = %v, want fresh", first.State)
	}
	if err := claims.Release(ctx, "archive_record", "k-3"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if again := claimState(ctx, t, claims, "archive_record", "k-3", "digest-a"); again.State != agents.ClaimFresh {
		t.Fatalf("the released key = %v, want fresh", again.State)
	}
}

// A settled claim is a result somebody may still replay. Releasing one would
// turn a completed call into one that executes a second time — the exact
// failure the key exists to prevent.
func TestReleasingASettledKeyDoesNotDiscardItsResult(t *testing.T) {
	e := integration.Setup(t)
	claims := toolIdempotency(e.Pool)
	ctx := agentCtxAs(e, "agent:"+ids.NewV7().String())
	recorded := json.RawMessage(`{"schema_version":"1.0.0","evidence":[],"data":{"sent":true}}`)

	claimState(ctx, t, claims, "send_email", "k-4", "digest-a")
	if err := claims.Settle(ctx, "send_email", "k-4", recorded); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if err := claims.Release(ctx, "send_email", "k-4"); err != nil {
		t.Fatalf("release: %v", err)
	}
	replay := claimState(ctx, t, claims, "send_email", "k-4", "digest-a")
	if replay.State != agents.ClaimReplay || string(replay.Result) != string(recorded) {
		t.Fatalf("after releasing a settled key: %v / %s — the recorded result must survive", replay.State, replay.Result)
	}
}

// The three scopes a key lives inside. Each of these pairs would be one claim
// if the corresponding column were dropped from the key, and each collision
// would hand one caller another's recorded answer.
func TestOneKeyIsThreeDifferentClaims(t *testing.T) {
	e := integration.Setup(t)
	claims := toolIdempotency(e.Pool)
	const key = "shared-key"

	agentA := agentCtxAs(e, "agent:"+ids.NewV7().String())
	agentB := agentCtxAs(e, "agent:"+ids.NewV7().String())

	if first := claimState(agentA, t, claims, "send_email", key, "digest-a"); first.State != agents.ClaimFresh {
		t.Fatalf("agent A's claim = %v, want fresh", first.State)
	}
	// A second passport. Two agents acting for one human must not be able to
	// spend — or replay — each other's keys.
	if other := claimState(agentB, t, claims, "send_email", key, "digest-a"); other.State != agents.ClaimFresh {
		t.Fatalf("agent B's claim of the same key = %v, want fresh", other.State)
	}
	// A second tool. `create_record` under one key says nothing about
	// `send_email` under it, exactly as two REST paths do not collide.
	if otherTool := claimState(agentA, t, claims, "create_record", key, "digest-a"); otherTool.State != agents.ClaimFresh {
		t.Fatalf("the same key on another tool = %v, want fresh", otherTool.State)
	}
	// And the REST door. The endpoint spelling is what keeps a tool's key out of
	// a request path's; this proves the two really do write different rows.
	actor, _ := principal.Actor(agentA)
	outcome, _, err := claimKey(agentA, e.Pool, actor.ID, key, "POST /v1/quotas", "digest-a")
	if err != nil {
		t.Fatalf("the REST claim failed: %v", err)
	}
	if outcome != claimFresh {
		t.Fatalf("a REST call collided with a tool's key: %v", outcome)
	}
}
