// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// What the open-promise read discloses, against a real database and a real
// row scope.
//
// Both claims here answer SUCCESSFULLY when they are broken, which is why they
// need a database rather than a string assertion over the built SQL. Delete
// `AND `+visible from attachTaskAbout and every unit test in this package still
// passes — the deal's name simply appears in an answer it should not be in.
// Delete the narrowing gate and a caller learns a project exists by asking
// about its promises. Neither failure raises anything.
//
// The rows are seeded as the table OWNER, so the reads below run against rows
// this store did not write — the shape a real workspace has.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The names the assertions hunt for. A caller who may not read the deal must
// never be told either of these, and they are distinctive enough that a
// substring search over the whole answer is conclusive.
const (
	hiddenDealName   = "Confidential Zeta renewal"
	visiblePersonNam = "Ada Lovelace"
	hiddenProjectNam = "Confidential Zeta rollout"
)

// promiseEnv is one workspace with two reps, and records owned by each.
type promiseEnv struct {
	owner *pgx.Conn
	pool  *pgxpool.Pool
	ws    ids.UUID
	// rep is the caller every read below is made as; other owns the records
	// rep must not see.
	rep   ids.UUID
	other ids.UUID
}

func setupPromises(t *testing.T) *promiseEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` " +
			"(integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	e := &promiseEnv{owner: owner, ws: ids.NewV7(), rep: ids.NewV7(), other: ids.NewV7()}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Promises', $2, 'EUR')`,
		e.ws, "promises-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	for _, user := range []ids.UUID{e.rep, e.other} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
			user, e.ws, "rep-"+user.String()+"@promises.test"); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.pool = pool
	return e
}

// as binds the rep at OWN row scope — the narrowest real caller, and the one
// for whom every scope clause in this read is non-empty.
func (e *promiseEnv) as() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Read: true}, "person": {Read: true},
				"deal": {Read: true}, "project": {Read: true},
				"organization": {Read: true},
			},
			RowScope: principal.RowScopeOwn,
		},
	})
}

// exec runs one owner statement, failing the test on error.
func (e *promiseEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seeding: %v", err)
	}
}

// seedSplitTask writes the shape the disclosure rule is about: ONE open task
// linked both to a person the caller owns and to a deal they do not.
//
// The activity gate is an ANY-LINK rule, so the visible person makes the task
// readable — and that is correct. What must not follow is being told the name
// of the deal on the other link.
func (e *promiseEnv) seedSplitTask(t *testing.T) (taskID, hiddenDealID ids.UUID) {
	t.Helper()
	orgID, personID := ids.NewV7(), ids.NewV7()
	pipelineID, stageID := ids.NewV7(), ids.NewV7()
	hiddenDealID, taskID = ids.NewV7(), ids.NewV7()

	e.exec(t, `INSERT INTO organization (id, workspace_id, display_name, owner_id, source, captured_by)
		VALUES ($1, $2, 'Zeta GmbH', $3, 'seed', 'system')`, orgID, e.ws, e.rep)
	e.exec(t, `INSERT INTO pipeline (id, workspace_id, name, is_default, position)
		VALUES ($1, $2, 'Default', true, 1)`, pipelineID, e.ws)
	e.exec(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, win_probability)
		VALUES ($1, $2, $3, 'Qualified', 1, 20)`, stageID, e.ws, pipelineID)
	e.exec(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
		VALUES ($1, $2, $3, $4, 'seed', 'system')`, personID, e.ws, visiblePersonNam, e.rep)
	// Owned by the OTHER rep, so an own-scoped caller cannot read it.
	e.exec(t, `INSERT INTO deal (id, workspace_id, name, organization_id, owner_id,
			pipeline_id, stage_id, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'seed', 'system')`,
		hiddenDealID, e.ws, hiddenDealName, orgID, e.other, pipelineID, stageID)

	e.exec(t, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, due_at,
			assignee_id, is_done, source, captured_by)
		VALUES ($1, $2, 'task', 'Renew the Zeta contract', now(), now() - interval '2 days',
			$3, false, 'seed', 'system')`, taskID, e.ws, e.rep)
	e.exec(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, person_id)
		VALUES ($1, $2, $3, 'person', $4)`, ids.NewV7(), e.ws, taskID, personID)
	e.exec(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, deal_id)
		VALUES ($1, $2, $3, 'deal', $4)`, ids.NewV7(), e.ws, taskID, hiddenDealID)
	return taskID, hiddenDealID
}

// A task reachable through a visible person is readable in full. Being told
// what ELSE it is about is a second question, and the answer to it is bounded
// by the caller's scope on each linked record — not by the task's.
func TestAPromiseNamesOnlyTheRecordsItsReaderMaySee(t *testing.T) {
	e := setupPromises(t)
	taskID, hiddenDealID := e.seedSplitTask(t)

	tasks, _, err := NewStore(e.pool).ListOpenTasks(e.as(), ListOpenTasksInput{})
	if err != nil {
		t.Fatalf("listing open tasks: %v", err)
	}

	var found *OpenTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			found = &tasks[i]
		}
	}
	if found == nil {
		t.Fatal("the task linked to a person the caller owns is missing — the any-link " +
			"rule makes it readable, so this test would prove nothing about what it names")
	}

	named := map[string]string{}
	for _, about := range found.About {
		named[about.EntityID.String()] = about.Name
	}
	if _, told := named[hiddenDealID.String()]; told {
		t.Errorf("the answer names a deal owned by another rep (%s) — an activity readable "+
			"through one visible link does not license disclosing the others", hiddenDealID)
	}
	for id, name := range named {
		if name == hiddenDealName {
			t.Errorf("the answer carries the hidden deal's NAME against %s, which is the "+
				"disclosure the link-visibility clause exists to prevent", id)
		}
	}
	// And the visible half IS named, or the assertions above would pass over an
	// answer that simply carries nothing.
	if !strings.Contains(strings.Join(namesIn(named), "|"), visiblePersonNam) {
		t.Errorf("the answer names none of the records the caller CAN see (%v) — the "+
			"projection is empty, so its filtering proved nothing", named)
	}
}

// Narrowing to a record is a question about that record. A caller who may not
// read it is answered not-found, the same as for an id that names nothing —
// otherwise the reply "here are its promises" is itself the disclosure.
func TestNarrowingToAnUnreadableRecordAnswersNotFound(t *testing.T) {
	e := setupPromises(t)
	orgID, hiddenProjectID, taskID := ids.NewV7(), ids.NewV7(), ids.NewV7()
	e.exec(t, `INSERT INTO organization (id, workspace_id, display_name, owner_id, source, captured_by)
		VALUES ($1, $2, 'Zeta GmbH', $3, 'seed', 'system')`, orgID, e.ws, e.rep)
	e.exec(t, `INSERT INTO project (id, workspace_id, name, organization_id, owner_id, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, 'seed', 'system')`,
		hiddenProjectID, e.ws, hiddenProjectNam, orgID, e.other)
	// The task is assigned to the CALLER, so nothing about the task itself is
	// what hides it — only the project the sweep is narrowed to.
	e.exec(t, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, due_at,
			assignee_id, is_done, source, captured_by)
		VALUES ($1, $2, 'task', 'Ship the Zeta rollout', now(), now(), $3, false, 'seed', 'system')`,
		taskID, e.ws, e.rep)
	e.exec(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, project_id)
		VALUES ($1, $2, $3, 'project', $4)`, ids.NewV7(), e.ws, taskID, hiddenProjectID)

	projectType := "project"
	_, _, err := NewStore(e.pool).ListOpenTasks(e.as(), ListOpenTasksInput{
		EntityType: &projectType, EntityID: &hiddenProjectID,
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("narrowing to a project owned by another rep → %v, want ErrNotFound — an "+
			"answer of any kind tells the caller the project is there", err)
	}
}

// namesIn is the map's values, for the containment assertion above.
func namesIn(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
