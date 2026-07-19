// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The shared seam every compose-package test drives a scripted
// *ai.FakeClient through: instead of handing the raw fake straight to a
// seam under test (evidenceExtractor, the deep-read worker, the offer
// drafter, …), it rides the same DB-less router NewLocalModelPath wires
// production's DB-less callers through — routing, the budget guardrail,
// metering and secret-stripping all run for real, only the provider is
// the deterministic fake.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fakeModelPath builds a ModelPath over fake via the router — never a
// direct client wiring — so a test's scripted responses are served
// through the exact seam production code uses. WithoutResultCache is
// always on: a scripted sequence of distinct responses must never
// collapse onto one cached answer.
func fakeModelPath(t *testing.T, fake *ai.FakeClient) ModelPath {
	t.Helper()
	mp, err := NewLocalModelPath(ai.FakeRoutingConfig(), ai.WithFakeClient(fake), ai.WithoutResultCache())
	if err != nil {
		t.Fatalf("NewLocalModelPath: %v", err)
	}
	return mp
}

// fakeWorkspaceCtx gives a DB-less router test a workspace principal to
// serve under (ai.Router.Complete refuses to run outside one — a test
// driving the fake through the router needs a tenant even though
// NewLocalModelPath's meter and cache are in-memory, not a real
// workspace row).
func fakeWorkspaceCtx() context.Context {
	return principal.WithWorkspaceID(context.Background(), ids.NewV7())
}
