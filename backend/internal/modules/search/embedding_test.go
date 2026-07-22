// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// unboundEmbedder is a minimal Embedder standing in for the real
// --ai-fake/no-embeddings-configured shape: EmbedIdentity() reports the
// legitimate unbound ("", 0) lane (ai.Router.EmbedIdentity's own
// documented contract for a never-bound embeddings tier). Embed panics if
// ever called — the whole point of this test is that UpsertEmbedding must
// never reach it when unbound.
type unboundEmbedder struct{}

func (unboundEmbedder) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	panic("Embed must never be called on an unbound embed lane")
}

func (unboundEmbedder) EmbedIdentity() (string, int) { return "", 0 }

// TestUpsertEmbeddingNoOpsOnUnboundLane pins the fix for the F1 defect: an
// unbound embed lane (identity == "") must make UpsertEmbedding a clean
// no-op, not the hard error the width guard used to raise (dims==0 vs the
// fake's own 1024 default). A store with a nil pool and a context
// carrying no workspace proves the no-op fires BEFORE any DB work is
// attempted — if the identity=="" guard above WithWorkspaceTx ever
// regresses, this would surface as a non-nil error (ErrNoWorkspace) or a
// nil-pool panic instead of the (false, nil) this test requires. This is
// also the anti-loop pin: search.EmbedGen.HandleEvent treats a non-nil
// UpsertEmbedding error as unacked, so a regression here reproduces the
// redelivery loop the fix removed.
func TestUpsertEmbeddingNoOpsOnUnboundLane(t *testing.T) {
	s := NewStore(nil)
	fresh, err := s.UpsertEmbedding(context.Background(), "person", ids.NewV7(), "some text", unboundEmbedder{})
	if err != nil {
		t.Fatalf("unbound lane must not error, got %v", err)
	}
	if fresh {
		t.Fatal("unbound lane must not report fresh=true — nothing was embedded")
	}
}
