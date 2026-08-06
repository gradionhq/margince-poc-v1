// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// A nil pool is safe here: FakeRoutingConfig's embeddings binding has no
// model, so seedEmbedBinding's identity check is empty and it never
// touches the store construction wraps. The router itself does no I/O
// until a call is served.
func TestNewModelPathExposesCacheInvalidation(t *testing.T) {
	path, err := NewModelPath(ai.FakeRoutingConfig(), nil, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewModelPath: %v", err)
	}
	if path.InvalidateCache == nil {
		t.Fatal("InvalidateCache is nil; a data reset cannot drop cached completions and the reseeded install would serve pre-reset answers")
	}
	path.InvalidateCache(ids.WorkspaceID{}) // must not panic
}

// The hook must actually reach the router's result cache, not just exist:
// a second identical request under the same workspace is served from
// cache (one model call recorded), and only a request AFTER
// InvalidateCache reaches the fake client again. Runs over
// NewLocalModelPath (no Postgres) because the router refuses to serve a
// completion outside a workspace context, and metering a real NewModelPath
// call needs the real pool the compose/integration suite already covers
// (see ai_fake_modelpath_integration_test.go).
func TestModelPathInvalidateCacheForcesAFreshCompletion(t *testing.T) {
	fake := ai.NewFakeClient()
	path, err := NewLocalModelPath(ai.FakeRoutingConfig(), ai.WithFakeClient(fake))
	if err != nil {
		t.Fatalf("NewLocalModelPath: %v", err)
	}

	workspace := ids.New[ids.WorkspaceKind]()
	ctx := principal.WithWorkspaceID(context.Background(), workspace.UUID)
	req := model.Request{
		System:   "test",
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}

	if _, err := path.ColdStart.Complete(ctx, req); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := path.ColdStart.Complete(ctx, req); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if calls := len(fake.Calls()); calls != 1 {
		t.Fatalf("calls after two identical requests = %d, want 1 (the second must be a cache hit)", calls)
	}

	path.InvalidateCache(workspace)

	if _, err := path.ColdStart.Complete(ctx, req); err != nil {
		t.Fatalf("third Complete: %v", err)
	}
	if calls := len(fake.Calls()); calls != 2 {
		t.Fatalf("calls after invalidation = %d, want 2 (the cache must have been dropped)", calls)
	}
}
