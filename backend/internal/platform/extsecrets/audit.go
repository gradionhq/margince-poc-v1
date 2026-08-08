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

// What a read found. Only reads carry an outcome, and the reason is the
// asymmetry between the two failure modes:
//
// A read that found NOTHING is the interesting one. Probing key names a unit
// does not own is reconnaissance, it is read-shaped, and it is exactly what
// the rationale above says this ledger is for — a ledger that recorded only
// the reads that succeeded would answer "what did it get at?" with the one
// thing that did not happen. So a miss is committed, and the refusal is
// raised afterwards.
//
// A write that failed is not recorded, deliberately. A refused Put or Delete
// rolls its transaction back, and appending a row for it would mean the
// ledger asserts a secret changed hands when none did. A failed write is a
// caller bug or a lost race, not reconnaissance; it belongs in the error the
// caller already gets.
const (
	outcomeResolved = "resolved"
	outcomeMissing  = "missing"
	// outcomeTorn is the mapping row naming material the custodian does not
	// hold. It is recorded as well as alarmed on, so the ledger shows which
	// key was unreadable and when.
	outcomeTorn = "torn"
)

// audit appends a state-changing operation to system_log inside the caller's
// transaction, so a recorded secret change is one that actually committed.
//
// The detail names WHAT changed hands and never the material itself: the
// unit, the key, the scope, and — at user scope — whose. storekit.LogSystem
// needs a bound actor and a workspace-pinned transaction; both are the
// caller's obligation, and it refuses rather than guessing if either is
// missing.
func (s *store) audit(ctx context.Context, tx pgx.Tx, action string, user *ids.UserID, key string) error {
	_, err := storekit.LogSystem(ctx, tx, action, s.detail(user, key))
	return err
}

// auditRead appends a read, found or not, with what it found.
func (s *store) auditRead(ctx context.Context, tx pgx.Tx, user *ids.UserID, key, outcome string) error {
	detail := s.detail(user, key)
	detail["outcome"] = outcome
	_, err := storekit.LogSystem(ctx, tx, actionRead, detail)
	return err
}

// detail is the shared shape both ledger entries carry.
func (s *store) detail(user *ids.UserID, key string) map[string]any {
	detail := map[string]any{
		"extension": s.unit,
		"key":       key,
		"scope":     scopeWorkspace,
	}
	if user != nil {
		detail["scope"] = scopeUser
		detail["user_id"] = user.String()
	}
	return detail
}
