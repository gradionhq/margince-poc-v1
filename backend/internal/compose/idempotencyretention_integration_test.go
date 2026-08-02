// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Storage limitation for the replay claim table over a real Postgres: a
// settled claim carries a verbatim record snapshot, so once the replay window
// it protects has closed the row is subject data kept for no purpose and the
// sweep removes it — while a claim still inside the window is untouched,
// because deleting it would break the retry it exists to make safe.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedSettledClaim plants one recorded response whose created_at is ageOffset
// (a Postgres interval expression) into the past.
func seedSettledClaim(t *testing.T, e *integration.Env, key, ageOffset string) {
	t.Helper()
	e.WsExec(t, `
		INSERT INTO idempotency_key
		  (workspace_id, principal_id, key, endpoint, request_digest,
		   response_status, response_body, response_content_type, created_at)
		VALUES ($1, $2, $3, 'POST /v1/people', 'digest',
		        201, '{"full_name":"Snapshot Subject","email":"subject@example.org"}',
		        'application/json', now() - $4::interval)`,
		e.WS, "human:"+ids.NewV7().String(), key, ageOffset)
}

func claimExists(t *testing.T, e *integration.Env, key string) bool {
	t.Helper()
	return e.WsCount(t, `SELECT count(*) FROM idempotency_key WHERE key = $1`, key) > 0
}

func TestIdempotencyRetentionPurgesSnapshotsPastTheReplayWindow(t *testing.T) {
	e := integration.Setup(t)

	seedSettledClaim(t, e, "expired-claim", "30 hours")
	seedSettledClaim(t, e, "fresh-claim", "1 hour")

	sweeper := NewIdempotencyRetentionSweeper(e.Pool, slog.New(slog.DiscardHandler))
	if err := sweeper.SweepWorkspace(principal.WithWorkspaceID(context.Background(), e.WS)); err != nil {
		t.Fatalf("SweepWorkspace: %v", err)
	}

	if claimExists(t, e, "expired-claim") {
		t.Error("a claim past the 24h replay window survived the sweep — its response body is a full record snapshot that can never be replayed again, so keeping it stores subject data for no purpose")
	}
	// The positive control: the sweep must not be indiscriminate, or the
	// retry-safety the table exists for would be gone.
	if !claimExists(t, e, "fresh-claim") {
		t.Error("a claim still inside the replay window was purged — the retry it protects would re-execute")
	}

	t.Run("a second pass is a no-op", func(t *testing.T) {
		if err := sweeper.SweepWorkspace(principal.WithWorkspaceID(context.Background(), e.WS)); err != nil {
			t.Fatalf("SweepWorkspace: %v", err)
		}
		if !claimExists(t, e, "fresh-claim") {
			t.Error("the second pass purged a claim still inside the window")
		}
	})
}
