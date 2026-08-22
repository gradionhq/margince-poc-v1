// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// An erasure holds a project's correspondence instead of destroying it (D5).
//
// The shield used to ask only about deals, so an email filed under a project
// and no deal failed the Handelsbrief test and was scrubbed like an internal
// note. These tests drive the real eraser against a subject whose only
// commercial link is a project, and assert the two halves that make the fix
// real: the stamped row is held, and a row captured BEFORE the stamp existed
// is held too — through the legacy arm, which must stay in step with the
// shield or the erasure fails outright rather than under-shielding.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// projectErasureFixture is a subject with a 400-day-old email whose only
// commercial attribution is a project — no deal anywhere near it.
type projectErasureFixture struct {
	person, email, project ids.UUID
}

func seedProjectErasureFixture(t *testing.T, e *Env) projectErasureFixture {
	t.Helper()
	org := e.SeedOrg(t, "Acme GmbH", nil)
	f := projectErasureFixture{person: ids.NewV7(), email: ids.NewV7(), project: ids.NewV7()}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx,
			`INSERT INTO person (id, full_name, first_name, source, captured_by)
			 VALUES ($1, 'Delivery Contact', 'Delivery', 'manual', 'human:x')`, f.person); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO person_email (person_id, email, source, captured_by)
			 VALUES ($1, 'delivery@example.test', 'manual', 'human:x')`, f.person); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO project (id, name, key, organization_id, phase, source, captured_by)
			 VALUES ($1, 'ERP rollout', 'ERP27', $2, 'delivering', 'manual', 'human:x')`,
			f.project, org); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity (id, kind, subject, body, raw, counterparty_email, occurred_at, source, captured_by)
			 VALUES ($1, 'email', 'Milestone 3 sign-off', 'The acceptance test passed.', '{"provider":"payload"}'::jsonb,
			         'delivery@example.test', now() - interval '400 days', 'manual', 'human:x')`,
			f.email); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO activity_link (activity_id, entity_type, person_id) VALUES ($1, 'person', $2)`,
			f.email, f.person)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// heldState is what distinguishes a held record from a destroyed one: a
// restriction instant and a body that still says what it said.
type heldState struct {
	restrictedAt *string
	subject      string
	body         *string
}

func readHeldState(t *testing.T, e *Env, activity ids.UUID) heldState {
	t.Helper()
	var got heldState
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT restricted_at::text, subject, body FROM activity WHERE id = $1`,
			activity).Scan(&got.restrictedAt, &got.subject, &got.body)
	}); err != nil {
		t.Fatalf("reading the erased subject's correspondence: %v", err)
	}
	return got
}

// A project-linked email is a Handelsbrief. Erasing its subject holds it —
// substance intact, restriction pinned — rather than scrubbing it.
func TestErasureHoldsCorrespondenceHeldOnlyByAProject(t *testing.T) {
	e := Setup(t)
	f := seedProjectErasureFixture(t, e)

	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: f.email},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}); err != nil {
		t.Fatalf("filing the email under its project: %v", err)
	}
	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), f.person, "test"); err != nil {
		t.Fatalf("erasing the subject: %v", err)
	}

	got := readHeldState(t, e, f.email)
	if got.restrictedAt == nil {
		t.Fatal("the erasure destroyed correspondence filed under a live project — a project is a commercial engagement and its mail is a Handelsbrief")
	}
	if got.body == nil || *got.body != "The acceptance test passed." {
		t.Errorf("body = %v, want the commercial substance kept; a restriction removes the subject, not the transaction", got.body)
	}
}

// The legacy arm, and the reason it is not optional. A row filed under a
// project BEFORE the stamp writer existed carries no class, so the shield
// reaches it through the derived rule — and the erasure's own stamp step must
// reach it through the SAME rule. If the two disagree, the restrict step
// selects a row the activity_restriction_needs_evidence trigger then refuses,
// and the whole erasure fails rather than under-shielding one record.
func TestErasureStampsAPreStampProjectLinkAndHoldsIt(t *testing.T) {
	e := Setup(t)
	f := seedProjectErasureFixture(t, e)

	// The link written the way a database predating this feature holds it:
	// present, and with no evidence row behind it.
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)`, f.email, f.project)

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), f.person, "test"); err != nil {
		t.Fatalf("erasing the subject over a pre-stamp project link: %v", err)
	}

	got := readHeldState(t, e, f.email)
	if got.restrictedAt == nil {
		t.Fatal("the erasure destroyed a pre-stamp project-linked Handelsbrief; the derived arm is the floor for rows captured before the stamp existed")
	}

	var basis *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT max(basis) FROM activity_retention_evidence WHERE activity_id = $1`,
			f.email).Scan(&basis)
	}); err != nil {
		t.Fatalf("reading the evidence the erasure wrote: %v", err)
	}
	if basis == nil || *basis != "project_linked" {
		t.Errorf("evidence basis = %v, want project_linked — a held record without evidence is one the controller cannot account for", basis)
	}
}

// The controller's list names the project holding the record. A shielded
// record whose reason resolves to nothing is evidence of nothing.
func TestTheRestrictedListNamesTheHoldingProject(t *testing.T) {
	e := Setup(t)
	f := seedProjectErasureFixture(t, e)

	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: f.email},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}); err != nil {
		t.Fatalf("filing the email under its project: %v", err)
	}
	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), f.person, "test"); err != nil {
		t.Fatalf("erasing the subject: %v", err)
	}

	// The compliance list is gated on retention_policy.read, which the harness
	// admin does not hold — it is admin/ops-only on every verb.
	controller := retentionAdminCtx(e.WS, principal.ObjectGrant{Read: true})
	page, err := privacy.ListRestrictedActivities(controller, e.DB(), nil, nil)
	if err != nil {
		t.Fatalf("listing the held records: %v", err)
	}
	var found bool
	for _, record := range page.Records {
		if record.ActivityID != f.email {
			continue
		}
		found = true
		if len(record.Projects) != 1 {
			t.Fatalf("qualifying projects = %d, want 1 — the controller must be able to say what obliges the hold", len(record.Projects))
		}
		if record.Projects[0].Name != "ERP rollout" {
			t.Errorf("qualifying project = %q, want the frozen name", record.Projects[0].Name)
		}
		if len(record.Deals) != 0 {
			t.Errorf("qualifying deals = %d, want none — no deal was ever near this record", len(record.Deals))
		}
	}
	if !found {
		t.Fatal("the held record is absent from the controller's list")
	}
}

// A note filed under a project is stamped and still erased. The stamp is
// unfiltered by kind (matching the deal writer), so the class lands on a note
// as readily as on an email — and `handelsbriefShielded` excludes notes from
// the shield, which is what keeps that class inert.
//
// Without this test the two rules could drift apart silently and put a
// six-year floor on every internal jotting a project accumulates, which on a
// two-year engagement is most of them. That failure has no error and nothing
// to notice: the note simply stops being erasable.
func TestANoteFiledUnderAProjectIsStampedAndStillErased(t *testing.T) {
	e := Setup(t)
	f := seedProjectErasureFixture(t, e)
	note := ids.NewV7()
	e.WsExec(t, `INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by)
		VALUES ($1, 'note', 'Internal jotting', 'Chase them next week.', now() - interval '400 days', 'manual', 'human:x')`,
		note)
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, person_id) VALUES ($1, 'person', $2)`,
		note, f.person)

	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: note},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}); err != nil {
		t.Fatalf("filing the note under its project: %v", err)
	}

	var class *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT retention_class FROM activity WHERE id = $1`, note).Scan(&class)
	}); err != nil {
		t.Fatalf("reading the note's class: %v", err)
	}
	if class == nil {
		t.Fatal("fixture drift: the note was not stamped, so this test cannot prove the class is inert on a note")
	}

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), f.person, "test"); err != nil {
		t.Fatalf("erasing the subject: %v", err)
	}

	got := readHeldState(t, e, note)
	if got.restrictedAt != nil {
		t.Fatal("a project note was HELD rather than erased — a note is not correspondence, and the shield must exclude it whatever class it carries")
	}
	if got.subject != "Erased Subject" {
		t.Errorf("note subject = %q, want the erased placeholder", got.subject)
	}
}

// The shield tests only the LINK; the erasure's legacy stamp arm joins the
// project behind it. The two agree only because activity_link.project_id is
// ON DELETE CASCADE, so a link to a project that no longer exists is not a
// state the database holds.
//
// This test asserts the cascade rather than the comment claiming it. Flipping
// that FK to SET NULL would leave a link row whose project is gone: the shield
// would hold the activity, the stamp arm would write no evidence, and the
// activity_restriction_needs_evidence trigger would refuse — failing the whole
// erasure for that subject, not just under-shielding one record.
func TestDeletingAProjectTakesItsActivityLinksWithIt(t *testing.T) {
	e := Setup(t)
	f := seedProjectErasureFixture(t, e)

	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: f.email},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}); err != nil {
		t.Fatalf("filing the email under its project: %v", err)
	}
	// Assert the link EXISTS before deleting the project. Without this the
	// orphan count below is satisfied by having nothing to count, so a relink
	// that silently wrote no link would read as a passing cascade.
	var linked int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity_link WHERE activity_id = $1 AND entity_type = 'project'`,
			f.email).Scan(&linked)
	}); err != nil {
		t.Fatalf("reading the fixture's project link: %v", err)
	}
	if linked != 1 {
		t.Fatalf("fixture drift: the email carries %d project links before the delete, want 1 — the cascade assertion below would pass with nothing to cascade", linked)
	}

	e.WsExec(t, `DELETE FROM project WHERE id = $1`, f.project)

	var orphans int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT count(*) FROM activity_link l
			 WHERE l.entity_type = 'project'
			   AND NOT EXISTS (SELECT 1 FROM project p WHERE p.id = l.project_id)`).Scan(&orphans)
	}); err != nil {
		t.Fatalf("counting orphaned project links: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("%d project link(s) outlived their project — the erasure shield reads the link and its stamp arm reads the project, and they only agree while the cascade holds", orphans)
	}

	// The evidence, by contrast, MUST survive: its project_id is ON DELETE SET
	// NULL and the frozen name is what answers after the record is gone.
	var name *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT max(project_name) FROM activity_retention_evidence WHERE activity_id = $1`,
			f.email).Scan(&name)
	}); err != nil {
		t.Fatalf("reading the evidence after the project was deleted: %v", err)
	}
	if name == nil || *name != "ERP rollout" {
		t.Errorf("evidence project_name = %v after the project was deleted, want the frozen name — evidence that dies with its record is evidence of nothing", name)
	}
}
