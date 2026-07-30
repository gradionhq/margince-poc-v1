// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// What the entity filter must SPELL, per entity type.
//
// The company timeline is the one asymmetric case: an account is reached
// through three links, so filtering on it emits the OrgLinkedActivityExists
// walk instead of a flat activity_link join. Every other entity type is
// reached by its own link and only its own, so it keeps the join — and the
// row scope is composed either way, which is what keeps the wider account
// predicate from widening who may read.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// unboundedCtx binds an admin whose row scope is all, so
// auth.ActivityScopeClause contributes nothing and the assertions below are
// about the entity filter alone.
func unboundedCtx() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:admin", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

// teamScopedCtx binds a rep bounded to one team, so the row scope clause is
// non-empty and its presence can be asserted.
func teamScopedCtx() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:rep", UserID: ids.NewV7(),
		TeamIDs: []ids.UUID{ids.NewV7()},
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Read: true},
			},
			RowScope: principal.RowScopeTeam,
		},
	})
}

// filterFor builds the timeline filter for one entity type, failing the test
// on any error.
func filterFor(ctx context.Context, t *testing.T, entityType string) (join string, where string, args []any) {
	t.Helper()
	entity := ids.NewV7()
	joined, terms, args, err := listActivitiesFilter(ctx, ListActivitiesInput{
		EntityType: &entityType, EntityID: &entity,
	})
	if err != nil {
		t.Fatalf("building the %s filter: %v", entityType, err)
	}
	return joined, strings.Join(terms, " AND "), args
}

func TestOrganizationFilterWalksTheAccountsThreeLinks(t *testing.T) {
	join, where, args := filterFor(unboundedCtx(), t, "organization")

	if join != "" {
		t.Errorf("organization filter joined %q; a join multiplies an activity "+
			"reachable through two links into two rows and breaks the keyset cursor", join)
	}
	if !strings.Contains(where, "EXISTS (") {
		t.Fatalf("organization filter is not an EXISTS: %s", where)
	}
	for _, arm := range []string{"l.organization_id = $1", "d.organization_id = $1", "r.organization_id = $1"} {
		if !strings.Contains(where, arm) {
			t.Errorf("organization filter misses the %q arm: %s", arm, where)
		}
	}
	if !strings.Contains(where, "r.kind = 'employment'") ||
		!strings.Contains(where, "r.ended_at IS NULL") {
		t.Errorf("the employment arm must be live employment only: %s", where)
	}
	// One bind position, read by all three arms: an account id registered
	// three times would silently mis-number every later placeholder.
	if len(args) != 1 {
		t.Errorf("organization filter bound %d args, want 1 (the account id): %v", len(args), args)
	}
}

func TestEveryOtherEntityTypeKeepsItsFlatLinkJoin(t *testing.T) {
	for entityType, column := range map[string]string{
		"person":  "al.person_id",
		"deal":    "al.deal_id",
		"lead":    "al.lead_id",
		"project": "al.project_id",
	} {
		join, where, args := filterFor(unboundedCtx(), t, entityType)
		if join != " JOIN activity_link al ON al.activity_id = a.id" {
			t.Errorf("%s filter join = %q, want the flat activity_link join", entityType, join)
		}
		if !strings.Contains(where, "al.entity_type = $1") {
			t.Errorf("%s filter does not pin the link's entity_type: %s", entityType, where)
		}
		if !strings.Contains(where, column+" = $2") {
			t.Errorf("%s filter does not match %s: %s", entityType, column, where)
		}
		if strings.Contains(where, "r.kind = 'employment'") {
			t.Errorf("%s filter walks the account's employment arm; only an account does", entityType)
		}
		if len(args) != 2 {
			t.Errorf("%s filter bound %d args, want 2: %v", entityType, len(args), args)
		}
	}
}

// The account walk widens WHICH activities belong to the account. It must not
// widen WHO may read one, so the row-scope clause is still composed next to it.
func TestTheAccountWalkStillCarriesTheRowScope(t *testing.T) {
	_, where, _ := filterFor(teamScopedCtx(), t, "organization")
	if !strings.Contains(where, "activity_link nl") {
		t.Errorf("the row-scope link-walk is missing from a bounded caller's account filter: %s", where)
	}
	// The employment arm is the walk's own token: the row-scope clause also
	// mentions l.organization_id, so matching on that would pass vacuously.
	if !strings.Contains(where, "r.kind = 'employment'") {
		t.Errorf("the account walk is missing for a bounded caller: %s", where)
	}
}

func TestAnUnknownEntityTypeIsRefusedRatherThanBuiltIntoSQL(t *testing.T) {
	entityType, entity := "invoice", ids.NewV7()
	_, _, _, err := listActivitiesFilter(unboundedCtx(), ListActivitiesInput{
		EntityType: &entityType, EntityID: &entity,
	})
	var badType *InvalidLinkTypeError
	if !errors.As(err, &badType) {
		t.Fatalf("filtering on an unknown entity type → %v, want InvalidLinkTypeError", err)
	}
	if badType.EntityType != entityType {
		t.Errorf("refusal names %q, want %q", badType.EntityType, entityType)
	}
}
