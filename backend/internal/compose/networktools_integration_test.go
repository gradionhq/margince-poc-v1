// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The two newer relationship-graph seams (ADR-0078) against a real database:
// intro_path_to, at_risk_relationships, and the retriever decorator that puts a
// deal's coverage findings into the assistant's context.
//
// These are the seams where a tool's answer is assembled, and the things worth
// pinning are the ones a stub cannot show: that a route names both of its ends,
// that a capped sweep reports its own reach, and that a refused advisory read
// costs the section rather than the whole answer.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// employAt records one contact's live employment at an account, the edge an
// intro route walks its second hop over.
func employAt(t *testing.T, e *integration.Env, person, org ids.UUID) {
	t.Helper()
	seedAsAdmin(t, e, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES (`+wsGUC+`, 'employment', $1, $2, 'manual', 'human:test')`, person, org)
		return err
	}, "recording employment")
}

// wsGUC is the workspace the RLS GUC already binds. Every seed here writes
// through WithWorkspaceTx for that reason: a raw pool exec has no GUC and the
// policy refuses it.
const wsGUC = `NULLIF(current_setting('app.workspace_id', true), '')::uuid`

// seedAsAdmin runs one fixture write inside a workspace-bound transaction.
func seedAsAdmin(t *testing.T, e *integration.Env, fn func(context.Context, pgx.Tx) error, what string) {
	t.Helper()
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return fn(ctx, tx)
	}); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func TestIntroPathNamesBothEndsOfTheRouteAndSaysWhenItWasCapped(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)

	var orgID ids.UUID
	seedAsAdmin(t, e, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO organization (workspace_id, display_name, source, captured_by)
			VALUES (`+wsGUC+`, 'Acme GmbH', 'manual', 'human:test') RETURNING id`).Scan(&orgID)
	}, "seeding the account")
	person, err := e.People.CreatePerson(ctx, people.CreatePersonInput{
		FullName: "Jonas Bach", Source: "manual",
	})
	if err != nil {
		t.Fatalf("seeding the contact: %v", err)
	}
	employAt(t, e, ids.UUID(person.Id), orgID)
	// One recorded interaction, which is what makes a route rather than a name.
	seedInteractionEdge(t, e, e.Rep1, ids.UUID(person.Id))

	routes, truncated, err := introPathLister(e.Pool)(ctx, orgID)
	if err != nil {
		t.Fatalf("intro path: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want the one colleague with recorded contact", len(routes))
	}
	// Both ends: a route naming only the colleague leaves a rep asking "an
	// intro to whom".
	if routes[0].UserID != e.Rep1 || routes[0].PersonID != ids.UUID(person.Id) {
		t.Errorf("the route is %+v, want Rep1 → Jonas Bach", routes[0])
	}
	if routes[0].PersonName == "" || routes[0].DisplayName == "" {
		t.Error("the route carries a bare uuid on one end; a rep cannot act on it")
	}
	// An account well under the fetch bound was not cut, and says so.
	if truncated {
		t.Error("a one-contact account reported its candidate set as truncated")
	}
}

func TestIntroPathRefusesAnAccountTheCallerCannotRead(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)

	// An account that does not exist and one the caller may not read must
	// answer the same way, or the difference names the record.
	if _, _, err := introPathLister(e.Pool)(ctx, ids.NewV7()); err == nil {
		t.Error("an unknown account answered a route list rather than a refusal")
	}
}

func TestTheAtRiskSweepReportsItsOwnReach(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	seedOpenDeal(t, e)

	report, err := atRiskLister(e.Pool)(ctx)
	if err != nil {
		t.Fatalf("at-risk sweep: %v", err)
	}
	// One open deal with nobody on it is below REPORT-PARAM-1's floor, so the
	// sweep has something to say — and it says how far it looked, because a
	// capped scan presented as a clean pipeline is the failure this field
	// exists to prevent.
	if report.DealsScanned == 0 {
		t.Fatal("the sweep reports scanning no deals, but one open deal exists")
	}
	if report.Truncated {
		t.Error("a one-deal pipeline reported its scan as truncated")
	}
	if len(report.Deals) == 0 {
		t.Error("a deal with no engaged contacts raised no finding")
	}
}

func TestTheRiskRetrieverDropsTheSectionNotTheAnswerWhenTheDealIsRefused(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)

	// The inner walk succeeded; the coverage read is refused because the deal
	// id resolves to nothing this caller can read. The assembled timeline must
	// survive — a revoked grant costs the advisory section, not the answer.
	inner := stubContext{out: retrieval.Context{
		Anchor:   datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()},
		Sections: []retrieval.Section{{Name: "recent_touches", Items: []retrieval.Item{{Summary: "a call"}}}},
	}}
	got, err := riskAwareRetriever{pool: e.Pool, inner: inner}.
		AssembleContext(ctx, inner.out.Anchor, retrieval.AssembleOptions{})
	if err != nil {
		t.Fatalf("a refused coverage read failed the whole assembly: %v", err)
	}
	if len(got.Sections) != 1 || got.Sections[0].Name != "recent_touches" {
		t.Errorf("the assembled context is %+v, want the inner walk intact with no risk section", got.Sections)
	}
}

// stubContext is an inner retriever that returns a fixed assembly, so the
// decorator's own behaviour is what is under test rather than the walk's.
type stubContext struct{ out retrieval.Context }

func (s stubContext) Search(context.Context, retrieval.Query) (retrieval.Result, error) {
	return retrieval.Result{}, nil
}

func (s stubContext) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	return s.out, nil
}

// seedInteractionEdge writes one row of the projection directly: the fold is
// tested elsewhere, and these tests are about what the seams do with an edge
// that exists.
func seedInteractionEdge(t *testing.T, e *integration.Env, user, person ids.UUID) {
	t.Helper()
	seedAsAdmin(t, e, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO graph_interaction_edge
			    (workspace_id, user_id, person_id, last_at, count_90d, in_count_90d,
			     out_count_90d, count_total, computed_at)
			VALUES (`+wsGUC+`, $1, $2, now(), 6, 3, 3, 6, now())`, user, person)
		return err
	}, "seeding the interaction edge")
}

// seedOpenDeal writes one open deal on the workspace's default pipeline.
func seedOpenDeal(t *testing.T, e *integration.Env) ids.UUID {
	t.Helper()
	var dealID ids.UUID
	seedAsAdmin(t, e, func(ctx context.Context, tx pgx.Tx) error {
		var pipelineID, stageID ids.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO pipeline (workspace_id, name) VALUES (`+wsGUC+`, 'At-risk test')
			RETURNING id`).Scan(&pipelineID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO stage (workspace_id, pipeline_id, name, position)
			VALUES (`+wsGUC+`, $1, 'Qualified', 0) RETURNING id`, pipelineID).Scan(&stageID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO deal (workspace_id, name, stage_id, pipeline_id, owner_id, source, captured_by)
			VALUES (`+wsGUC+`, 'Threadless', $1, $2, $3, 'manual', 'human:test')
			RETURNING id`, stageID, pipelineID, e.Rep1).Scan(&dealID)
	}, "seeding the deal")
	return dealID
}
