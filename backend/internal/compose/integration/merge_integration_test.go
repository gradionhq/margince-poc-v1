// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

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
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestMergePerson_relinkSurvivorshipAndReferentialIntegrity(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	firstAda := "Ada"
	source, err := e.People.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Ada Source", FirstName: &firstAda, Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "ada.work@x.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	target, err := e.People.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Ada Target", Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "ada.other@x.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	src, tgt := personIDOf(ids.UUID(source.Id)), personIDOf(ids.UUID(target.Id))

	survivor, err := e.People.MergePerson(admin, src, tgt)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if personIDOf(ids.UUID(survivor.Id)) != tgt {
		t.Fatalf("survivor = %s, want the target %s", survivor.Id, tgt)
	}

	// Referential integrity: nothing LIVE still points at the merged-away
	// source (the source row itself keeps merged_into_id, that is the
	// redirect and expected).
	if n := e.WsCount(t, `SELECT count(*) FROM person_email WHERE person_id = $1`, src); n != 0 {
		t.Errorf("%d emails still point at the merged-away source", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM person_email WHERE person_id = $1`, tgt); n != 2 {
		t.Errorf("survivor has %d emails, want both relinked", n)
	}
	// Primary demotion: exactly one primary work email survives on B.
	if n := e.WsCount(t, `SELECT count(*) FROM person_email WHERE person_id = $1 AND email_type = 'work' AND is_primary AND archived_at IS NULL`, tgt); n != 1 {
		t.Errorf("survivor has %d primary work emails, want exactly 1 (A's must demote)", n)
	}

	// Fill-only survivorship: B had no first_name, so it takes A's.
	after, err := e.People.GetPerson(admin, tgt, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read survivor: %v", err)
	}
	if after.FirstName == nil || *after.FirstName != "Ada" {
		t.Errorf("survivor first_name = %v, want the filled 'Ada'", after.FirstName)
	}

	// The source is archived with a one-hop redirect and no longer live.
	if _, err := e.People.GetPerson(admin, src, storekit.LiveOnly); err == nil {
		t.Error("merged-away source still reads as live")
	}
	archived, err := e.People.GetPerson(admin, src, storekit.IncludeArchived)
	if err != nil {
		t.Fatalf("read archived source: %v", err)
	}
	if archived.MergedIntoId == nil || personIDOf(ids.UUID(*archived.MergedIntoId)) != tgt {
		t.Errorf("source merged_into_id = %v, want the target %s", archived.MergedIntoId, tgt)
	}
}

func TestMergePerson_consentMergesRestrictively(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	source := e.SeedPerson(t, "Consent Source", nil)
	target := e.SeedPerson(t, "Consent Target", nil)

	// One shared purpose; the source WITHDREW, the target GRANTED. The
	// restrictive rule says a merge may only ever REDUCE what the workspace
	// may do: A's withdrawal propagates to B, so the survivor ends up
	// withdrawn — never the other way round.
	purpose := ids.NewV7()
	e.WsExec(t, `INSERT INTO consent_purpose (id, workspace_id, key, label) VALUES ($1, $2, 'marketing', 'Marketing')`, purpose, e.WS)
	e.WsExec(t, `INSERT INTO person_consent (workspace_id, person_id, purpose_id, state) VALUES ($1, $2, $3, 'withdrawn')`, e.WS, source, purpose)
	e.WsExec(t, `INSERT INTO person_consent (workspace_id, person_id, purpose_id, state) VALUES ($1, $2, $3, 'granted')`, e.WS, target, purpose)

	if _, err := e.People.MergePerson(admin, personIDOf(source), personIDOf(target)); err != nil {
		t.Fatalf("merge: %v", err)
	}

	var state string
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state FROM person_consent WHERE person_id = $1 AND purpose_id = $2`, target, purpose).Scan(&state)
	}); err != nil {
		t.Fatalf("read survivor consent: %v", err)
	}
	if state != "withdrawn" {
		t.Errorf("survivor consent = %q, want withdrawn (a merge only ever tightens)", state)
	}
	// The source's consent row folded in, none left behind.
	if n := e.WsCount(t, `SELECT count(*) FROM person_consent WHERE person_id = $1`, source); n != 0 {
		t.Errorf("%d consent rows still point at the merged-away source", n)
	}
}

func TestMergeOrganization_hierarchyReparenting(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	source, err := e.People.CreateOrganization(admin, people.CreateOrganizationInput{DisplayName: "Acme Source", Source: "manual"})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	target, err := e.People.CreateOrganization(admin, people.CreateOrganizationInput{DisplayName: "Acme Target", Source: "manual"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	srcID, tgtID := orgIDOf(ids.UUID(source.Id)), orgIDOf(ids.UUID(target.Id))
	// A child sits under the source.
	child, err := e.People.CreateOrganization(admin, people.CreateOrganizationInput{
		DisplayName: "Acme Child", ParentOrgID: &srcID, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	if _, err := e.People.MergeOrganization(admin, srcID, tgtID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// The child is re-homed under the survivor.
	got, err := e.People.GetOrganization(admin, orgIDOf(ids.UUID(child.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read child: %v", err)
	}
	if got.ParentOrgId == nil || orgIDOf(ids.UUID(*got.ParentOrgId)) != tgtID {
		t.Errorf("child parent = %v, want the survivor %s", got.ParentOrgId, tgtID)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization WHERE parent_org_id = $1`, srcID); n != 0 {
		t.Errorf("%d orgs still parented on the merged-away source", n)
	}
}

func TestMergeOrganization_partnerExtensionMovesIntoVacancy(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	source, err := e.People.CreateOrganization(admin, people.CreateOrganizationInput{DisplayName: "Partner Source", Source: "manual"})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	target, err := e.People.CreateOrganization(admin, people.CreateOrganizationInput{DisplayName: "Plain Target", Source: "manual"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	srcID, tgtID := orgIDOf(ids.UUID(source.Id)), orgIDOf(ids.UUID(target.Id))
	// The source carries the partner program; the target has none.
	e.WsExec(t, `INSERT INTO partner (workspace_id, organization_id, source, captured_by) VALUES ($1, $2, 'manual', 'human:test')`, e.WS, srcID)
	e.WsExec(t, `UPDATE organization SET classification = 'partner' WHERE id = $1`, srcID)

	if _, err := e.People.MergeOrganization(admin, srcID, tgtID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// The 1:1 extension moved into the vacancy, and the survivor flips to
	// classification=partner (the A41 invariant).
	if n := e.WsCount(t, `SELECT count(*) FROM partner WHERE organization_id = $1`, tgtID); n != 1 {
		t.Errorf("survivor has %d partner rows, want the moved 1", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM partner WHERE organization_id = $1`, srcID); n != 0 {
		t.Errorf("%d partner rows still point at the merged-away source", n)
	}
	got, err := e.People.GetOrganization(admin, tgtID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read survivor: %v", err)
	}
	if got.Classification == nil || string(*got.Classification) != "partner" {
		t.Errorf("survivor classification = %v, want partner", got.Classification)
	}
}

func TestMerge_errorPaths(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	a := e.SeedPerson(t, "A", nil)
	b := e.SeedPerson(t, "B", nil)
	c := e.SeedPerson(t, "C", nil)

	// Self-merge.
	var selfErr *people.MergeSelfError
	if _, err := e.People.MergePerson(admin, personIDOf(a), personIDOf(a)); !errors.As(err, &selfErr) {
		t.Fatalf("self-merge → %v, want people.MergeSelfError", err)
	}

	// First merge succeeds; a second merge OF the same source answers
	// AlreadyMerged with the redirect pointer.
	if _, err := e.People.MergePerson(admin, personIDOf(a), personIDOf(b)); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	var already *people.AlreadyMergedError
	if _, err := e.People.MergePerson(admin, personIDOf(a), personIDOf(c)); !errors.As(err, &already) {
		t.Fatalf("re-merge of a merged-away source → %v, want people.AlreadyMergedError", err)
	} else if already.IntoID != b {
		t.Errorf("AlreadyMerged points at %s, want the first survivor %s", already.IntoID, b)
	}

	// Merging INTO a merged-away (archived) target is refused.
	var deadTarget *people.MergedTargetError
	if _, err := e.People.MergePerson(admin, personIDOf(c), personIDOf(a)); !errors.As(err, &deadTarget) {
		t.Fatalf("merge into a dead target → %v, want people.MergedTargetError", err)
	}
}
