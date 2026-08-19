// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestReadClassesPartitionTheShareableTables(t *testing.T) {
	// Every shareable table is exactly one of identity or commercial, and the
	// tables carrying capture privacy are identity tables — the one narrowing
	// that survives the shared-identity read.
	for table := range shareableTables {
		if identityTables[table] == commercialTables[table] {
			t.Errorf("%s is in neither or both read classes", table)
		}
	}
	for table := range identityTables {
		if !shareableTables[table] {
			t.Errorf("identity table %s is not shareable", table)
		}
	}
	for table := range commercialTables {
		if !shareableTables[table] {
			t.Errorf("commercial table %s is not shareable", table)
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

func TestCommercialTablesKeepTheRowScope(t *testing.T) {
	rep := human(principal.RowScopeOwn)
	if sql := rendered(rep, "project"); !strings.Contains(sql, "t.owner_id = $") {
		t.Errorf("project predicate for a rep lost the owner scope: %s", sql)
	}
	if UnboundedFor(rep, "project") {
		t.Error("UnboundedFor(rep, project) = true")
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
