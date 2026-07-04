// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The features/01 §1.3 two-record merge acceptance criteria, against the
// real migrated Postgres: non-lossy relink with zero orphaned references,
// primary-slot demotion, fill-only survivorship, the restrictive consent
// rule, org hierarchy reparenting + the 1:1 partner extension, and the
// self / already-merged / dead-target error paths.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// wsExec runs one setup statement in a workspace-bound transaction (RLS is
// FORCED, so the GUC must be set even for the owner-less test pool).
func (e *authzEnv) wsExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	if err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}); err != nil {
		t.Fatalf("setup exec: %v", err)
	}
}

// wsCount returns a scalar count in a workspace-bound transaction.
func (e *authzEnv) wsCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	var n int
	if err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return n
}

func TestMergePerson_relinkSurvivorshipAndReferentialIntegrity(t *testing.T) {
	e := setupAuthz(t)
	admin := e.admin()

	firstAda := "Ada"
	source, err := e.people.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Ada Source", FirstName: &firstAda, Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "ada.work@x.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	target, err := e.people.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Ada Target", Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "ada.other@x.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	src, tgt := ids.UUID(source.Id), ids.UUID(target.Id)

	survivor, err := e.people.MergePerson(admin, src, tgt)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if ids.UUID(survivor.Id) != tgt {
		t.Fatalf("survivor = %s, want the target %s", survivor.Id, tgt)
	}

	// Referential integrity: nothing LIVE still points at the merged-away
	// source (the source row itself keeps merged_into_id, that is the
	// redirect and expected).
	if n := e.wsCount(t, `SELECT count(*) FROM person_email WHERE person_id = $1`, src); n != 0 {
		t.Errorf("%d emails still point at the merged-away source", n)
	}
	if n := e.wsCount(t, `SELECT count(*) FROM person_email WHERE person_id = $1`, tgt); n != 2 {
		t.Errorf("survivor has %d emails, want both relinked", n)
	}
	// Primary demotion: exactly one primary work email survives on B.
	if n := e.wsCount(t, `SELECT count(*) FROM person_email WHERE person_id = $1 AND email_type = 'work' AND is_primary AND archived_at IS NULL`, tgt); n != 1 {
		t.Errorf("survivor has %d primary work emails, want exactly 1 (A's must demote)", n)
	}

	// Fill-only survivorship: B had no first_name, so it takes A's.
	after, err := e.people.GetPerson(admin, tgt, false)
	if err != nil {
		t.Fatalf("read survivor: %v", err)
	}
	if after.FirstName == nil || *after.FirstName != "Ada" {
		t.Errorf("survivor first_name = %v, want the filled 'Ada'", after.FirstName)
	}

	// The source is archived with a one-hop redirect and no longer live.
	if _, err := e.people.GetPerson(admin, src, false); err == nil {
		t.Error("merged-away source still reads as live")
	}
	archived, err := e.people.GetPerson(admin, src, true)
	if err != nil {
		t.Fatalf("read archived source: %v", err)
	}
	if archived.MergedIntoId == nil || ids.UUID(*archived.MergedIntoId) != tgt {
		t.Errorf("source merged_into_id = %v, want the target %s", archived.MergedIntoId, tgt)
	}
}

func TestMergePerson_consentMergesRestrictively(t *testing.T) {
	e := setupAuthz(t)
	admin := e.admin()

	source := e.seedPerson(t, "Consent Source", nil)
	target := e.seedPerson(t, "Consent Target", nil)

	// One shared purpose; the source WITHDREW, the target GRANTED. The
	// restrictive rule says a merge may only ever REDUCE what the workspace
	// may do: A's withdrawal propagates to B, so the survivor ends up
	// withdrawn — never the other way round.
	purpose := ids.NewV7()
	e.wsExec(t, `INSERT INTO consent_purpose (id, workspace_id, key, label) VALUES ($1, $2, 'marketing', 'Marketing')`, purpose, e.ws)
	e.wsExec(t, `INSERT INTO person_consent (workspace_id, person_id, purpose_id, state) VALUES ($1, $2, $3, 'withdrawn')`, e.ws, source, purpose)
	e.wsExec(t, `INSERT INTO person_consent (workspace_id, person_id, purpose_id, state) VALUES ($1, $2, $3, 'granted')`, e.ws, target, purpose)

	if _, err := e.people.MergePerson(admin, source, target); err != nil {
		t.Fatalf("merge: %v", err)
	}

	var state string
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	if err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state FROM person_consent WHERE person_id = $1 AND purpose_id = $2`, target, purpose).Scan(&state)
	}); err != nil {
		t.Fatalf("read survivor consent: %v", err)
	}
	if state != "withdrawn" {
		t.Errorf("survivor consent = %q, want withdrawn (a merge only ever tightens)", state)
	}
	// The source's consent row folded in, none left behind.
	if n := e.wsCount(t, `SELECT count(*) FROM person_consent WHERE person_id = $1`, source); n != 0 {
		t.Errorf("%d consent rows still point at the merged-away source", n)
	}
}

func TestMergeOrganization_hierarchyReparenting(t *testing.T) {
	e := setupAuthz(t)
	admin := e.admin()

	source, err := e.people.CreateOrganization(admin, people.CreateOrganizationInput{DisplayName: "Acme Source", Source: "manual"})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	target, err := e.people.CreateOrganization(admin, people.CreateOrganizationInput{DisplayName: "Acme Target", Source: "manual"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	srcID, tgtID := ids.UUID(source.Id), ids.UUID(target.Id)
	// A child sits under the source.
	child, err := e.people.CreateOrganization(admin, people.CreateOrganizationInput{
		DisplayName: "Acme Child", ParentOrgID: &srcID, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	if _, err := e.people.MergeOrganization(admin, srcID, tgtID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// The child is re-homed under the survivor.
	got, err := e.people.GetOrganization(admin, ids.UUID(child.Id), false)
	if err != nil {
		t.Fatalf("read child: %v", err)
	}
	if got.ParentOrgId == nil || ids.UUID(*got.ParentOrgId) != tgtID {
		t.Errorf("child parent = %v, want the survivor %s", got.ParentOrgId, tgtID)
	}
	if n := e.wsCount(t, `SELECT count(*) FROM organization WHERE parent_org_id = $1`, srcID); n != 0 {
		t.Errorf("%d orgs still parented on the merged-away source", n)
	}
}

func TestMergeOrganization_partnerExtensionMovesIntoVacancy(t *testing.T) {
	e := setupAuthz(t)
	admin := e.admin()

	source, err := e.people.CreateOrganization(admin, people.CreateOrganizationInput{DisplayName: "Partner Source", Source: "manual"})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	target, err := e.people.CreateOrganization(admin, people.CreateOrganizationInput{DisplayName: "Plain Target", Source: "manual"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	srcID, tgtID := ids.UUID(source.Id), ids.UUID(target.Id)
	// The source carries the partner program; the target has none.
	e.wsExec(t, `INSERT INTO partner (workspace_id, organization_id, source, captured_by) VALUES ($1, $2, 'manual', 'human:test')`, e.ws, srcID)
	e.wsExec(t, `UPDATE organization SET classification = 'partner' WHERE id = $1`, srcID)

	if _, err := e.people.MergeOrganization(admin, srcID, tgtID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// The 1:1 extension moved into the vacancy, and the survivor flips to
	// classification=partner (the A41 invariant).
	if n := e.wsCount(t, `SELECT count(*) FROM partner WHERE organization_id = $1`, tgtID); n != 1 {
		t.Errorf("survivor has %d partner rows, want the moved 1", n)
	}
	if n := e.wsCount(t, `SELECT count(*) FROM partner WHERE organization_id = $1`, srcID); n != 0 {
		t.Errorf("%d partner rows still point at the merged-away source", n)
	}
	got, err := e.people.GetOrganization(admin, tgtID, false)
	if err != nil {
		t.Fatalf("read survivor: %v", err)
	}
	if got.Classification == nil || string(*got.Classification) != "partner" {
		t.Errorf("survivor classification = %v, want partner", got.Classification)
	}
}

func TestMerge_errorPaths(t *testing.T) {
	e := setupAuthz(t)
	admin := e.admin()

	a := e.seedPerson(t, "A", nil)
	b := e.seedPerson(t, "B", nil)
	c := e.seedPerson(t, "C", nil)

	// Self-merge.
	var selfErr *people.MergeSelfError
	if _, err := e.people.MergePerson(admin, a, a); !errors.As(err, &selfErr) {
		t.Fatalf("self-merge → %v, want people.MergeSelfError", err)
	}

	// First merge succeeds; a second merge OF the same source answers
	// AlreadyMerged with the redirect pointer.
	if _, err := e.people.MergePerson(admin, a, b); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	var already *people.AlreadyMergedError
	if _, err := e.people.MergePerson(admin, a, c); !errors.As(err, &already) {
		t.Fatalf("re-merge of a merged-away source → %v, want people.AlreadyMergedError", err)
	} else if already.IntoID != b {
		t.Errorf("AlreadyMerged points at %s, want the first survivor %s", already.IntoID, b)
	}

	// Merging INTO a merged-away (archived) target is refused.
	var deadTarget *people.MergedTargetError
	if _, err := e.people.MergePerson(admin, c, a); !errors.As(err, &deadTarget) {
		t.Fatalf("merge into a dead target → %v, want people.MergedTargetError", err)
	}
}
