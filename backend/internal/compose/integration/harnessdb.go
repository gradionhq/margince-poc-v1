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
