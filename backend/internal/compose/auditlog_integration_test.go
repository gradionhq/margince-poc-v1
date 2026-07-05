// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The audit-log governance read (GET /audit-log, feedback/13): admin
// (unbounded human) reads the workspace trail newest-first with live
// filters and a stable keyset walk; a bounded rep and an agent
// principal are refused outright — the surface never narrows to a
// misleading partial view.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestAuditLogReadRequiresUnboundedHuman(t *testing.T) {
	e := setupAuthz(t)

	e.seedPerson(t, "Audit Subject", nil)

	// A bounded rep is refused — 403, not a narrowed page.
	repCtx := e.as(e.rep1, []ids.UUID{e.team1}, repPerms)
	if _, err := privacy.ListAuditLog(repCtx, e.pool, privacy.AuditFilter{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("bounded rep reads audit log: err=%v, want permission denied", err)
	}

	// An agent principal is refused even with unbounded grants: the
	// agent gate only fronts mutating routes, so the human-only rule
	// binds at the store.
	agentCtx := principal.WithWorkspaceID(t.Context(), e.ws)
	agentCtx = principal.WithCorrelationID(agentCtx, ids.NewV7())
	agentCtx = principal.WithActor(agentCtx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + ids.NewV7().String(),
		UserID: e.rep1, Permissions: adminPerms,
	})
	if _, err := privacy.ListAuditLog(agentCtx, e.pool, privacy.AuditFilter{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent reads audit log: err=%v, want permission denied", err)
	}

	// The unbounded human admin reads it.
	page, err := privacy.ListAuditLog(e.admin(), e.pool, privacy.AuditFilter{})
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("admin sees an empty audit log after a mutation")
	}
}

func TestAuditLogFiltersAndKeysetWalk(t *testing.T) {
	e := setupAuthz(t)

	var personIDs []ids.UUID
	for _, name := range []string{"One", "Two", "Three", "Four", "Five"} {
		personIDs = append(personIDs, e.seedPerson(t, name, nil))
	}
	admin := e.admin()

	// Filter: only person creates, and only the one entity.
	action := "create"
	entityType := "person"
	page, err := privacy.ListAuditLog(admin, e.pool, privacy.AuditFilter{
		Action: &action, EntityType: &entityType, EntityID: &personIDs[2],
	})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entity filter returned %d rows, want 1", len(page.Entries))
	}
	if page.Entries[0].EntityID == nil || *page.Entries[0].EntityID != personIDs[2] {
		t.Fatalf("entity filter returned the wrong row: %+v", page.Entries[0])
	}

	// Keyset walk: pages never overlap, order is newest-first, and the
	// walk terminates.
	limit := 2
	seen := map[ids.UUID]bool{}
	var cursor *string
	for range 10 {
		page, err := privacy.ListAuditLog(admin, e.pool, privacy.AuditFilter{
			EntityType: &entityType, Limit: &limit, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		for i, entry := range page.Entries {
			if seen[entry.ID] {
				t.Fatalf("cursor walk revisited audit row %s", entry.ID)
			}
			seen[entry.ID] = true
			if i > 0 {
				prev := page.Entries[i-1]
				if entry.OccurredAt.After(prev.OccurredAt) {
					t.Fatal("page is not newest-first")
				}
			}
		}
		if !page.HasMore {
			break
		}
		cursor = &page.NextCursor
	}
	if len(seen) < len(personIDs) {
		t.Fatalf("walk saw %d person audit rows, want at least %d", len(seen), len(personIDs))
	}

	// A malformed cursor is a client fault, not a 500.
	bad := "not-a-cursor"
	if _, err := privacy.ListAuditLog(admin, e.pool, privacy.AuditFilter{Cursor: &bad}); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}
