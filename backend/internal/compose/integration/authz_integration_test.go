// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The authorization matrix (B-EP03.2/.3a, features/04 §1 AC): role ×
// object × action × ownership against the real migrated Postgres,
// exercised at the store layer — the one enforcement path HTTP and the
// future MCP surface both ride. Principals are constructed directly (the
// JSONB→Permissions loading path is covered by identity's policy tests).

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestObjectLevelRBACDeniesUngrantedActions(t *testing.T) {
	e := Setup(t)
	target := e.SeedPerson(t, "Target", &e.Rep1)

	reader := e.As(e.Rep3, []ids.UUID{e.Team2}, ReadOnlyPerms)

	if _, err := e.People.CreatePerson(reader, people.CreatePersonInput{FullName: "X", Source: "manual"}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("read_only create → %v, want ErrPermissionDenied", err)
	}
	if _, err := e.People.UpdatePerson(reader, target, people.UpdatePersonInput{Title: strPtr("CEO")}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("read_only update → %v, want ErrPermissionDenied", err)
	}
	if _, err := e.People.ArchivePerson(reader, target); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("read_only archive → %v, want ErrPermissionDenied", err)
	}
	// …but reading is granted, and row_scope=all sees the foreign-owned row.
	if _, err := e.People.GetPerson(reader, target, storekit.LiveOnly); err != nil {
		t.Errorf("read_only get → %v, want success", err)
	}

	// A rep (no delete grant on person) cannot archive even an OWN record:
	// object-level denial precedes row scope.
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	if _, err := e.People.ArchivePerson(rep, target); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("rep archive own → %v, want ErrPermissionDenied", err)
	}
}

func TestRowScopeTeamNeverShowsAnotherTeamsRecord(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Mine", &e.Rep1)
	teammates := e.SeedPerson(t, "Teammates", &e.Rep2)
	foreign := e.SeedPerson(t, "Foreign", &e.Rep3)
	shared := e.SeedPerson(t, "Shared", nil)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)

	rows, _, err := e.People.ListPeople(rep, people.ListPeopleInput{})
	if err != nil {
		t.Fatal(err)
	}
	visible := map[ids.UUID]bool{}
	for _, p := range rows {
		visible[ids.UUID(p.Id)] = true
	}
	for id, want := range map[ids.UUID]bool{mine: true, teammates: true, shared: true, foreign: false} {
		if visible[id] != want {
			t.Errorf("team-scoped list visibility of %s = %v, want %v", id, visible[id], want)
		}
	}

	// Single fetch: the foreign row answers 404 — never the row, and
	// never a 403 that would disclose its existence.
	if _, err := e.People.GetPerson(rep, foreign, storekit.LiveOnly); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("get another team's record → %v, want ErrNotFound", err)
	}
	// Nor can it be mutated blind by id.
	if _, err := e.People.UpdatePerson(rep, foreign, people.UpdatePersonInput{Title: strPtr("Pwned")}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("update another team's record → %v, want ErrNotFound", err)
	}

	// row_scope=all (read_only) sees all four.
	all, _, err := e.People.ListPeople(e.As(e.Rep3, []ids.UUID{e.Team2}, ReadOnlyPerms), people.ListPeopleInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Errorf("row_scope=all sees %d people, want 4", len(all))
	}
}

func TestMutationRecordsTheGoverningRuleInAuditLog(t *testing.T) {
	e := Setup(t)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	p, err := e.People.CreatePerson(rep, people.CreatePersonInput{FullName: "Audited", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}

	owner := OwnerConn(t)

	var rule string
	err = owner.QueryRow(context.Background(),
		`SELECT authorization_rule FROM audit_log WHERE entity_type = 'person' AND entity_id = $1 AND action = 'create'`,
		ids.UUID(p.Id)).Scan(&rule)
	if err != nil {
		t.Fatal(err)
	}
	if want := "role[rep] person.create row_scope=team"; rule != want {
		t.Errorf("authorization_rule = %q, want %q", rule, want)
	}
}

func TestZeroPermissionsFailClosed(t *testing.T) {
	e := Setup(t)
	nobody := e.As(ids.NewV7(), nil, principal.Permissions{})
	if _, _, err := e.People.ListPeople(nobody, people.ListPeopleInput{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("unresolved permissions list → %v, want ErrPermissionDenied (fail closed)", err)
	}
}

func strPtr(s string) *string { return &s }
