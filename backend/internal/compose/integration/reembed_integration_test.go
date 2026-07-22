// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The resumable corpus re-embed routine (ADR-0068 design §5.6-swap v7,
// Task 10): ReembedCorpus re-embeds every live entity fleet-wide under a
// target identity, is free to re-run (UpsertEmbedding's content-hash +
// identity skip-compare makes an already-current row cost no model call),
// and refuses to run at all — via ErrIdentityDrift — when the embedder
// compose actually injected disagrees with the job's target identity.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestReembedCorpusReembedsAllLiveEntitiesAndIsResumable seeds 3 people
// under a stale identity, then proves a single ReembedCorpus call under a
// NEW identity re-embeds all 3 (their stored model becomes the new
// identity), flips the binding marker via CompleteReembedding (status
// idle, populated = new), and reads EntitiesPending == 0 afterward. A
// SECOND ReembedCorpus pass over the same identity must cost zero
// additional embed calls — the resumability property Task 6's
// skip-compare exists to provide.
func TestReembedCorpusReembedsAllLiveEntitiesAndIsResumable(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	staleEmbedder := fakeEmbedderNamed(t, fake, "model-stale")
	newEmbedder := fakeEmbedderNamed(t, fake, "model-new")
	staleIdentity, _ := staleEmbedder.EmbedIdentity()
	newIdentity, _ := newEmbedder.EmbedIdentity()
	if staleIdentity == newIdentity {
		t.Fatalf("test setup produced identical identities %q — no swap exercised", staleIdentity)
	}

	if err := e.store.SeedBinding(ctx, staleIdentity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	names := []string{"Reembed One", "Reembed Two", "Reembed Three"}
	personIDs := make([]ids.UUID, len(names))
	for i, name := range names {
		id := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, $3, 'manual', 'human:x')`, name)
		if _, err := e.store.UpsertEmbedding(e.Admin(), "person", id, name, staleEmbedder); err != nil {
			t.Fatalf("seeding the stale-identity baseline for %s: %v", name, err)
		}
		personIDs[i] = id
	}
	baselineCalls := len(fake.Calls())

	// Mirrors the real job's start (Task 15): the CAS moves the marker to
	// 'reembedding' so CompleteReembedding — a CAS that only fires FROM
	// that status — has something to flip back.
	if err := e.store.ClaimAndEnqueueReembedding(ctx, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding: %v", err)
	}

	if err := e.store.ReembedCorpus(ctx, newEmbedder, newIdentity); err != nil {
		t.Fatalf("ReembedCorpus: %v", err)
	}

	populated, status, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != newIdentity {
		t.Fatalf("populated_identity = %q, want %q", populated, newIdentity)
	}
	if status != "idle" {
		t.Fatalf("status = %q, want idle after a clean ReembedCorpus finish", status)
	}

	for i, id := range personIDs {
		if got := e.storedEmbeddingModel(t, id); got != newIdentity {
			t.Fatalf("person[%d] model = %q, want %q (must have been re-embedded under the new identity)", i, got, newIdentity)
		}
	}

	firstPassCalls := len(fake.Calls()) - baselineCalls
	if firstPassCalls != len(names) {
		t.Fatalf("first ReembedCorpus made %d embed calls, want %d (one per live entity)", firstPassCalls, len(names))
	}

	pending, err := e.store.EntitiesPending(ctx, newIdentity)
	if err != nil {
		t.Fatalf("EntitiesPending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("EntitiesPending = %d, want 0 after a clean re-embed", pending)
	}

	// Resumability: nothing changed since the first pass, so every row is
	// already current under newIdentity — the skip-compare inside
	// UpsertEmbedding must short-circuit before ever calling the embedder.
	if err := e.store.ReembedCorpus(ctx, newEmbedder, newIdentity); err != nil {
		t.Fatalf("second ReembedCorpus: %v", err)
	}
	secondPassCalls := len(fake.Calls()) - baselineCalls - firstPassCalls
	if secondPassCalls != 0 {
		t.Fatalf("second ReembedCorpus made %d embed calls, want 0 (a resumed/re-run pass must be free)", secondPassCalls)
	}

	pending, err = e.store.EntitiesPending(ctx, newIdentity)
	if err != nil {
		t.Fatalf("EntitiesPending after second pass: %v", err)
	}
	if pending != 0 {
		t.Fatalf("EntitiesPending after second pass = %d, want 0", pending)
	}
}

// TestReembedCorpusIdentityDriftCancelsWithoutTouchingRows proves the
// entry guard fires — and touches NOTHING — when the embedder compose
// actually injected no longer agrees with the job's own target identity:
// an operator swapped the live embed binding after this job was
// enqueued. Task 15 maps ErrIdentityDrift to river.JobCancel so a stale
// job cancels cleanly instead of retrying 25 times against an identity
// nothing serves anymore.
func TestReembedCorpusIdentityDriftCancelsWithoutTouchingRows(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-current")

	const markerIdentity = "stale-marker-identity"
	const staleRowIdentity = "stale-marker-identity"
	if err := e.store.SeedBinding(ctx, markerIdentity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Drift Person', 'manual', 'human:x')`)
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'stale-hash', $3, '[1,2,3]'::vector)`,
		e.WS, personID, staleRowIdentity); err != nil {
		t.Fatalf("seeding the stale-identity row: %v", err)
	}

	// The job's own args identity does NOT match what embedder actually
	// reports — the drift the guard exists to catch.
	err := e.store.ReembedCorpus(ctx, embedder, "some-other-target-identity")
	if !errors.Is(err, search.ErrIdentityDrift) {
		t.Fatalf("ReembedCorpus with a mismatched argsIdentity = %v, want ErrIdentityDrift", err)
	}

	if calls := len(fake.Calls()); calls != 0 {
		t.Fatalf("identity drift must not call the embedder, got %d calls", calls)
	}
	if got := e.storedEmbeddingModel(t, personID); got != staleRowIdentity {
		t.Fatalf("drift guard must not touch existing rows, model = %q, want unchanged %q", got, staleRowIdentity)
	}
	_, status, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "idle" {
		t.Fatalf("identity drift must not alter the binding marker's status, got %q", status)
	}
}
