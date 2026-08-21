// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestEveryShareableTableIsWorkspaceReadable(t *testing.T) {
	// Every record type a manual grant can widen is read by every seat today.
	// A record type that arrives scoped-read must say so here rather than land
	// silently: the write arm, not the read arm, is what keeps a row its
	// owner's. A table free of capture privacy renders the predicate away
	// entirely; person and organization keep an owner arm because an
	// owner-private capture still answers to its owner alone.
	rep := human(principal.RowScopeOwn)
	for table := range shareableTables {
		if !identityTables[table] {
			t.Errorf("shareable table %s is not workspace-readable; if that is deliberate, "+
				"say so here and pin the scoped predicate it keeps", table)
			continue
		}
		if ownerPrivateTables[table] {
			continue
		}
		if sql := rendered(rep, table); sql != "TRUE" {
			t.Errorf("%s predicate for a rep = %q, want TRUE — a table with no capture "+
				"privacy left has nothing to narrow a workspace read with", table, sql)
		}
	}
	for table := range identityTables {
		if !shareableTables[table] {
			t.Errorf("identity table %s is not shareable", table)
		}
	}
	for table := range ownerPrivateTables {
		if !identityTables[table] {
			t.Errorf("capture-private table %s is not an identity table", table)
		}
	}
}

func TestIdentityTablesAreReadByEverySeat(t *testing.T) {
	// A rep on own scope reads a colleague's deal and lead whole — the
	// predicate collapses to TRUE — and reads a colleague's person and
	// company unless capture privacy still holds the row.
	rep := human(principal.RowScopeOwn)
	for _, table := range []string{"deal", "lead"} {
		if sql := rendered(rep, table); sql != "TRUE" {
			t.Errorf("%s predicate for a rep = %q, want TRUE", table, sql)
		}
		if !UnboundedFor(rep, table) {
			t.Errorf("UnboundedFor(rep, %s) = false; list paths would still render a clause", table)
		}
	}
	for _, table := range []string{"person", "organization"} {
		sql := rendered(rep, table)
		if strings.Contains(sql, "t.owner_id IS NULL OR t.owner_id = $") {
			t.Errorf("%s predicate for a rep still carries the own-scope arm: %s", table, sql)
		}
		if !strings.Contains(sql, "t.visibility <> 'owner'") {
			t.Errorf("%s predicate for a rep dropped capture privacy: %s", table, sql)
		}
	}
}

func TestAProjectIsReadByEverySeat(t *testing.T) {
	// A consultant delivering a project they neither own nor were granted has
	// to reach it: the predicate collapses to TRUE and the list paths render
	// no clause at all. Nothing narrows a project read — it carries no
	// capture privacy either, since migration 1787320003 narrowed its
	// visibility CHECK to 'workspace'.
	rep := human(principal.RowScopeOwn)
	if sql := rendered(rep, "project"); sql != "TRUE" {
		t.Errorf("project predicate for a rep = %q, want TRUE — a rep 404s on the "+
			"project they are working but do not own", sql)
	}
	if !UnboundedFor(rep, "project") {
		t.Error("UnboundedFor(rep, project) = false; list paths would still narrow to the owner")
	}
}

func TestAProjectStaysTheOwnersToWrite(t *testing.T) {
	// The read widening must not reach the write arm: seeing a project is not
	// permission to edit one.
	rep := human(principal.RowScopeOwn)
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	sql := writeAuthorityPredicate(rep, "project", arg)
	if !strings.Contains(sql, "owner_id = $") || !strings.Contains(sql, "rg.access = 'write'") {
		t.Errorf("project write arm for a rep widened with the read class: %s", sql)
	}
}

func TestTheWriteArmIsUntouchedByTheReadClasses(t *testing.T) {
	// Shared read, scoped write: a rep's write authority over a deal is still
	// own/team scope or a write grant.
	rep := human(principal.RowScopeOwn)
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	sql := writeAuthorityPredicate(rep, "deal", arg)
	if !strings.Contains(sql, "owner_id = $") || !strings.Contains(sql, "rg.access = 'write'") {
		t.Errorf("deal write arm for a rep widened: %s", sql)
	}
}

func TestTheContentGateIsTheDiscoverGateNarrowedByAudience(t *testing.T) {
	rep := human(principal.RowScopeTeam)
	ctx := principal.WithActor(context.Background(), rep)
	// Each gate registers its own arguments, so the shared prefix renders
	// the same positions in both.
	freshArg := func() func(any) int {
		var args []any
		return func(v any) int { args = append(args, v); return len(args) }
	}
	arg := freshArg()
	discover, err := ActivityDiscoverClause(ctx, "a", freshArg())
	if err != nil {
		t.Fatal(err)
	}
	content, err := ActivityContentClause(ctx, "a", freshArg())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(content, discover) {
		t.Errorf("the content gate does not start from the discover gate:\n%s\nvs\n%s", content, discover)
	}
	for _, arm := range []string{"a.audience = 'workspace'", "a.captured_by LIKE $", "activity_participant ap", "activity_audience_member am"} {
		if !strings.Contains(content, arm) {
			t.Errorf("the content gate lacks the %q arm: %s", arm, content)
		}
		if strings.Contains(discover, arm) {
			t.Errorf("the discover gate carries the audience arm %q; a last-touch marker would hide a limited mail: %s", arm, discover)
		}
	}

	// The audience is a property of the row and does not yield to row_scope=all.
	admin := human(principal.RowScopeAll)
	adminContent, err := ActivityContentClause(principal.WithActor(context.Background(), admin), "a", arg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(adminContent, "a.audience = 'workspace'") {
		t.Errorf("an all-scope human skips the audience arm: %s", adminContent)
	}

	// Only the system principal reads the arm away.
	system := principal.Principal{Type: principal.PrincipalSystem, ID: "system",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll}}
	systemContent, err := ActivityContentClause(principal.WithActor(context.Background(), system), "a", arg)
	if err != nil {
		t.Fatal(err)
	}
	if systemContent != ActivityAvailableClause("a") {
		t.Errorf("system content gate = %q, want the availability test alone", systemContent)
	}
}

// The row-scope vocabulary's two arithmetic claims, pinned so they cannot rot
// into assertions nobody checks.
func TestTheOwnerScopedSetIsNotEveryOwnedTable(t *testing.T) {
	// Every shareable table must be row-scoped, or EnsureCanGrant answers
	// "not a row-scoped table" — a 500 — for a record type the product
	// deliberately allows sharing. The two sets are edited in different places
	// and nothing else notices when they drift apart.
	for table := range shareableTables {
		if !ownerScopedTables[table] {
			t.Errorf("%s is shareable but not row-scoped: EnsureCanGrant would fail it "+
				"as an unknown table rather than judging the caller's authority", table)
		}
	}

	// ownerScopedTables is deliberately SMALLER than "every table with an
	// owner_id". The comment on it states the numbers; this is what stops them
	// becoming folklore. attributionOnlyOwners are the tables whose owner_id
	// names the measured or attributed subject rather than an access owner —
	// quota's doc.go carries the ruling for the case that matters.
	attributionOnlyOwners := []string{
		"quota", "webhook_subscription", "capture_pending_counterparty",
		"organization_domain_disposition", "signal", "email_signature",
	}
	for _, table := range attributionOnlyOwners {
		if ownerScopedTables[table] {
			t.Errorf("%s carries owner_id as an ATTRIBUTION, not an access owner, and has "+
				"entered the row-scope vocabulary — on quota that silently narrows the "+
				"leaderboard read every seat is supposed to get", table)
		}
	}
	if got, want := len(ownerScopedTables), 9; got != want {
		t.Errorf("ownerScopedTables holds %d tables, want %d — if that is a deliberate "+
			"change, the count in its own comment moves with it", got, want)
	}
	if got, want := len(attributionOnlyOwners)+len(ownerScopedTables), 15; got != want {
		t.Errorf("the row-scoped set (%d) plus the attribution-only owners (%d) is %d, "+
			"want the 15 tables that carry owner_id — a new owner_id column belongs in "+
			"exactly one of the two, and choosing is the point",
			len(ownerScopedTables), len(attributionOnlyOwners), got)
	}
}
