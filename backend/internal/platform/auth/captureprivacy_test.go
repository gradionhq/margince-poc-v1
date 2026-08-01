// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// Capture privacy as the predicate SPELLS it. A connector-created person or
// organization is written visibility='owner' (ADR-0063 §7) and belongs to
// the user whose mailbox produced it until a human promotes it. These tests
// pin the two properties that make that true:
//
//   - the visibility column is READ — a predicate that never mentions it
//     lets a team-scoped colleague read an unpromoted captured contact;
//   - row_scope=all does NOT clear it — capture privacy is a property of
//     the row, not a scope tier, so an admin faces it too.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// rendered builds the predicate for one table and returns its SQL, with the
// arg registrar the production callers use.
func rendered(p principal.Principal, table string) string {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	return VisiblePredicate(p, table, arg)("t")
}

func human(scope principal.RowScope) principal.Principal {
	return principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:rep", UserID: ids.NewV7(),
		TeamIDs:     []ids.UUID{ids.NewV7()},
		Permissions: principal.Permissions{RowScope: scope},
	}
}

func TestCapturePrivacyIsReadOnEveryTableThatCarriesIt(t *testing.T) {
	// The defect this pins: person and organization have carried a
	// visibility column since migration 0095, and the predicate never
	// consulted it — so under team scope an owner-private captured contact
	// was readable by the whole team.
	for _, table := range []string{"person", "organization"} {
		for _, scope := range []principal.RowScope{
			principal.RowScopeOwn, principal.RowScopeTeam, principal.RowScopeAll,
		} {
			sql := rendered(human(scope), table)
			if !strings.Contains(sql, "t.visibility <> 'owner'") {
				t.Errorf("%s predicate at row_scope=%s does not read the visibility "+
					"column, so an unpromoted captured row leaks: %s", table, scope, sql)
			}
			if !strings.Contains(sql, "t.owner_id = $") {
				t.Errorf("%s predicate at row_scope=%s never lets the capturing user "+
					"back in to their own private row: %s", table, scope, sql)
			}
		}
	}
}

func TestRowScopeAllDoesNotClearCapturePrivacy(t *testing.T) {
	// An admin reads every workspace-visible row and no owner-private one:
	// the founder decision is the importing user ONLY, not even Admin.
	admin := human(principal.RowScopeAll)

	if UnboundedFor(admin, "person") {
		t.Error("UnboundedFor(admin, person) = true: an admin would skip the " +
			"clause entirely and read every colleague's unpromoted contacts")
	}
	if !UnboundedFor(admin, "deal") {
		t.Error("UnboundedFor(admin, deal) = false: deal carries no visibility " +
			"column, so an admin still reads it unfiltered")
	}
	// Unbounded itself is an admission test several engines gate on and
	// must keep answering for the scope tier alone.
	if !Unbounded(admin) {
		t.Error("Unbounded(admin) = false: the admission test changed meaning")
	}
}

func TestOnlyTheSystemPrincipalReadsCapturePrivateTablesUnfiltered(t *testing.T) {
	// Provisioning, the outbox relay and the privacy engines run as system
	// and must see the whole estate; nothing else may.
	system := principal.Principal{
		Type: principal.PrincipalSystem, ID: "system",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	}
	if !UnboundedFor(system, "person", "organization") {
		t.Fatal("the system principal is filtered by capture privacy; " +
			"provisioning and the relay cannot see the rows they maintain")
	}
	if sql := rendered(system, "person"); sql != "TRUE" {
		t.Errorf("system person predicate = %q, want TRUE", sql)
	}
}

func TestAnExplicitShareStillWidensAnOwnerPrivateRow(t *testing.T) {
	// Sharing is a deliberate disclosure by someone who could already read
	// the row — the same human act promotion is. Scope never widens an
	// owner-private row; an explicit grant does, or the owner would share a
	// record the grantee then cannot see.
	sql := rendered(human(principal.RowScopeTeam), "person")
	if !strings.Contains(sql, "record_grant") {
		t.Fatalf("the grant arm is gone from the person predicate: %s", sql)
	}
	// The grant arm must sit OUTSIDE the capture-privacy arm, or a share of
	// an owner-private row would be conjoined away.
	privacyArm := strings.Index(sql, "t.visibility <> 'owner'")
	grantArm := strings.Index(sql, "record_grant")
	if privacyArm < 0 || grantArm < 0 || grantArm < privacyArm {
		t.Errorf("the grant arm does not follow the capture-privacy arm; a "+
			"shared owner-private row would stay hidden: %s", sql)
	}
}

func TestTablesWithoutCapturePrivacyAreUnchanged(t *testing.T) {
	// deal, lead and project carry no visibility column; adding the arm to
	// them would filter on a column that does not exist.
	for _, table := range []string{"deal", "lead", "project"} {
		sql := rendered(human(principal.RowScopeTeam), table)
		if strings.Contains(sql, "visibility") {
			t.Errorf("%s predicate reads a visibility column it does not have: %s", table, sql)
		}
	}
}

func TestTheOwnerPredicateIsTotal(t *testing.T) {
	// A caller that composes the predicate without asking UnboundedFor
	// first must get a WIDENING arm. row_scope=all matches neither the team
	// branch nor the own branch, so a non-total predicate would silently
	// narrow an admin to their own rows.
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	if sql := OwnerPredicate(human(principal.RowScopeAll), arg)("t"); sql != "TRUE" {
		t.Errorf("OwnerPredicate(row_scope=all) = %q, want TRUE — an all-scope "+
			"reader must not be narrowed to their own rows", sql)
	}
}

func TestSubjectRightsPredicateDropsOnlyTheCapturePrivacyArm(t *testing.T) {
	// Art. 15 and Art. 17 owe the subject everything the controller holds,
	// including an unpromoted capture. The crossing lifts capture privacy
	// and nothing else — the caller's own/team scope still binds, so a rep
	// with person.delete cannot erase a colleague's person.
	p := human(principal.RowScopeTeam)
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	sql := predicateFor(p, "person", arg, withoutCapturePrivacy)("t")

	if strings.Contains(sql, "visibility") {
		t.Errorf("the subject-rights predicate still applies capture privacy, "+
			"so a SAR would silently omit an unpromoted captured person: %s", sql)
	}
	if !strings.Contains(sql, "t.owner_id") {
		t.Errorf("the subject-rights predicate dropped the owner scope too; a rep "+
			"could reach a colleague's records: %s", sql)
	}
}
