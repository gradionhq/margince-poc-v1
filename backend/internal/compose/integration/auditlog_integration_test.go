// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The audit-log governance read (GET /audit-log): an admin human reads the
// workspace trail newest-first with live filters and a stable keyset walk.
// Everyone else is refused outright — a bounded rep, an agent principal, and
// the two roles that hold an unbounded row scope without holding the
// compliance authority. The surface never narrows to a misleading partial
// view.

import (
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestAuditLogReadRequiresAdminHuman(t *testing.T) {
	e := Setup(t)

	e.SeedPerson(t, "Audit Subject", nil)

	// A bounded rep is refused — 403, not a narrowed page.
	repCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	if _, err := privacy.ListAuditLog(repCtx, e.DB(), privacy.AuditFilter{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("bounded rep reads audit log: err=%v, want permission denied", err)
	}

	// An unbounded row scope is NOT the predicate. ops and read_only both seed
	// with scope `all` — pinned by policy's own suite — and the governance
	// matrix reserves the compliance read for the admin alone: it is oversight
	// of ops' own machine-origin actions, so it cannot sit with the role it
	// oversees. Ops carries the admin object grid here because production does,
	// which is what makes its refusal evidence about the ROLE rather than about
	// a missing grant.
	for _, unbounded := range []principal.Permissions{OpsPerms, ReadOnlyPerms} {
		ctx := e.As(ids.NewV7(), []ids.UUID{e.Team1}, unbounded)
		if _, err := privacy.ListAuditLog(ctx, e.DB(), privacy.AuditFilter{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("%v reads audit log: err=%v, want permission denied", unbounded.RoleKeys, err)
		}
	}

	// An agent principal is refused even with unbounded grants: the
	// agent gate only fronts mutating routes, so the human-only rule
	// binds at the store.
	agentCtx := principal.WithWorkspaceID(t.Context(), e.WS)
	agentCtx = principal.WithCorrelationID(agentCtx, ids.NewV7())
	agentCtx = principal.WithActor(agentCtx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + ids.NewV7().String(),
		UserID: e.Rep1, Permissions: AdminPerms,
	})
	if _, err := privacy.ListAuditLog(agentCtx, e.DB(), privacy.AuditFilter{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent reads audit log: err=%v, want permission denied", err)
	}

	// The unbounded human admin reads it.
	page, err := privacy.ListAuditLog(e.Admin(), e.DB(), privacy.AuditFilter{})
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("admin sees an empty audit log after a mutation")
	}
}

func TestAuditLogFiltersAndKeysetWalk(t *testing.T) {
	e := Setup(t)

	var personIDs []ids.UUID
	for _, name := range []string{"One", "Two", "Three", "Four", "Five"} {
		personIDs = append(personIDs, e.SeedPerson(t, name, nil))
	}
	admin := e.Admin()

	// Filter: only person creates, and only the one entity.
	action := "create"
	entityType := "person"
	page, err := privacy.ListAuditLog(admin, e.DB(), privacy.AuditFilter{
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
		page, err := privacy.ListAuditLog(admin, e.DB(), privacy.AuditFilter{
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
	if _, err := privacy.ListAuditLog(admin, e.DB(), privacy.AuditFilter{Cursor: &bad}); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}

// TestAuditLogResolvesTheHumanBehindEveryRow pins PD-002 on the compliance
// read: attribution names the PERSON, and an identifier is what a reader falls
// back to only when no person resolves. The screen this feeds is the one an
// auditor opens first, and "agent:01a01740-…" is not somebody who can be asked
// about a change.
//
// Three arms, because the read has three honest outcomes and the middle one is
// the trap: a human resolves to a name, a machine resolves to NO name while its
// granting human does, and an id no app_user matches resolves to nothing at all
// rather than to an invented or guessed name.
func TestAuditLogResolvesTheHumanBehindEveryRow(t *testing.T) {
	e := Setup(t)

	// Seeded through the real writer: SeedPerson goes via people.CreatePerson,
	// so the create row's actor_id is whatever storekit actually stamps for the
	// harness admin. A hand-inserted row would prove nothing about production —
	// the spelling of actor_id IS what this read has to match on.
	personID := e.SeedPerson(t, "Attribution Subject", nil)

	page, err := privacy.ListAuditLog(e.Admin(), e.DB(), privacy.AuditFilter{EntityID: &personID})
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("no audit row for a person the real writer just created")
	}

	var create *privacy.AuditEntry
	for i := range page.Entries {
		if page.Entries[i].Action == "create" {
			create = &page.Entries[i]
			break
		}
	}
	if create == nil {
		t.Fatalf("no create row among %d entries", len(page.Entries))
	}
	if create.ActorType != "human" {
		t.Fatalf("create row actor_type = %q, want human", create.ActorType)
	}
	// Every harness seat is seeded with display_name "Rep", so that is the
	// name the join must return. The point is that a name comes back AT ALL:
	// before this, the wire carried only the opaque 'human:<uuid>'.
	if create.ActorName == nil || *create.ActorName != "Rep" {
		t.Errorf("create row actor_name = %v, want the admin's resolved display name", create.ActorName)
	}
	if create.OnBehalfOfName != nil {
		t.Errorf("human row on_behalf_of_name = %v, want nil — a human acts for themselves",
			create.OnBehalfOfName)
	}

	// An agent row: no actor name (a machine has none), and the granting human
	// named. This is the inversion the issue is about — the passport uuid is
	// the qualifier, the person is the answer.
	ada := seedWorkspaceUser(t, e, "Ada Authority")
	seedRecordAuditRow(t, e, "update", personID, "agent",
		"agent:"+ids.NewV7().String(), &ada, nil, map[string]any{"title": "CTO"},
		time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond))

	// An actor_id no app_user can match: the honest-fallback arm. A read that
	// invented a name here would be worse than one that returns none.
	seedRecordAuditRow(t, e, "update", personID, "human", "human:"+ids.NewV7().String(), nil,
		nil, map[string]any{"title": "VP"},
		time.Now().Add(2*time.Hour).UTC().Truncate(time.Microsecond))

	page, err = privacy.ListAuditLog(e.Admin(), e.DB(), privacy.AuditFilter{EntityID: &personID})
	if err != nil {
		t.Fatalf("admin re-list: %v", err)
	}
	var sawAgent, sawUnresolvable bool
	for _, entry := range page.Entries {
		switch {
		case entry.ActorType == "agent":
			sawAgent = true
			if entry.ActorName != nil {
				t.Errorf("agent row actor_name = %v, want nil — a machine has no display name",
					entry.ActorName)
			}
			if entry.OnBehalfOfName == nil || *entry.OnBehalfOfName != "Ada Authority" {
				t.Errorf("agent row on_behalf_of_name = %v, want Ada Authority — the person answerable for it",
					entry.OnBehalfOfName)
			}
		case entry.ActorType == "human" && entry.ActorName == nil:
			sawUnresolvable = true
		}
	}
	if !sawAgent {
		t.Error("the seeded agent row never came back from the compliance read")
	}
	if !sawUnresolvable {
		t.Error("a human actor_id matching no app_user must resolve to no name, not be dropped")
	}
}
