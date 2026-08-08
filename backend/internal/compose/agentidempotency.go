// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Retry safety for the governed tool surface, over the claim the REST door
// already uses.
//
// The whole adapter is the two translations the tool surface cannot make for
// itself: which principal holds the key, and what a claim outcome means to a
// caller that speaks tool results rather than HTTP. The claim transaction, the
// 24h window, the digest comparison and the retention sweep are
// idempotency.go's, unchanged and shared — a second implementation would be a
// second answer to "is this the same call", and the two would drift the first
// time either window moved.

import (
	"context"
	"encoding/json"
	"errors"
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

// agentClaims implements the tool surface's claim seam.
type agentClaims struct{ pool *pgxpool.Pool }

var _ agents.Idempotency = agentClaims{}

// toolIdempotency is the claim store every composed tool surface installs.
func toolIdempotency(pool *pgxpool.Pool) agentClaims { return agentClaims{pool: pool} }

// Claim takes the key for this call inside the caller's workspace transaction.
//
// The principal is the ACTOR, which for a passport call is
// "agent:<passport_id>" — so a key is scoped to the passport that spent it. Two
// agents acting for one human cannot collide on a key, and neither can an agent
// and the human themselves.
func (c agentClaims) Claim(ctx context.Context, tool, key, digest string) (agents.Claim, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		// Unreachable behind the admission gate, which has already resolved the
		// caller. Named rather than defaulted: a claim scoped to nobody would be
		// a key every caller shares.
		return agents.Claim{}, errors.New("compose: no principal on a tools/call idempotency claim")
	}
	outcome, stored, err := claimKey(ctx, c.pool, actor.ID, key, mcpClaimEndpoint(tool), digest)
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
		return agents.Claim{State: agents.ClaimReplay, Result: json.RawMessage(stored.body)}, nil
	default:
		return agents.Claim{}, fmt.Errorf("compose: unknown idempotency claim outcome %d", outcome)
	}
}

// Settle records the sealed result so a repeat of the same key answers with it.
// The 200 is the claim table's "this attempt succeeded" marker rather than a
// status any tool caller sees — a tools/call has no HTTP status of its own, and
// the column is what settleClaim reads to tell a settled claim from a released
// one.
func (c agentClaims) Settle(ctx context.Context, tool, key string, result json.RawMessage) error {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("compose: no principal settling a tools/call idempotency claim")
	}
	return settleClaim(ctx, c.pool, actor.ID, key, mcpClaimEndpoint(tool), 200, string(result), mcpClaimContentType)
}

// Release gives the key back after a failed attempt.
func (c agentClaims) Release(ctx context.Context, tool, key string) error {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("compose: no principal releasing a tools/call idempotency claim")
	}
	return releaseClaim(ctx, c.pool, actor.ID, key, mcpClaimEndpoint(tool))
}
