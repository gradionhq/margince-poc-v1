// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSweepWorkspaceEmbeddingDriftNoOpsOnAnUnboundEmbedLane(t *testing.T) {
	// The guard must fire before any store read: a nil pool proves the
	// sweep never touches the database for an unbound lane (a query here
	// would nil-panic, not fail an assertion).
	healed, err := NewStore(nil).SweepWorkspaceEmbeddingDrift(context.Background(), ids.WorkspaceID{}, unboundEmbedder{})
	if err != nil {
		t.Fatalf("SweepWorkspaceEmbeddingDrift on an unbound lane: %v", err)
	}
	if healed != 0 {
		t.Fatalf("healed = %d, want 0 — there is no identity to heal under", healed)
	}
}
