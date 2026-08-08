// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extsecrets

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A secret changing hands moves no domain row, so there is no audit_log
// entry to attach it to; it belongs in system_log, the non-entity
// operational ledger — the same posture the boot's extension inventory
// takes (compose/extensioninventory.go).
//
// Reads are recorded alongside the writes. For an ordinary table that would
// be noise, but the question an operator asks after a unit is found to
// misbehave is "what did it get at?", and a ledger that only records stores
// answers it with the one thing that did not happen.
const (
	actionStored  = "extension.secret_stored"
	actionRotated = "extension.secret_rotated"
	actionDeleted = "extension.secret_deleted"
	actionRead    = "extension.secret_read"
)

// The two scope names the detail carries. They are the ledger's vocabulary,
// not the schema's — a reader should not have to know that "workspace" means
// user_id IS NULL.
const (
	scopeWorkspace = "workspace"
	scopeUser      = "user"
)

// audit appends the operation to system_log inside the caller's transaction,
// so a recorded secret change is one that actually committed.
//
// The detail names WHAT changed hands and never the material itself: the
// unit, the key, the scope, and — at user scope — whose. storekit.LogSystem
// needs a bound actor and a workspace-pinned transaction; both are the
// caller's obligation, and it refuses rather than guessing if either is
// missing.
func (s *store) audit(ctx context.Context, tx pgx.Tx, action string, user *ids.UserID, key string) error {
	detail := map[string]any{
		"extension": s.unit,
		"key":       key,
		"scope":     scopeWorkspace,
	}
	if user != nil {
		detail["scope"] = scopeUser
		detail["user_id"] = user.String()
	}
	_, err := storekit.LogSystem(ctx, tx, action, detail)
	return err
}
