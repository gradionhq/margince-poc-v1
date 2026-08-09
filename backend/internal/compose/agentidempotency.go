// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Retry safety for the governed tool surface, over the claim the REST door
// already uses.
//
// The whole adapter is the translations the tool surface cannot make for
// itself: which principal holds the key, and what a claim outcome means to a
// caller that speaks tool results rather than HTTP. The claim transaction, the
// 24h window, the digest comparison and the retention sweep are
// idempotency.go's, unchanged and shared — a second implementation would be a
// second answer to "is this the same call", and the two would drift the first
// time either window moved.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// mcpClaimEndpoint namespaces a tool's claims inside the shared table. The REST
// door's endpoint is "METHOD /concrete/path", so no tool name can collide with
// one — and a key spent on `send_email` is untouched by the same key spent on
// `create_record`, exactly as two REST paths are.
func mcpClaimEndpoint(tool string) string { return "MCP " + tool }

// mcpClaimContentType is what a replayed tool result is stored as. The column
// exists so a REST replay repeats its original media type; a tool result is
// always this one, and recording it keeps the row readable as what it is rather
// than defaulting into a claim nobody made.
const mcpClaimContentType = "application/json"

// The two statuses a tool's claim records. A tools/call has no HTTP status of
// its own; these are how the shared row distinguishes its three settled states
// — success, ran-and-failed, still in flight (NULL) — using the column
// resolveExistingClaim already reads.
const (
	mcpClaimSucceeded = 200
	mcpClaimFailed    = 500
)

// agentClaims implements the tool surface's claim seam.
type agentClaims struct{ pool *pgxpool.Pool }

var _ agents.Idempotency = agentClaims{}

// toolIdempotency is the claim store every composed tool surface installs.
func toolIdempotency(pool *pgxpool.Pool) agentClaims { return agentClaims{pool: pool} }

// claimHolder answers who a key belongs to.
//
// The ACTOR, which for a passport call is "agent:<passport_id>" — so a key is
// scoped to the passport that spent it. Two agents acting for one human cannot
// collide on a key, and neither can an agent and the human themselves.
//
// An EMPTY id is refused as hard as a missing principal, because it is the same
// failure wearing a valid-looking value: principal.Actor answers ok for a
// zero-value Principal, and an id-less claim would write `principal_id = ”` —
// one row every such caller in the workspace shares, and one caller's recorded
// result replayable by another.
func claimHolder(ctx context.Context, verb string) (string, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.ID == "" {
		return "", fmt.Errorf("compose: no principal %s a tools/call idempotency claim", verb)
	}
	return actor.ID, nil
}

// Claim takes the key for this call inside the caller's workspace transaction.
func (c agentClaims) Claim(ctx context.Context, tool, key, digest string) (agents.Claim, error) {
	holder, err := claimHolder(ctx, "claiming")
	if err != nil {
		return agents.Claim{}, err
	}
	outcome, stored, err := claimKey(ctx, c.pool, holder, key, mcpClaimEndpoint(tool), digest)
	if err != nil {
		return agents.Claim{}, err
	}
	switch outcome {
	case claimFresh:
		return agents.Claim{State: agents.ClaimFresh}, nil
	case claimInProgress:
		return agents.Claim{State: agents.ClaimInFlight}, nil
	case claimMismatch:
		return agents.Claim{State: agents.ClaimMismatch}, nil
	case claimReplay:
		return agents.Claim{
			State: agents.ClaimReplay, Result: json.RawMessage(stored.body), Records: stored.records,
		}, nil
	case claimFailed:
		return agents.Claim{State: agents.ClaimFailed, Reason: stored.body}, nil
	default:
		return agents.Claim{}, fmt.Errorf("compose: unknown idempotency claim outcome %d", outcome)
	}
}

// Settle records the sealed result, and what it cost the caller's read bound,
// so a repeat of the same key answers with it at the same price.
func (c agentClaims) Settle(ctx context.Context, tool, key string, result json.RawMessage, records int) error {
	holder, err := claimHolder(ctx, "settling")
	if err != nil {
		return err
	}
	return recordClaimOutcome(ctx, c.pool, holder, key, mcpClaimEndpoint(tool),
		mcpClaimSucceeded, string(result), mcpClaimContentType, records)
}

// Fail records that the tool ran under this key and produced no result. The
// reason is stored where a result would be, and a later attempt is told it
// rather than being allowed to run again — see agents.Idempotency for why a
// failed run is not a free key.
func (c agentClaims) Fail(ctx context.Context, tool, key, reason string) error {
	holder, err := claimHolder(ctx, "failing")
	if err != nil {
		return err
	}
	// No records: nothing was handed over, so there is nothing a replay of it
	// could cost — and it will never be replayed in any case.
	return recordClaimOutcome(ctx, c.pool, holder, key, mcpClaimEndpoint(tool),
		mcpClaimFailed, reason, mcpClaimContentType, 0)
}

// Release gives back a key whose call never ran.
func (c agentClaims) Release(ctx context.Context, tool, key string) error {
	holder, err := claimHolder(ctx, "releasing")
	if err != nil {
		return err
	}
	return releaseClaim(ctx, c.pool, holder, key, mcpClaimEndpoint(tool))
}
