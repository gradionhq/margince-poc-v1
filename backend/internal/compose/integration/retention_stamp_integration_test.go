// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The commercial-correspondence stamp (A165/ADR-0114, A167/ADR-0116).
//
// The floor now asks whether an activity is a Handelsbrief — correspondence
// about an actual commercial transaction — and answers from a stamp written
// when the deal concluded, not from a link walked at erasure time. These tests
// drive the REAL winning transition through the deals store, because the whole
// point of the stamp is that it commits with the transaction that earns it: a
// fixture that inserted the column by hand would prove the column exists and
// nothing about the writer.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// stampFixture is one deal, its pipeline, an open and a won stage, and an
// email filed against the deal before it concluded.
type stampFixture struct {
	deal     ids.UUID
	wonStage ids.StageID
	email    ids.UUID
}

func seedStampFixture(t *testing.T, e *Env) stampFixture {
	t.Helper()
	pipeline, openStage, wonStage, email := ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7()
	e.WsExec(t, `INSERT INTO pipeline (id, workspace_id, name, is_default, position)
		VALUES ($1, $2, 'Stamp fixture', false, 91)`, pipeline, e.WS)
	e.WsExec(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, $3, 'Qualify', 0, 'open', 10)`, openStage, e.WS, pipeline)
	e.WsExec(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, $3, 'Closed Won', 1, 'won', 100)`, wonStage, e.WS, pipeline)

	deal := e.SeedDeal(t, "Acme rollout",
		ids.PipelineID{UUID: pipeline}, ids.StageID{UUID: openStage}, nil)

	// Filed against the deal while it is still open, which is the ordinary
	// order: the correspondence exists before the deal concludes.
	e.WsExec(t, `INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', 'Order confirmation', 'the agreed price was 4200 EUR', now(), 'manual', 'human:x')`,
		email, e.WS)
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, deal_id)
		VALUES ($1, $2, 'deal', $3)`, e.WS, email, deal)

	return stampFixture{deal: deal, wonStage: ids.StageID{UUID: wonStage}, email: email}
}

// wonInput wins a deal without a contract. The paper is a separate obligation
// from the retention class and these tests are about the class, so the win
// declares why there is none rather than seeding an agreement to satisfy a
// guard it is not exercising.
func wonInput(stage ids.StageID) deals.AdvanceDealInput {
	reason := "verbal"
	return deals.AdvanceDealInput{ToStageID: stage, WonWithoutContractReason: &reason}
}

type stampRow struct {
	class    *string
	stampAt  *string
	evidence int
	dealName *string
}

func readStamp(t *testing.T, e *Env, activity ids.UUID) stampRow {
	t.Helper()
	var got stampRow
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT retention_class, retention_class_at::text FROM activity WHERE id = $1`,
			activity).Scan(&got.class, &got.stampAt); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT count(*), max(deal_name) FROM activity_retention_evidence WHERE activity_id = $1`,
			activity).Scan(&got.evidence, &got.dealName)
	}); err != nil {
		t.Fatalf("reading the stamp: %v", err)
	}
	return got
}

// Winning the deal stamps the correspondence filed against it, and records the
// deal that qualified it — in the same transaction, so there is no window in
// which an erasure sees the record unclassified.
func TestWinningADealStampsItsCorrespondence(t *testing.T) {
	e := Setup(t)
	f := seedStampFixture(t, e)

	if before := readStamp(t, e, f.email); before.class != nil {
		t.Fatalf("fixture drift: the email carries a class before the deal concluded (%q)", *before.class)
	}

	if _, err := e.Deals.AdvanceDeal(e.Admin(), ids.DealID{UUID: f.deal}, wonInput(f.wonStage)); err != nil {
		t.Fatalf("advancing the deal to won: %v", err)
	}

	got := readStamp(t, e, f.email)
	if got.class == nil {
		t.Fatal("winning the deal left its correspondence unstamped — an unstamped Handelsbrief is one the next erasure destroys")
	}
	if *got.class != "commercial_correspondence" {
		t.Errorf("retention_class = %q, want commercial_correspondence", *got.class)
	}
	if got.stampAt == nil {
		t.Error("the class landed without its timestamp; the stamp's provenance is what a supervisory authority is shown")
	}
	if got.evidence != 1 {
		t.Errorf("evidence rows = %d, want 1 — a stamp with nothing behind it is an assertion the controller cannot substantiate", got.evidence)
	}
	if got.dealName == nil || *got.dealName != "Acme rollout" {
		t.Errorf("evidence deal_name = %v, want the deal's name frozen at qualification", got.dealName)
	}
}

// Reopening a won deal never unstamps it. Qualification is reversible in the
// product and the classification deliberately is not: over-retention is an
// argument to have with a supervisory authority, destruction is irreversible.
func TestReopeningAWonDealLeavesTheStampStanding(t *testing.T) {
	e := Setup(t)
	f := seedStampFixture(t, e)

	openAgain := ids.NewV7()
	e.WsExec(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, (SELECT pipeline_id FROM stage WHERE id = $3), 'Reopened', 2, 'open', 40)`,
		openAgain, e.WS, f.wonStage.UUID)

	if _, err := e.Deals.AdvanceDeal(e.Admin(), ids.DealID{UUID: f.deal}, wonInput(f.wonStage)); err != nil {
		t.Fatalf("advancing to won: %v", err)
	}
	if _, err := e.Deals.AdvanceDeal(e.Admin(), ids.DealID{UUID: f.deal},
		deals.AdvanceDealInput{ToStageID: ids.StageID{UUID: openAgain}}); err != nil {
		t.Fatalf("reopening the deal: %v", err)
	}

	got := readStamp(t, e, f.email)
	if got.class == nil {
		t.Fatal("reopening the deal unstamped its correspondence — the classification is monotonic precisely because qualification is not")
	}
}
