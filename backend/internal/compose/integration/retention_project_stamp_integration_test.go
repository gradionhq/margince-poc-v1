// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Correspondence filed under a project is business correspondence (D5).
//
// The deal stamp beside this one fires when a deal CONCLUDES — one event, many
// activities. A project has no such instant: it is a commercial engagement from
// the moment it exists, so the LINK is the event and each activity is stamped
// as it is filed. These tests drive the real writers (the relink path, the
// erasure's legacy arm) rather than inserting evidence rows by hand, because
// what is being proved is that the classification commits with the link.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// projectStampFixture is one project on an organization and an email that is
// not yet filed under it.
type projectStampFixture struct {
	project ids.UUID
	email   ids.UUID
}

func seedProjectStampFixture(t *testing.T, e *Env) projectStampFixture {
	t.Helper()
	org := e.SeedOrg(t, "Acme GmbH", nil)
	project, email := ids.NewV7(), ids.NewV7()
	e.WsExec(t, `INSERT INTO project (id, name, key, organization_id, phase, source, captured_by)
		VALUES ($1, 'ERP rollout', 'ERP27', $2, 'delivering', 'manual', 'human:x')`, project, org)
	e.WsExec(t, `INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by)
		VALUES ($1, 'email', 'Milestone 3 sign-off', 'the acceptance test passed', now(), 'manual', 'human:x')`,
		email)
	return projectStampFixture{project: project, email: email}
}

// projectStampRow is what the two writers must agree on: the class on the
// activity, and the evidence naming the project that qualified it.
type projectStampRow struct {
	class       *string
	stampAt     *string
	evidence    int
	basis       *string
	projectName *string
	projectID   *string
}

func readProjectStamp(t *testing.T, e *Env, activity ids.UUID) projectStampRow {
	t.Helper()
	var got projectStampRow
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT retention_class, retention_class_at::text FROM activity WHERE id = $1`,
			activity).Scan(&got.class, &got.stampAt); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*), max(basis), max(project_name), max(project_id::text)
			  FROM activity_retention_evidence
			 WHERE activity_id = $1 AND basis = 'project_linked'`,
			activity).Scan(&got.evidence, &got.basis, &got.projectName, &got.projectID)
	}); err != nil {
		t.Fatalf("reading the project stamp: %v", err)
	}
	return got
}

// Filing an activity under a project stamps it, in the transaction that wrote
// the link — there is no window in which an erasure sees it unclassified.
func TestFilingAnActivityUnderAProjectStampsIt(t *testing.T) {
	e := Setup(t)
	f := seedProjectStampFixture(t, e)

	if before := readProjectStamp(t, e, f.email); before.class != nil {
		t.Fatalf("fixture drift: the email carries a class before it was filed (%q)", *before.class)
	}

	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: f.email},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}); err != nil {
		t.Fatalf("filing the email under its project: %v", err)
	}

	got := readProjectStamp(t, e, f.email)
	if got.class == nil {
		t.Fatal("filing under a project left the correspondence unstamped — an unstamped Handelsbrief is one the next erasure destroys")
	}
	if *got.class != "commercial_correspondence" {
		t.Errorf("retention_class = %q, want commercial_correspondence", *got.class)
	}
	if got.stampAt == nil {
		t.Error("the class landed without its timestamp; the stamp's provenance is what a supervisory authority is shown")
	}
	if got.evidence != 1 {
		t.Fatalf("project_linked evidence rows = %d, want 1 — a stamp with nothing behind it is an assertion the controller cannot substantiate", got.evidence)
	}
	if got.projectName == nil || *got.projectName != "ERP rollout" {
		t.Errorf("evidence project_name = %v, want the project's name frozen at qualification", got.projectName)
	}
	if got.projectID == nil || *got.projectID != f.project.String() {
		t.Errorf("evidence project_id = %v, want the project that qualified it", got.projectID)
	}
}

// The frozen name survives a rename. Evidence that reads back the CURRENT name
// is evidence of nothing: it cannot say what the record was called when the
// obligation arose.
func TestTheProjectNameIsFrozenAtQualification(t *testing.T) {
	e := Setup(t)
	f := seedProjectStampFixture(t, e)

	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: f.email},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}); err != nil {
		t.Fatalf("filing the email under its project: %v", err)
	}
	e.WsExec(t, `UPDATE project SET name = 'ERP rollout (renamed)' WHERE id = $1`, f.project)

	got := readProjectStamp(t, e, f.email)
	if got.projectName == nil || *got.projectName != "ERP rollout" {
		t.Errorf("evidence project_name = %v after a rename, want the name frozen at qualification", got.projectName)
	}
}

// Moving the activity off the project does NOT unstamp it. The classification
// is monotonic precisely because attribution is not: over-retention is an
// argument to have with a supervisory authority, destruction is irreversible.
// The behaviour is surprising enough that the product documents it, so it is
// held by a test rather than by the comment claiming it.
func TestRelinkingAwayFromAProjectLeavesTheStampStanding(t *testing.T) {
	e := Setup(t)
	f := seedProjectStampFixture(t, e)
	other := ids.NewV7()
	e.WsExec(t, `INSERT INTO project (id, name, organization_id, phase, source, captured_by)
		SELECT $1, 'Datacentre migration', organization_id, 'delivering', 'manual', 'human:x'
		  FROM project WHERE id = $2`, other, f.project)

	for _, target := range []ids.UUID{f.project, other} {
		if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: f.email},
			activities.RelinkActivityInput{
				EntityType: "project", EntityID: target, ReplaceExistingOfType: true,
			}); err != nil {
			t.Fatalf("filing the email under project %v: %v", target, err)
		}
	}

	got := readProjectStamp(t, e, f.email)
	if got.class == nil {
		t.Fatal("moving the activity to another project unstamped it — the class is monotonic and the evidence frozen")
	}
	// Both projects qualified it in turn, and the evidence for the first must
	// survive the move: the obligation it recorded was real when it arose.
	if got.evidence != 2 {
		t.Errorf("project_linked evidence rows = %d, want 2 — the first project's evidence must outlive the relink", got.evidence)
	}
}

// An activity can honestly qualify twice, and the uniqueness index must let it:
// a deal that concluded and a project it is filed under are two separate facts,
// and collapsing them would lose one of the obligations.
func TestAnActivityCanQualifyThroughBothADealAndAProject(t *testing.T) {
	e := Setup(t)
	f := seedProjectStampFixture(t, e)

	// Won through the real transition, because the deal stamp fires inside it:
	// a hand-inserted `status = 'won'` row would leave the correspondence
	// unstamped and this test would pass against a schema that cannot hold
	// two bases at all.
	pipeline, openStage, wonStage := ids.NewV7(), ids.NewV7(), ids.NewV7()
	e.WsExec(t, `INSERT INTO pipeline (id, name, is_default, position)
		VALUES ($1, 'Double-basis fixture', false, 92)`, pipeline)
	e.WsExec(t, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, 'Qualify', 0, 'open', 10)`, openStage, pipeline)
	e.WsExec(t, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, 'Closed Won', 1, 'won', 100)`, wonStage, pipeline)
	deal := e.SeedDeal(t, "ERP licences",
		ids.PipelineID{UUID: pipeline}, ids.StageID{UUID: openStage}, nil)
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, deal_id)
		VALUES ($1, 'deal', $2)`, f.email, deal)
	if _, err := e.Deals.AdvanceDeal(e.Admin(), ids.DealID{UUID: deal},
		wonInput(ids.StageID{UUID: wonStage})); err != nil {
		t.Fatalf("winning the deal: %v", err)
	}

	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: f.email},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}); err != nil {
		t.Fatalf("filing the email under its project: %v", err)
	}

	var bases []string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT basis FROM activity_retention_evidence WHERE activity_id = $1 ORDER BY basis`, f.email)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var basis string
			if err := rows.Scan(&basis); err != nil {
				return err
			}
			bases = append(bases, basis)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading the evidence: %v", err)
	}

	if len(bases) != 2 {
		t.Fatalf("evidence bases = %v, want both the deal's and the project's — an activity may qualify twice", bases)
	}
	if bases[0] != "deal_won" || bases[1] != "project_linked" {
		t.Errorf("evidence bases = %v, want [deal_won project_linked]", bases)
	}
}

// backfillStatement is the migration's backfill, verbatim. It is duplicated
// here rather than read from the file because a migration runs once and a test
// that re-applies it would be testing the runner; what must be proved is that
// the STATEMENT is safe to run again, which is what makes a partially failed
// backfill recoverable rather than a one-way door with no second chance.
//
// It must stay in step with 1787400000_project_linked_correspondence.up.sql.
// A drift here is not silent: the first run's assertions below fail, because a
// statement that no longer stamps is a statement that stamps nothing.
const backfillStatement = `
	WITH linked AS (
	  SELECT l.activity_id AS id, p.id AS project_id, p.name AS project_name
	    FROM activity_link l
	    JOIN project p ON p.id = l.project_id
	   WHERE l.entity_type = 'project'
	), stamped AS (
	  UPDATE activity a
	     SET retention_class = 'commercial_correspondence', retention_class_at = now()
	   WHERE a.id IN (SELECT id FROM linked)
	     AND a.retention_class IS NULL
	)
	INSERT INTO activity_retention_evidence (activity_id, basis, qualified_at, project_id, project_name)
	SELECT id, 'project_linked', now(), project_id, project_name FROM linked
	ON CONFLICT DO NOTHING`

// The backfill reaches a link the ladder wrote before the stamp existed, and
// running it twice changes nothing the second time. Both halves matter: the
// first is why the PR is not leaving a backlog of destroyable Handelsbriefe,
// the second is why a partial failure can simply be re-run.
func TestTheBackfillStampsPreExistingLinksAndIsIdempotent(t *testing.T) {
	e := Setup(t)
	f := seedProjectStampFixture(t, e)

	// The link exactly as a database predating this migration holds it: written
	// by the ladder, with no evidence row behind it.
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)`, f.email, f.project)
	if before := readProjectStamp(t, e, f.email); before.class != nil {
		t.Fatalf("fixture drift: the backlog link is already stamped (%q)", *before.class)
	}

	e.WsExec(t, backfillStatement)
	first := readProjectStamp(t, e, f.email)
	if first.class == nil {
		t.Fatal("the backfill left a pre-existing project link unstamped — that link is a Handelsbrief the next erasure destroys")
	}
	if first.evidence != 1 {
		t.Fatalf("project_linked evidence rows = %d after the backfill, want 1", first.evidence)
	}
	if first.projectName == nil || *first.projectName != "ERP rollout" {
		t.Errorf("evidence project_name = %v, want the project's name frozen by the backfill", first.projectName)
	}

	e.WsExec(t, backfillStatement)
	second := readProjectStamp(t, e, f.email)
	if second.evidence != 1 {
		t.Errorf("project_linked evidence rows = %d after re-running the backfill, want 1 — a backfill that duplicates cannot be safely re-run after a partial failure", second.evidence)
	}
	if second.stampAt == nil || first.stampAt == nil || *second.stampAt != *first.stampAt {
		t.Errorf("retention_class_at moved on the second run (%v then %v); the stamp instant is evidence and must not be rewritten", first.stampAt, second.stampAt)
	}
}

// The evidence's project columns are frozen at the database level, the way its
// deal columns already were. The trigger names every column it protects, so a
// column added beside them is unguarded by default — and an unguarded rewrite
// succeeds silently, leaving evidence that reads back as a fact about a record
// that never qualified anything.
func TestTheProjectEvidenceColumnsAreFrozenAtTheDatabase(t *testing.T) {
	e := Setup(t)
	f := seedProjectStampFixture(t, e)
	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.ActivityID{UUID: f.email},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}); err != nil {
		t.Fatalf("filing the email under its project: %v", err)
	}
	other := ids.NewV7()
	e.WsExec(t, `INSERT INTO project (id, name, organization_id, phase, source, captured_by)
		SELECT $1, 'Datacentre migration', organization_id, 'delivering', 'manual', 'human:x'
		  FROM project WHERE id = $2`, other, f.project)

	for _, c := range []struct {
		what   string
		update string
		args   []any
	}{
		{
			"rewriting the frozen project name",
			`UPDATE activity_retention_evidence SET project_name = 'Something else' WHERE activity_id = $1`,
			[]any{f.email},
		},
		{
			"repointing the evidence at another project",
			`UPDATE activity_retention_evidence SET project_id = $2 WHERE activity_id = $1`,
			[]any{f.email, other},
		},
	} {
		err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			_, execErr := tx.Exec(context.Background(), c.update, c.args...)
			return execErr
		})
		if err == nil {
			t.Errorf("%s succeeded; the evidence is frozen at the moment it qualified and a database that permits this permits rewriting the proof", c.what)
		}
	}
}
