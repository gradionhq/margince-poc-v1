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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// seatConnectorOrg plants one organization the Gmail CONNECTOR created — the
// only creator these cases need, because the whole point is that an AI wrote
// into a record it did not create.
func seatConnectorOrg(t *testing.T, e *integration.Env, name, nameSource string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, owner_id, display_name, name_source, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, 'test', 'connector:gmail')`, id, e.WS, e.Rep1, name, nameSource)
		return err
	}); err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
	return id
}

// agentCtx is an agent identity on a system principal — the shape every AI task
// in this product runs under (the deep-read worker, the counterparty verdict).
// actor_type is 'system'; the identity that matters is actor_id.
func agentCtx(e *integration.Env) context.Context {
	return principal.WithActor(e.As(e.Rep1, nil, integration.AdminPerms), principal.Principal{
		Type: principal.PrincipalSystem, ID: "agent:deepread", UserID: e.Rep1, OnBehalfOf: e.Rep1,
	})
}

// The case a record-level provenance filter gets wrong, and the reason
// ai_written exists. Gmail capture mints the organization, so captured_by says
// `connector:gmail` — and then the AI writes into it. Asking "who created it"
// answers `connector` and hides exactly the record somebody needs to check.
//
// Every write below goes through the real store, so the audit rows the
// predicate reads are the ones production would leave. Seeding the underlying
// rows by hand would prove the query can read a table and nothing about whether
// the system fills it.
func TestAiWrittenFindsRecordsTheConnectorMadeAndTheAiFilled(t *testing.T) {
	e := integration.Setup(t)
	adminCtx := e.As(e.Rep1, nil, integration.AdminPerms)
	store := people.NewStore(e.Pool)

	filled := seatConnectorOrg(t, e, "Acme Filled", "domain")
	industry := "Robotics"
	if _, err := store.UpdateOrganization(agentCtx(e), ids.From[ids.OrganizationKind](filled),
		people.UpdateOrganizationInput{Industry: &industry}); err != nil {
		t.Fatalf("agent enrichment write: %v", err)
	}
	// Connector-made, connector-named, no AI ever near it.
	seatConnectorOrg(t, e, "Gamma Untouched", "domain")

	names := func(ai *bool) []string {
		t.Helper()
		got, _, err := store.ListOrganizations(adminCtx, people.ListOrganizationsInput{AiWritten: ai})
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
	if touched := names(&yes); !has(touched, "Acme Filled") || has(touched, "Gamma Untouched") {
		t.Fatalf("ai_written=true returned %v, want the connector-made org an AI wrote into and not the untouched one", touched)
	}
	// The complement is the complement.
	if untouched := names(&no); !has(untouched, "Gamma Untouched") || has(untouched, "Acme Filled") {
		t.Fatalf("ai_written=false returned %v, want exactly the records ai_written=true did not", untouched)
	}

	// And the record-level filter still answers its own, narrower question:
	// neither of these was CREATED by an AI.
	agent := "agent"
	if got, _, err := store.ListOrganizations(adminCtx,
		people.ListOrganizationsInput{CapturedByKind: &agent}); err != nil || len(got) != 0 {
		t.Fatalf("captured_by_kind=agent returned %d orgs (err %v), want 0 — the connector created both", len(got), err)
	}
}

// The plainest case there is, and the one every narrower predicate missed: an
// agent updates an ORDINARY column.
//
// The update is driven through the real store under a real agent principal —
// not by seeding a provenance row — because the earlier version of this test
// inserted the row it then asserted on, which proved the query could read a
// table and nothing about whether the system ever writes it.
func TestAiWrittenCatchesAnAgentUpdatingAnOrdinaryColumn(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	org := seatConnectorOrg(t, e, "Delta Industries", "domain")

	industry := "Robotics"
	if _, err := store.UpdateOrganization(agentCtx(e), ids.From[ids.OrganizationKind](org),
		people.UpdateOrganizationInput{Industry: &industry}); err != nil {
		t.Fatalf("agent update: %v", err)
	}

	yes := true
	got, _, err := store.ListOrganizations(e.As(e.Rep1, nil, integration.AdminPerms),
		people.ListOrganizationsInput{AiWritten: &yes})
	if err != nil {
		t.Fatalf("ListOrganizations: %v", err)
	}
	for _, o := range got {
		if o.DisplayName == "Delta Industries" {
			return
		}
	}
	t.Fatalf("ai_written=true returned %v, missing the org whose ordinary column an agent updated — a review list that only knows about enrichment tables is not a review list", got)
}
