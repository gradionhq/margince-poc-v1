// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

// The similarity ranking behind the uncertain rung, over a real Postgres with
// pgvector: which of several live projects a message is nearest, and every way
// the ranking refuses to answer. Exercised on the rung's own function because
// a freshly captured message has no embedding yet — the embedding job runs
// after capture — so the ranked path is reachable only for a message embedded
// before the ladder asks, and the composed capture tests cannot stage one.
//
// The vectors are seeded by hand. The embedding writer is a model call, which
// is exactly the boundary a test may stand in for; what this proves is the
// query over what that writer leaves behind.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedVector writes one embedding row under one model.
func seedVector(ctx context.Context, t *testing.T, tx pgx.Tx, entityType string, id ids.UUID, model string, vector string) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
		INSERT INTO embedding (entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, $2, 0, 'h', $3, $4::vector)`, entityType, id, model, vector); err != nil {
		t.Fatalf("seeding the %s embedding: %v", entityType, err)
	}
}

// rankingTx opens a transaction that is rolled back when the test ends, so the
// vectors it seeds never outlive it.
func rankingTx(t *testing.T) (context.Context, pgx.Tx) {
	t.Helper()
	ctx := context.Background()
	tx, err := ownerConn(t).Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(ctx); err != nil {
			t.Errorf("rolling back the ranking transaction: %v", err)
		}
	})
	return ctx, tx
}

func TestNearestProjectRanksByCosineSimilarity(t *testing.T) {
	ctx, tx := rankingTx(t)
	activity := ids.From[ids.ActivityKind](ids.NewV7())
	erp, crm := LiveProject{ID: ids.NewV7(), Name: "ERP"}, LiveProject{ID: ids.NewV7(), Name: "CRM"}
	seedVector(ctx, t, tx, "activity", activity.UUID, "m1", "[1,0,0]")
	seedVector(ctx, t, tx, "project", erp.ID, "m1", "[0.9,0.1,0]")
	seedVector(ctx, t, tx, "project", crm.ID, "m1", "[0,1,0]")

	got, similarity, found, err := nearestProject(ctx, tx, activity, []LiveProject{erp, crm})
	if err != nil {
		t.Fatalf("nearestProject: %v", err)
	}
	if !found || got.ID != erp.ID {
		t.Fatalf("nearest = %+v (found=%v), want ERP", got, found)
	}
	if similarity < similarityFloor || similarity > 1 {
		t.Fatalf("similarity = %v, want within [%v, 1]", similarity, similarityFloor)
	}
}

// Below the floor, nearest is not near enough: a message about nothing in
// particular is still nearest to something, and that must not become a
// question in somebody's inbox.
func TestNearestProjectRefusesBelowTheFloor(t *testing.T) {
	ctx, tx := rankingTx(t)
	activity := ids.From[ids.ActivityKind](ids.NewV7())
	erp, crm := LiveProject{ID: ids.NewV7(), Name: "ERP"}, LiveProject{ID: ids.NewV7(), Name: "CRM"}
	seedVector(ctx, t, tx, "activity", activity.UUID, "m1", "[1,0,0]")
	seedVector(ctx, t, tx, "project", erp.ID, "m1", "[0,1,0]")
	seedVector(ctx, t, tx, "project", crm.ID, "m1", "[0,0,1]")

	_, _, found, err := nearestProject(ctx, tx, activity, []LiveProject{erp, crm})
	if err != nil {
		t.Fatalf("nearestProject: %v", err)
	}
	if found {
		t.Fatal("a project at zero similarity was proposed — the floor must refuse it")
	}
}

// A tie is ambiguity, answered the way the ladder answers every ambiguity:
// with nothing.
func TestNearestProjectRefusesATie(t *testing.T) {
	ctx, tx := rankingTx(t)
	activity := ids.From[ids.ActivityKind](ids.NewV7())
	erp, crm := LiveProject{ID: ids.NewV7(), Name: "ERP"}, LiveProject{ID: ids.NewV7(), Name: "CRM"}
	seedVector(ctx, t, tx, "activity", activity.UUID, "m1", "[1,0,0]")
	seedVector(ctx, t, tx, "project", erp.ID, "m1", "[1,0,0]")
	seedVector(ctx, t, tx, "project", crm.ID, "m1", "[1,0,0]")

	_, _, found, err := nearestProject(ctx, tx, activity, []LiveProject{erp, crm})
	if err != nil {
		t.Fatalf("nearestProject: %v", err)
	}
	if found {
		t.Fatal("two projects at the same similarity were ranked — a tie is not an answer")
	}
}

// No embedding for the message, or none for any project under the message's
// model: nothing to rank, nothing proposed. A project embedded under another
// model is not comparable and must not error the rung.
func TestNearestProjectRefusesWithoutComparableEmbeddings(t *testing.T) {
	ctx, tx := rankingTx(t)
	unembedded := ids.From[ids.ActivityKind](ids.NewV7())
	erp, crm := LiveProject{ID: ids.NewV7(), Name: "ERP"}, LiveProject{ID: ids.NewV7(), Name: "CRM"}
	seedVector(ctx, t, tx, "project", erp.ID, "m1", "[1,0,0]")
	seedVector(ctx, t, tx, "project", crm.ID, "m1", "[0,1,0]")
	if _, _, found, err := nearestProject(ctx, tx, unembedded, []LiveProject{erp, crm}); err != nil || found {
		t.Fatalf("an unembedded message ranked (found=%v, err=%v), want nothing", found, err)
	}

	otherModel := ids.From[ids.ActivityKind](ids.NewV7())
	seedVector(ctx, t, tx, "activity", otherModel.UUID, "m2", "[1,0]")
	if _, _, found, err := nearestProject(ctx, tx, otherModel, []LiveProject{erp, crm}); err != nil || found {
		t.Fatalf("a message under another model ranked (found=%v, err=%v), want nothing", found, err)
	}
}
