// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The row fixtures every suite seeds from, split out of harness.go when that
// file crossed the file-length cap (gradionhq/margince-poc-v1#2163).
//
// One concept: put a row in the database and answer its id. Env construction,
// the principal builders and the workspace-bound exec helpers stay next door —
// a suite reaches for exactly one of those three things at a time, and the
// split is along that grain rather than at whatever line the cap fell on.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

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
