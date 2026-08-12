// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/installseam"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// DB is the harness's pool bound to THIS env's workspace.
//
// Pinned rather than resolved: the cross-tenant suites seed a second workspace
// on purpose, and a handle that asked identity which one the installation is
// would refuse them outright (ErrMultipleWorkspaces) — taking with it the only
// mechanical proof that one tenant cannot read another, which ADR-0091 §9
// step 3 keeps green precisely to show this collapse was faithful.
func (e *Env) DB() *database.DB {
	return database.BindTo(e.Pool, ids.From[ids.WorkspaceKind](e.WS))
}

// DBFor pins a handle to another workspace, for the cross-tenant suites.
//
// In the collapsed plumbing (ADR-0091 §9 step 3) "which tenant am I" is a
// property of the HANDLE, not of the context — so a suite proving one tenant
// cannot read another builds a store per tenant rather than calling one store
// with a second tenant's ctx. The assertion is unchanged and still RLS's:
// a store bound to B must not resolve A's row.
func (e *Env) DBFor(ws ids.UUID) *database.DB {
	return database.BindTo(e.Pool, ids.From[ids.WorkspaceKind](ws))
}

// harnessDB pins a pool to a workspace at Setup time, before the Env exists to
// carry it.
func harnessDB(pool *pgxpool.Pool, ws ids.UUID) *database.DB {
	return database.BindTo(pool, ids.From[ids.WorkspaceKind](ws))
}

// DealsFor and PeopleFor are the harness stores of ANOTHER workspace, for the
// cross-tenant suites that seed a second tenant and then drive it through the
// real writer.
//
// The workspace a store writes is a property of its handle, so seeding tenant B
// through the harness's own store would stamp B's ids into A's bound
// transaction and be refused by RLS — the loud version of the silent
// cross-tenant write these suites exist to deny.
func (e *Env) DealsFor(ws ids.UUID) *deals.Store {
	return deals.NewStore(e.DBFor(ws), installseam.Deals())
}

// PeopleFor is DealsFor for the people module; see its doc for why the second
// tenant needs a store of its own.
func (e *Env) PeopleFor(ws ids.UUID) *people.Store {
	return people.NewStore(e.DBFor(ws))
}
