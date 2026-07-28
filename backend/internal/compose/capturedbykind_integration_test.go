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
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

// Authorization outranks the parameter check. A caller who may not read this
// object must get the authorization answer whatever they typed — if a bad enum
// value answered first, the endpoint would tell an unauthorized caller which
// values it accepts, and confirm the object exists while doing it.
func TestCapturedByKindIsRefusedOnlyAfterAuthorization(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)

	// A rep may read people but NOT organizations, so the organization list is
	// the natural unauthorized caller here.
	bogus := "not-a-kind"
	_, _, err := store.ListOrganizations(e.As(e.Rep1, nil, integration.RepPerms),
		people.ListOrganizationsInput{CapturedByKind: &bogus})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("ListOrganizations err = %v, want the permission denial — the enum check must not answer before authorization", err)
	}

	// With the read granted, the same value is refused on its own merits.
	_, _, err = store.ListOrganizations(e.As(e.Rep1, nil, integration.AdminPerms),
		people.ListOrganizationsInput{CapturedByKind: &bogus})
	if err == nil {
		t.Fatal("an unknown provenance kind was accepted once the caller could read")
	}
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("ListOrganizations err = %v, want the validation refusal for an authorized caller", err)
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

// seatOrgCapturedBy plants one organization with an explicit creator.
func seatOrgCapturedBy(t *testing.T, e *integration.Env, name, capturedBy, nameSource string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, owner_id, display_name, name_source, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, 'test', $6)`, id, e.WS, e.Rep1, name, nameSource, capturedBy)
		return err
	}); err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
	return id
}

// The case a record-level provenance filter gets wrong, and the reason
// ai_written exists. Gmail capture mints the organization, so captured_by says
// `connector:gmail` — and then the AI renames it from a signature and fills its
// profile. Asking "who created it" answers `connector` and hides exactly the
// record somebody needs to check.
func TestAiWrittenFindsRecordsTheConnectorMadeAndTheAiFilled(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	store := people.NewStore(e.Pool)

	filled := seatOrgCapturedBy(t, e, "Acme Filled", "connector:gmail", "domain")
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_profile_field
				(workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1, $2, 'icp', 'RevOps leaders', 'we serve RevOps leaders', 'https://acme.example', 0.9, 'site_read', 'agent:deepread')`,
			e.WS, filled)
		return err
	}); err != nil {
		t.Fatalf("seeding the AI-written field: %v", err)
	}
	// Renamed by the AI from an email signature: the value is on the record
	// itself, not in a child row.
	seatOrgCapturedBy(t, e, "Beta Robotics GmbH", "connector:gmail", "signature")
	// Neither: connector-made, connector-named, no AI anywhere near it.
	seatOrgCapturedBy(t, e, "Gamma Untouched", "connector:gmail", "domain")

	names := func(ai *bool) []string {
		t.Helper()
		got, _, err := store.ListOrganizations(ctx, people.ListOrganizationsInput{AiWritten: ai})
		if err != nil {
			t.Fatalf("ListOrganizations: %v", err)
		}
		out := make([]string, 0, len(got))
		for _, o := range got {
			out = append(out, o.DisplayName)
		}
		return out
	}
	has := func(list []string, want string) bool {
		for _, n := range list {
			if n == want {
				return true
			}
		}
		return false
	}

	yes, no := true, false
	touched := names(&yes)
	if !has(touched, "Acme Filled") {
		t.Errorf("ai_written=true returned %v, missing the connector-made org whose profile an AI wrote", touched)
	}
	if !has(touched, "Beta Robotics GmbH") {
		t.Errorf("ai_written=true returned %v, missing the org an AI renamed from a signature", touched)
	}
	if has(touched, "Gamma Untouched") {
		t.Errorf("ai_written=true returned %v — it includes an org no AI ever wrote to", touched)
	}

	// The complement is the complement: exactly the records the first list left.
	untouched := names(&no)
	if !has(untouched, "Gamma Untouched") || has(untouched, "Acme Filled") || has(untouched, "Beta Robotics GmbH") {
		t.Errorf("ai_written=false returned %v, want exactly the records ai_written=true did not", untouched)
	}

	// And the record-level filter still answers its own, narrower question:
	// none of these was CREATED by an AI.
	agent := "agent"
	if got, _, err := store.ListOrganizations(ctx,
		people.ListOrganizationsInput{CapturedByKind: &agent}); err != nil || len(got) != 0 {
		t.Fatalf("captured_by_kind=agent returned %d orgs (err %v), want 0 — the connector created every one of these", len(got), err)
	}
}
