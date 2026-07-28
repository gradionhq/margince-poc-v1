// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The provenance filter that makes AI-created records findable
// (ADR-0075/A121 §3a): captured_by_kind=agent IS the review list, the filter
// never becomes the only view, and it composes with row scope rather than
// widening it.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seatPersonCapturedBy plants one person with an explicit creator, which is the
// only thing this filter reads.
func seatPersonCapturedBy(t *testing.T, e *integration.Env, fullName, capturedBy string) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, $4, 'test', $5)`, ids.NewV7(), e.WS, e.Rep1, fullName, capturedBy)
		return err
	}); err != nil {
		t.Fatalf("seeding %s: %v", fullName, err)
	}
}

func TestCapturedByKindSelectsWhoCreatedTheRecord(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	store := people.NewStore(e.Pool)

	// One of each creator the write paths stamp, plus one whose prefix is in no
	// enum value at all — the case that decides whether a filter can quietly
	// become the only view.
	seatPersonCapturedBy(t, e, "Agent Made", "agent:capture_counterparty_verdict")
	seatPersonCapturedBy(t, e, "Human Made", "human:"+e.Rep1.String())
	seatPersonCapturedBy(t, e, "Connector Made", "connector:gmail")
	seatPersonCapturedBy(t, e, "System Made", "system:migration-0105")
	seatPersonCapturedBy(t, e, "Unclassified", "legacy-import")

	names := func(kind *string) []string {
		t.Helper()
		got, _, err := store.ListPeople(ctx, people.ListPeopleInput{CapturedByKind: kind})
		if err != nil {
			t.Fatalf("ListPeople: %v", err)
		}
		out := make([]string, 0, len(got))
		for _, p := range got {
			out = append(out, p.FullName)
		}
		return out
	}
	only := func(list []string, want string) bool { return len(list) == 1 && list[0] == want }

	agent := "agent"
	if got := names(&agent); !only(got, "Agent Made") {
		t.Fatalf("captured_by_kind=agent returned %v, want exactly the AI-created record", got)
	}
	for kind, want := range map[string]string{
		"human":     "Human Made",
		"connector": "Connector Made",
		"system":    "System Made",
	} {
		if got := names(&kind); !only(got, want) {
			t.Errorf("captured_by_kind=%s returned %v, want exactly %q", kind, got, want)
		}
	}

	// The unfiltered list is the complete one. A row whose prefix matches no
	// enum value is reachable there and only there — a filter that dropped it
	// from BOTH views would hide records nobody could then find.
	if got := names(nil); len(got) != 5 {
		t.Fatalf("the unfiltered list returned %d rows, want all 5 including the unclassified one: %v", len(got), got)
	}
}

func TestCapturedByKindNarrowsRowScopeAndNeverWidensIt(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)

	// An AI-created person owned by Rep3, who sits in the other team.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, 'Other Team AI Record', 'test', 'agent:capture_counterparty_verdict')`,
			ids.NewV7(), e.WS, e.Rep3)
		return err
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// Rep1 asks for the review list on his own scope. The filter selects WHICH
	// rows of what he may already see; it is not a way to see more.
	agent := "agent"
	got, _, err := store.ListPeople(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.RepPerms),
		people.ListPeopleInput{CapturedByKind: &agent})
	if err != nil {
		t.Fatalf("ListPeople: %v", err)
	}
	for _, p := range got {
		if p.FullName == "Other Team AI Record" {
			t.Fatal("the provenance filter returned a record outside the caller's row scope — a filter must narrow, never widen")
		}
	}
}
