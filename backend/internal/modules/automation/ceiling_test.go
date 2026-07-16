// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// actorCtx binds a human principal carrying exactly the object grants
// given — the same construction shape platform/auth's own tests use
// (admit_test.go), never a faked auth.Require.
func actorCtx(objects map[string]principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:test",
		Permissions: principal.Permissions{Objects: objects},
	})
}

func TestRequireAuthorCeiling(t *testing.T) {
	cases := []struct {
		name    string
		entry   CatalogEntry
		grants  map[string]principal.ObjectGrant
		wantErr bool
	}{
		{
			name:    "pinned action, author lacks the object's create grant",
			entry:   CatalogEntry{Key: "k", Action: string(ActionTypeCreateTask), Trigger: "deal.stage_changed"},
			grants:  map[string]principal.ObjectGrant{},
			wantErr: true,
		},
		{
			name:    "pinned action, author holds the object's create grant",
			entry:   CatalogEntry{Key: "k", Action: string(ActionTypeCreateTask), Trigger: "deal.stage_changed"},
			grants:  map[string]principal.ObjectGrant{"activity": {Create: true}},
			wantErr: false,
		},
		{
			name:    "target-scoped action, author lacks update on the resolved entity",
			entry:   CatalogEntry{Key: "assign_lead_owner", Action: string(ActionTypeAssignOwner), Trigger: "lead.created"},
			grants:  map[string]principal.ObjectGrant{"lead": {Read: true}}, // read, not update
			wantErr: true,
		},
		{
			name:    "target-scoped action, author holds update on the resolved entity",
			entry:   CatalogEntry{Key: "assign_lead_owner", Action: string(ActionTypeAssignOwner), Trigger: "lead.created"},
			grants:  map[string]principal.ObjectGrant{"lead": {Update: true}},
			wantErr: false,
		},
		{
			// Proves the check gates the RESOLVED object, not a fixed one:
			// holding update on "deal" (a plausible but wrong guess) must
			// not satisfy an automation whose trigger fired on "lead".
			name:    "target-scoped action, grant on the wrong entity does not satisfy",
			entry:   CatalogEntry{Key: "assign_lead_owner", Action: string(ActionTypeAssignOwner), Trigger: "lead.created"},
			grants:  map[string]principal.ObjectGrant{"deal": {Update: true}},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireAuthorCeiling(actorCtx(tc.grants), tc.entry)
			if tc.wantErr && !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Fatalf("got %v, want ErrPermissionDenied", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("got %v, want nil", err)
			}
		})
	}
}

// A catalog entry naming an action the registry doesn't define is a
// catalog misconfiguration, not a permission question — it must fail
// closed with a diagnosable error, never resolve to a silent pass.
func TestRequireAuthorCeilingFailsClosedOnUnregisteredAction(t *testing.T) {
	entry := CatalogEntry{Key: "bogus", Action: "not_a_real_action", Trigger: "lead.created"}
	err := requireAuthorCeiling(actorCtx(map[string]principal.ObjectGrant{}), entry)
	if err == nil {
		t.Fatal("unregistered action → nil error, want a non-nil misconfiguration error")
	}
}

// An entity-agnostic trigger (no dot-qualified entity) leaves the
// target-scoped shape nothing to resolve; this is the honest hard case
// where author-time enforcement stands down rather than guessing an
// object, deferring entirely to the match-time gate.
func TestRequireAuthorCeilingSkipsUnresolvableTargetScopedTrigger(t *testing.T) {
	entry := CatalogEntry{Key: "k", Action: string(ActionTypeAssignOwner), Trigger: "no_entity_here"}
	err := requireAuthorCeiling(actorCtx(map[string]principal.ObjectGrant{}), entry)
	if err != nil {
		t.Fatalf("unresolvable target-scoped trigger → %v, want nil (best-effort skip)", err)
	}
}

// Every seeded catalog entry's action must resolve — a catalog naming an
// action the registry does not define would be an author-time crash
// waiting to happen the moment someone tries to create it.
func TestSeededCatalogEntriesAllResolveAnActionDefinition(t *testing.T) {
	for _, entry := range Catalog() {
		if _, ok := ActionDefFor(ActionType(entry.Action)); !ok {
			t.Errorf("catalog entry %q names action %q, which the action registry does not define", entry.Key, entry.Action)
		}
	}
}
