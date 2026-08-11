// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The race itself, against real Postgres.
//
// The unit lane proves the tool hands the store the version the gate read. What
// it cannot prove is that the store then REFUSES on it: the version compare
// lives in Patch.ApplyGuarded, inside the transaction deals.Store.AdvanceDeal
// opens, and only a database answers that.
//
// So everything here is real except the timing: the deal, the pipeline, both
// writes, the gate, the tier resolver and the version compare. The provider is
// wrapped only to make the racing close land at the one instant that reproduces
// the defect — inside the window between the gate's read and the write it
// admits. A concurrent close is otherwise a coin toss, and a test that
// reproduces a race one run in fifty is a test that reports green.
//
// The authority seam is stubbed (adminSeat) because this is not about who the
// agent is; the gate's own suite covers that, and a real seat lookup would make
// this file fail for reasons that are not the race.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// adminSeat is the authority seam under this race: a full seat with the grants
// a deal move needs, so the call reaches the write and the refusal that follows
// is the version compare rather than a missing grant.
type adminSeat struct{}

func (adminSeat) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{Permissions: integration.AdminPerms}, nil
}

func (adminSeat) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

// closesDuringTheTierRead is the real provider with one instant of theatre: the
// FIRST read of a deal answers the record as it stands and then, on its way
// out, closes that deal for real. Every later read sees the closed deal, which
// is what the racing actor left behind.
type closesDuringTheTierRead struct {
	datasource.SystemOfRecordProvider
	t     *testing.T
	as    context.Context
	won   ids.UUID
	raced bool
}

func (p *closesDuringTheTierRead) Read(ctx context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	rec, err := p.SystemOfRecordProvider.Read(ctx, ref)
	if err != nil || p.raced || ref.Type != datasource.EntityDeal {
		return rec, err
	}
	p.raced = true
	// The real provider's own write, committed in its own transaction — the
	// concurrent actor's close, not a hand-made row.
	if _, err := p.AdvanceDeal(p.as, datasource.AdvanceDealInput{
		DealID: ref.ID, ToStageID: p.won, Source: "test",
	}); err != nil {
		p.t.Fatalf("the racing close did not land, so this test is not about a race: %v", err)
	}
	return rec, nil
}

func TestAConcurrentCloseSurvivesTheAutoExecutedMoveThatRacedIt(t *testing.T) {
	e := integration.Setup(t)
	pipeline, open, won := integration.DealFixture(t, e)
	human := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)

	native := NewProvider(e.Pool)
	racing := &closesDuringTheTierRead{
		SystemOfRecordProvider: native, t: t, as: human, won: won.UUID,
	}
	registry := agents.NewRegistry(
		approvalsAdapter{svc: approvals.NewService(e.Pool)}, auth.NewGate(adminSeat{}))
	// The stage resolver reads pipeline configuration and nothing this race
	// touches, so it goes straight to the real provider — only the RECORD read
	// carries the interleaving.
	agents.RegisterCoreTools(registry, racing, native, nil, fieldOwnership{pool: e.Pool})

	deal := seedDealForPinRace(human, t, native, e.Rep1, pipeline, open)

	// The move the agent asks for is open→open, which is 🟢 and runs unattended
	// — decided from a read that is already stale by the time the write opens
	// its transaction.
	_, err := registry.Invoke(pinRaceAgent(e), "advance_deal",
		json.RawMessage(`{"deal_id":"`+deal.String()+`","to_stage_id":"`+open.String()+`"}`))
	if !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("the racing move answered %v, want version skew — the tier gate proved both "+
			"endpoints open against a deal that closed before the write, and nothing bound the two", err)
	}

	// The outcome, which is what the issue is actually about: the deal is still
	// won, with the close the racing actor committed intact.
	stage, closedAt := dealStageAndClose(t, e, deal)
	if stage != won.String() {
		t.Errorf("the deal sits in stage %s, want the won stage the concurrent actor closed it in — "+
			"it was reopened by a call whose 🟢 tier was decided before that close", stage)
	}
	if !closedAt {
		t.Error("the deal has no close date, so the reopen cleared it — along with the lost reason " +
			"and the exchange rate frozen at close")
	}
}

// seedDealForPinRace creates one deal in the pipeline's open stage, through the
// real provider, and answers its id.
func seedDealForPinRace(as context.Context, t *testing.T, p *Provider, owner ids.UUID,
	pipeline ids.PipelineID, open ids.StageID,
) ids.UUID {
	t.Helper()
	ref, err := p.Create(as, datasource.CreateInput{
		EntityType: datasource.EntityDeal,
		Fields: json.RawMessage(`{"name":"Pin race","owner_id":"` + owner.String() +
			`","pipeline_id":"` + pipeline.String() + `","stage_id":"` + open.String() + `"}`),
		Source: "test",
	})
	if err != nil {
		t.Fatalf("seeding the deal: %v", err)
	}
	return ref.ID
}

// dealStageAndClose reads what the deal actually is now — the fact that decides
// whether the refusal held.
func dealStageAndClose(t *testing.T, e *integration.Env, deal ids.UUID) (string, bool) {
	t.Helper()
	var stage ids.UUID
	var closedAt *string
	seedAsAdmin(t, e, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT stage_id, closed_at::text FROM deal WHERE id = $1`, deal).Scan(&stage, &closedAt)
	}, "reading the deal back")
	return stage.String(), closedAt != nil
}

// pinRaceAgent is a passport principal holding the write scope — the type the
// tier gate governs, and the only one the pin is set for.
func pinRaceAgent(e *integration.Env) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:pin-race", SeatType: principal.SeatFull,
		OnBehalfOf: e.Rep1, UserID: e.Rep1, PassportID: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
		// Stamped as well as resolved: the tier resolver's read runs on the
		// context the caller arrived with, and only the admitted context carries
		// the authority the gate re-derived.
		Permissions: integration.AdminPerms,
	})
}
