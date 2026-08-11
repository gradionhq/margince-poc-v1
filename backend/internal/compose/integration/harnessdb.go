// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
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
