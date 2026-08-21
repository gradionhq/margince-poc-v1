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
	if _, err := e.People.UpdatePerson(reader, PersonIDOf(target), people.UpdatePersonInput{Title: strPtr("CEO")}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("read_only update → %v, want ErrPermissionDenied", err)
	}
	if _, err := e.People.ArchivePerson(reader, PersonIDOf(target), nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("read_only archive → %v, want ErrPermissionDenied", err)
	}
	// …but reading is granted, and row_scope=all sees the foreign-owned row.
	if _, err := e.People.GetPerson(reader, PersonIDOf(target), storekit.LiveOnly); err != nil {
		t.Errorf("read_only get → %v, want success", err)
	}

	// A rep (no delete grant on person) cannot archive even an OWN record:
	// object-level denial precedes row scope.
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	if _, err := e.People.ArchivePerson(rep, PersonIDOf(target), nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("rep archive own → %v, want ErrPermissionDenied", err)
	}
}

// Contacts are an identity table: every seat that holds person.read reads
// every contact, whichever team owns it, and the team row scope binds only
// WRITES. The one contact a seat cannot read is a capture-private one
// belonging to somebody else — and that one answers 404, never a 403 that
// would disclose its existence.
func TestRowScopeTeamReadsEveryContactButWritesOnlyItsOwnTeams(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Mine", &e.Rep1)
	teammates := e.SeedPerson(t, "Teammates", &e.Rep2)
	foreign := e.SeedPerson(t, "Foreign", &e.Rep3)
	shared := e.SeedPerson(t, "Shared", nil)
	private := e.SeedPerson(t, "Their Private Capture", &e.Rep3)
	e.MakeCapturePrivate(t, "person", private, e.Rep3)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)

	rows, _, err := e.People.ListPeople(rep, people.ListPeopleInput{})
	if err != nil {
		t.Fatal(err)
	}
	visible := map[ids.UUID]bool{}
	for _, p := range rows {
		visible[ids.UUID(p.Id)] = true
	}
	for id, want := range map[ids.UUID]bool{mine: true, teammates: true, shared: true, foreign: true, private: false} {
		if visible[id] != want {
			t.Errorf("team-scoped list visibility of %s = %v, want %v", id, visible[id], want)
		}
	}

	// Single fetch: the other team's contact is readable; the private one
	// answers 404 — never the row, and never a 403 that would disclose it.
	if _, err := e.People.GetPerson(rep, PersonIDOf(foreign), storekit.LiveOnly); err != nil {
		t.Errorf("get another team's contact → %v, want success", err)
	}
	if _, err := e.People.GetPerson(rep, PersonIDOf(private), storekit.LiveOnly); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("get another rep's private capture → %v, want ErrNotFound", err)
	}
	// Writes keep the team scope: the readable foreign row is refused, the
	// hidden one stays hidden.
	if _, err := e.People.UpdatePerson(rep, PersonIDOf(foreign), people.UpdatePersonInput{Title: strPtr("Pwned")}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("update another team's contact → %v, want ErrPermissionDenied", err)
	}
	if _, err := e.People.UpdatePerson(rep, PersonIDOf(private), people.UpdatePersonInput{Title: strPtr("Pwned")}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("update another rep's private capture → %v, want ErrNotFound", err)
	}
	if _, err := e.People.UpdatePerson(rep, PersonIDOf(teammates), people.UpdatePersonInput{Title: strPtr("Lead")}); err != nil {
		t.Errorf("update a teammate's contact → %v, want success", err)
	}

	// The private capture's owner sees all five; a stranger with row_scope=all
	// still sees only four, because capture privacy is not a row-scope tier.
	all, _, err := e.People.ListPeople(e.As(e.Rep3, []ids.UUID{e.Team2}, ReadOnlyPerms), people.ListPeopleInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Errorf("the private capture's owner with row_scope=all sees %d people, want 5", len(all))
	}
	stranger, _, err := e.People.ListPeople(e.As(ids.NewV7(), nil, ReadOnlyPerms), people.ListPeopleInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stranger) != 4 {
		t.Errorf("a stranger with row_scope=all sees %d people, want 4", len(stranger))
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
