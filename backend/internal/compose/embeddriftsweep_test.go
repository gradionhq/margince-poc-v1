// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// staticIdentityEmbedder answers a fixed EmbedIdentity and is never asked
// to embed — the registration tests below only exercise the gate that
// decides whether the sweep exists at all.
type staticIdentityEmbedder struct{ identity string }

func (e staticIdentityEmbedder) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	return model.Embeddings{}, nil
}

func (e staticIdentityEmbedder) EmbedIdentity() (string, int) { return e.identity, 1024 }

func TestAddEmbedDriftSweepJobRequiresABoundEmbedLane(t *testing.T) {
	// No embedder at all: a role with no model path has no store to heal.
	if jobs := addEmbedDriftSweepJob(newJobRegistry(), nil, JobRunnerConfig{}, nil); jobs != nil {
		t.Fatalf("nil embedder registered %d periodic jobs, want none", len(jobs))
	}
	// An embedder whose identity is empty (--ai-fake, or a routing config
	// with no embeddings binding) seeds no marker either — same posture as
	// WithEmbedReindex.
	if jobs := addEmbedDriftSweepJob(newJobRegistry(), nil, JobRunnerConfig{Embedder: staticIdentityEmbedder{identity: ""}}, nil); jobs != nil {
		t.Fatalf("empty-identity embedder registered %d periodic jobs, want none", len(jobs))
	}
}

func TestAddEmbedDriftSweepJobRegistersWorkerAndTick(t *testing.T) {
	reg := newJobRegistry()
	jobs := addEmbedDriftSweepJob(reg, nil, JobRunnerConfig{Embedder: staticIdentityEmbedder{identity: "fake/model@1024"}}, nil)
	if len(jobs) != 1 {
		t.Fatalf("bound embed lane registered %d periodic jobs, want exactly 1", len(jobs))
	}
	// The worker must be registered under the same kind the periodic tick
	// enqueues — a tick with no worker would queue jobs nothing works.
	// AddWorker panics on a duplicate kind, so a second registration here
	// proves the first one happened.
	defer func() {
		if recover() == nil {
			t.Fatalf("re-registering the sweep worker did not panic — the first registration never happened")
		}
	}()
	addGovernedWorker[EmbedDriftSweepArgs](reg, &embedDriftSweepWorker{}, 0)
}
