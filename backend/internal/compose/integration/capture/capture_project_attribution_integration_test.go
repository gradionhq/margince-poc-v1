// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

// The project attribution ladder end to end over a real migrated Postgres
// (PROJ-FORM-1..3): a captured message is filed under a project by its thread,
// by the deal it is filed under, or by the key its subject names — and under
// nothing at all when no rung matches, which is the answer for most mail.
//
// Every fixture is written by the thing that writes it in production: projects
// and deals through the deals store, activities through the capture sink.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// projectSeeder is the deals store plus the context its writes run under —
// carried together because every fixture here needs both, and a test that
// passed them separately could pass a mismatched pair.
type projectSeeder struct {
	store *deals.Store
	ctx   context.Context
	orgID ids.UUID
	// The pipeline a seeded deal is born on. Scaffolding rather than subject:
	// nothing in the ladder reads a stage, and a deal cannot exist without one.
	pipelineID ids.PipelineID
	stageID    ids.StageID
}

// newProjectSeeder wires the REAL deals store over the test pool, with a
// principal that may create the records the ladder later reads. The
// installation's anchor company is reused as the projects' anchor: the harness
// already created it the way cold start does, and inventing a second one here
// would be a fixture writing what no production path writes.
func newProjectSeeder(t *testing.T, e *integration.SearchEnv) projectSeeder {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"project":      {Create: true, Read: true, Update: true},
				"deal":         {Create: true, Read: true, Update: true},
				"organization": {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	var orgID, pipelineID, stageID ids.UUID
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id FROM organization WHERE is_anchor LIMIT 1`).Scan(&orgID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO pipeline (name, is_default, position) VALUES ('Sales', true, 0)
			RETURNING id`).Scan(&pipelineID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO stage (pipeline_id, name, position, semantic, win_probability)
			VALUES ($1, 'Qualify', 0, 'open', 10) RETURNING id`, pipelineID).Scan(&stageID)
	}); err != nil {
		t.Fatalf("seeding the pipeline a deal is born on: %v", err)
	}
	return projectSeeder{
		store:      deals.NewStore(e.DB(), compose.DealsInstallation()),
		ctx:        ctx,
		orgID:      orgID,
		pipelineID: ids.From[ids.PipelineKind](pipelineID),
		stageID:    ids.From[ids.StageKind](stageID),
	}
}

// project creates one live project through the store that owns the table.
func (s projectSeeder) project(t *testing.T, name, key string) ids.UUID {
	t.Helper()
	created, err := s.store.CreateProject(s.ctx, deals.CreateProjectInput{
		Name:           name,
		Key:            &key,
		OrganizationID: ids.From[ids.OrganizationKind](s.orgID),
		Source:         "manual",
	})
	if err != nil {
		t.Fatalf("creating project %q: %v", name, err)
	}
	return ids.UUID(created.Id)
}

// dealOnProject creates a deal that rolls up to the given project, through the
// store that owns both tables.
func (s projectSeeder) dealOnProject(t *testing.T, name string, projectID ids.UUID) ids.UUID {
	t.Helper()
	id := ids.From[ids.ProjectKind](projectID)
	return s.createDeal(t, name, &id)
}

// deal creates a deal belonging to no project — the control the deal rung must
// not inherit anything from.
func (s projectSeeder) deal(t *testing.T, name string) ids.UUID {
	t.Helper()
	return s.createDeal(t, name, nil)
}

func (s projectSeeder) createDeal(t *testing.T, name string, projectID *ids.ProjectID) ids.UUID {
	t.Helper()
	orgID := ids.From[ids.OrganizationKind](s.orgID)
	created, err := s.store.CreateDeal(s.ctx, deals.CreateDealInput{
		Name:           name,
		PipelineID:     s.pipelineID,
		StageID:        s.stageID,
		OrganizationID: &orgID,
		ProjectID:      projectID,
		Source:         "manual",
	})
	if err != nil {
		t.Fatalf("creating deal %q: %v", name, err)
	}
	return ids.UUID(created.Id)
}

// linkedProject answers which project one captured message was filed under, or
// the zero id when the ladder concluded nothing.
func linkedProject(t *testing.T, e *integration.SearchEnv, sourceID string) ids.UUID {
	t.Helper()
	var projectID ids.UUID
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT coalesce(
			         (SELECT al.project_id
			            FROM activity a
			            JOIN activity_link al ON al.activity_id = a.id AND al.entity_type = 'project'
			           WHERE a.source_id = $1),
			         '00000000-0000-0000-0000-000000000000'::uuid)`, sourceID).Scan(&projectID)
	})
	if err != nil {
		t.Fatalf("reading the message's project link: %v", err)
	}
	return projectID
}

// The subject-key rung, and its two refusals: an ambiguous subject and a key
// that is only a substring of a longer word both leave the message unfiled.
func TestCaptureFilesAMessageUnderTheProjectItsSubjectNames(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	erp := seed.project(t, "ERP replacement", "ERP")
	seed.project(t, "CRM rollout", "CRM")

	sync(t,
		emailAbout("pk1@acme.example", "", "[ERP] weekly status"),
		emailAbout("pk2@acme.example", "", "ERPNEXT evaluation"),
		emailAbout("pk3@acme.example", "", "ERP and CRM together"),
		emailAbout("pk4@acme.example", "", "lunch on Thursday"),
	)

	if got := linkedProject(t, e, "pk1@acme.example"); got != erp {
		t.Fatalf("the subject naming ERP filed the message under %s, want the ERP project %s", got, erp)
	}
	// A key must be a whole token: ERPNEXT is a different word, and nothing
	// downstream of this ladder would catch the message landing on ERP.
	if got := linkedProject(t, e, "pk2@acme.example"); !got.IsZero() {
		t.Fatalf("ERPNEXT filed a message under project %s; a key must never match a substring", got)
	}
	// Two projects named in one subject is not evidence for either.
	if got := linkedProject(t, e, "pk3@acme.example"); !got.IsZero() {
		t.Fatalf("an ambiguous subject filed a message under project %s, want nothing", got)
	}
	// Most mail belongs to no project, and that is the correct answer.
	if got := linkedProject(t, e, "pk4@acme.example"); !got.IsZero() {
		t.Fatalf("a subject naming no project filed a message under %s, want nothing", got)
	}
}

// The thread rung: a conversation is about one body of work, so the reply
// inherits its sibling's project even though its own subject names none.
func TestCaptureFilesAReplyUnderItsThreadsProject(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	erp := seed.project(t, "ERP replacement", "ERP")

	sync(t, emailAbout("th1@acme.example", "", "[ERP] kickoff"))
	// A second pull, so the reply genuinely reads a committed sibling rather
	// than one its own batch happened to write first.
	sync(t, emailAbout("th2@acme.example", "th1@acme.example", "Re: kickoff"))

	if got := linkedProject(t, e, "th2@acme.example"); got != erp {
		t.Fatalf("the reply landed on project %s, want its thread's project %s", got, erp)
	}
}

// The deal rung: a message the connector filed under a deal that belongs to a
// project belongs to that project too. The subject names no project here, so
// the rollup is the only evidence there is.
func TestCaptureFilesAMessageUnderTheProjectOfItsDeal(t *testing.T) {
	env := newCaptureEnv(t)
	e := env.e
	seed := newProjectSeeder(t, e)
	erp := seed.project(t, "ERP replacement", "ERP")
	onProject := seed.dealOnProject(t, "Phase two", erp)
	offProject := seed.deal(t, "Unrelated pursuit")

	env.syncFiledUnderDeal(t,
		map[string]ids.UUID{
			"dl1@acme.example": onProject,
			"dl2@acme.example": offProject,
		},
		emailAbout("dl1@acme.example", "", "quick question"),
		emailAbout("dl2@acme.example", "", "separate question"),
	)

	if got := linkedProject(t, e, "dl1@acme.example"); got != erp {
		t.Fatalf("the message on a project's deal landed on %s, want %s", got, erp)
	}
	// A deal that belongs to no project inherits nothing — there is nothing to
	// inherit, and inventing one would be the guess this ladder never makes.
	if got := linkedProject(t, e, "dl2@acme.example"); !got.IsZero() {
		t.Fatalf("a deal with no project filed its message under %s, want nothing", got)
	}
}

// A message's filing is decided once. Re-pulling it — which every sync loop
// does, because the bus and the mailbox are both at-least-once — must not move
// it, even when the provider hands back a subject that now names a different
// project. Replacement is a human's relink alone.
func TestCaptureDecidesAMessagesProjectOnlyOnce(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	erp := seed.project(t, "ERP replacement", "ERP")
	crm := seed.project(t, "CRM rollout", "CRM")

	sync(t, emailAbout("ow1@acme.example", "", "[CRM] kickoff"))
	if got := linkedProject(t, e, "ow1@acme.example"); got != crm {
		t.Fatalf("the first pass filed the message under %s, want %s", got, crm)
	}
	// A replay re-runs the whole capture, ladder included. The link that stands
	// is the first one, and uq_activity_link_project means a second cannot even
	// be written.
	sync(t, emailAbout("ow1@acme.example", "", "[ERP] renamed"))
	if got := linkedProject(t, e, "ow1@acme.example"); got != crm {
		t.Fatalf("a replay moved the message to %s, want the original %s (erp is %s)", got, crm, erp)
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM activity a
		  JOIN activity_link al ON al.activity_id = a.id AND al.entity_type = 'project'
		 WHERE a.source_id = 'ow1@acme.example'`); n != 1 {
		t.Fatalf("%d project links on one activity, want exactly 1", n)
	}
}
