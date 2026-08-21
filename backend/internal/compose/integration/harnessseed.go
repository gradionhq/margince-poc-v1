// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The fixture rows a suite writes before it acts, split out of harness.go so
// the environment (the migrated database, the pools, the seat contexts) and the
// records seeded into it read as one concept each.

// The id wideners assert a harness-seeded untyped id as the entity a people-store
// call targets — the suites' spelling of the contracts-edge ids.From widening. The
// harness keeps its fixture ids untyped so every module's suite can share them,
// and each suite widens at the call it makes.
//
// Only PersonIDOf is exported, because integration/channels widens person ids from
// outside this package. The other three have no caller beyond it, and a suite
// package that later needs one exports it then.

// PersonIDOf widens a harness fixture id to a person id.
func PersonIDOf(u ids.UUID) ids.PersonID { return ids.From[ids.PersonKind](u) }

// orgIDOf widens a harness fixture id to an organization id.
func orgIDOf(u ids.UUID) ids.OrganizationID { return ids.From[ids.OrganizationKind](u) }

// leadIDOf widens a harness fixture id to a lead id.
func leadIDOf(u ids.UUID) ids.LeadID       { return ids.From[ids.LeadKind](u) }
func projectIDOf(u ids.UUID) ids.ProjectID { return ids.From[ids.ProjectKind](u) }

// userIDPtr types an optional harness user id (Env keeps its fixture ids
// untyped so every module's suite can use them) for people's typed inputs.
func userIDPtr(owner *ids.UUID) *ids.UserID {
	if owner == nil {
		return nil
	}
	id := ids.From[ids.UserKind](*owner)
	return &id
}

// SeedPerson creates a person owned by the given user (nil = ownerless),
// acting as admin.
func (e *Env) SeedPerson(t *testing.T, name string, owner *ids.UUID) ids.UUID {
	t.Helper()
	p, err := e.People.CreatePerson(e.Admin(), people.CreatePersonInput{FullName: name, OwnerID: userIDPtr(owner), Source: "manual"})
	if err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
	return ids.UUID(p.Id)
}

// SeedOrg creates an organization owned by the given user, acting as admin.
func (e *Env) SeedOrg(t *testing.T, name string, owner *ids.UUID) ids.UUID {
	t.Helper()
	org, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{
		DisplayName: name, OwnerID: userIDPtr(owner),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids.UUID(org.Id)
}

// SeedPartnerOrg creates an organization and gives it a partner programme, so
// a deal may name it.
//
// A deal's partner must BE a partner (the store refuses any other company,
// because commission prices from the margin tier on that row). A fixture that
// pointed a deal at a plain organization was writing a row the product itself
// will not write, which is the shape of test that proves nothing.
//
// The tier is optional: a partner with none is a real and common state — the
// arrangement exists, the rate has not been agreed — and accrual treats it as
// earning nothing rather than as an error.
func (e *Env) SeedPartnerOrg(t *testing.T, name string, tier *string, owner *ids.UUID) ids.UUID {
	t.Helper()
	org := e.SeedOrg(t, name, owner)
	// The harness's AdminPerms deliberately carries no `partner` grant, so the
	// seeding acts as a seat that has one. Borrowing the caller's context would
	// make every suite that seeds a partner grow a permission it is not testing.
	seeder := e.As(e.AdminUser, nil, principal.Permissions{
		RoleKeys: []string{"admin"},
		Objects: map[string]principal.ObjectGrant{
			"partner": {Create: true, Read: true, Update: true},
			// Becoming a partner also stamps the organization's relationship
			// types, so the seat needs the company as well as the programme.
			objOrg: {Read: true, Update: true},
		},
		RowScope: principal.RowScopeAll,
	})
	if _, err := e.People.UpsertPartner(seeder, people.UpsertPartnerInput{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		PartnerRole:    "consulting",
		MarginTier:     tier,
	}); err != nil {
		t.Fatalf("giving %s a partner programme: %v", name, err)
	}
	return org
}

// SeedOrgAs creates an ownerless organization in a SECOND workspace, under
// that workspace's own context — unlike SeedOrg, which always writes the
// harness's primary workspace as e.Admin().
//
// It names the workspace as well as the ctx because the row lands wherever the
// STORE is bound: the harness's own store would stamp the second tenant's ids
// into the first tenant's transaction, which RLS refuses.
func (e *Env) SeedOrgAs(ctx context.Context, t *testing.T, ws ids.UUID, name string) ids.UUID {
	t.Helper()
	org, err := e.PeopleFor(ws).CreateOrganization(ctx, people.CreateOrganizationInput{DisplayName: name})
	if err != nil {
		t.Fatal(err)
	}
	return ids.UUID(org.Id)
}

// SeedDeal creates a deal owned by the given user, acting as admin.
func (e *Env) SeedDeal(t *testing.T, name string, pipeline ids.PipelineID, stage ids.StageID, owner *ids.UUID) ids.UUID {
	t.Helper()
	d, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: name, PipelineID: pipeline, StageID: stage, OwnerID: userIDPtr(owner),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids.UUID(d.Id)
}

// MakeCapturePrivate turns a seeded person or organization into a
// capture-private row — `visibility='owner'`, owned by the given user — the
// state a connector leaves an unpromoted contact in. Person, organization,
// lead and deal are otherwise readable by every seat of the workspace, so
// this is the ONE way a test still has to put an identity row out of a
// caller's read scope; a test about row scope on a commercial table seeds a
// project instead.
func (e *Env) MakeCapturePrivate(t *testing.T, table string, id, owner ids.UUID) {
	t.Helper()
	if table != objPerson && table != objOrg {
		t.Fatalf("MakeCapturePrivate: %s carries no visibility column", table)
	}
	e.WsExec(t, `UPDATE `+table+` SET visibility = 'owner', owner_id = $2 WHERE id = $1`, id, owner)
}

// WsExec runs one setup statement in a workspace-bound transaction (RLS is
// FORCED, so the GUC must be set even for the owner-less test pool).
func (e *Env) WsExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}); err != nil {
		t.Fatalf("setup exec: %v", err)
	}
}

// WsCount returns a scalar count in a workspace-bound transaction.
func (e *Env) WsCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	var n int
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return n
}

// SeedWonDealLinkedTo files the given activities against a WON deal, which is
// what makes them Handelsbriefe under the statutory correspondence floor
// (A165/ADR-0114).
//
// Before A165 the floor shielded by exclusion — every activity that was not a
// task or a note — so a fixture testing it needed no deal at all. The floor now
// covers correspondence about an actual commercial transaction, so a test that
// wants a shielded record has to supply the transaction. A fixture that skips
// it does not test a weaker floor; it tests the erasure path, because the
// records go.
//
// The deal is written directly rather than through the store because the store
// stamps the correspondence itself on the winning transition, and a fixture
// that used it would prove the stamp works by using the stamp.
func (e *Env) SeedWonDealLinkedTo(t *testing.T, activities ...ids.UUID) ids.UUID {
	t.Helper()
	pipeline, stage, deal := ids.NewV7(), ids.NewV7(), ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `INSERT INTO pipeline (id, name, is_default, position)
			VALUES ($1, 'Floor fixture', false, 90)`, pipeline); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability)
			VALUES ($1, $2, 'Closed Won', 0, 'won', 100)`, stage, pipeline); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deal (id, name, status, pipeline_id, stage_id, closed_at, source, captured_by)
			VALUES ($1, 'Floor fixture deal', 'won', $2, $3, now(), 'manual', 'human:x')`,
			deal, pipeline, stage); err != nil {
			return err
		}
		for _, a := range activities {
			if _, err := tx.Exec(ctx, `INSERT INTO activity_link (activity_id, entity_type, deal_id)
				VALUES ($1, 'deal', $2)`, a, deal); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seeding the qualifying deal: %v", err)
	}
	return deal
}
