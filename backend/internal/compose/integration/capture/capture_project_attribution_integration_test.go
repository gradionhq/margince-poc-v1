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
				// Delete is the archive grant, which the fall-through case needs
				// to retire a project through the real store.
				"project":      {Create: true, Read: true, Update: true, Delete: true},
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

// archiveProject retires a project through the store that owns it, so the row
// the ladder then meets is the one a real archive leaves behind.
func (s projectSeeder) archiveProject(t *testing.T, projectID ids.UUID) {
	t.Helper()
	if _, err := s.store.ArchiveProject(s.ctx, ids.From[ids.ProjectKind](projectID), nil); err != nil {
		t.Fatalf("archiving project %s: %v", projectID, err)
	}
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

// capturedStamp is the retention class and the frozen evidence on a captured
// message — what the ladder must write BESIDE the link, in the same
// transaction, because filing under a project qualifies the correspondence.
type capturedStamp struct {
	class       *string
	evidence    int
	projectName *string
}

func stampOfCaptured(t *testing.T, e *integration.SearchEnv, sourceID string) capturedStamp {
	t.Helper()
	var got capturedStamp
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT a.retention_class,
			       (SELECT count(*) FROM activity_retention_evidence e
			         WHERE e.activity_id = a.id AND e.basis = 'project_linked'),
			       (SELECT max(e.project_name) FROM activity_retention_evidence e
			         WHERE e.activity_id = a.id AND e.basis = 'project_linked')
			  FROM activity a WHERE a.source_id = $1`, sourceID).
			Scan(&got.class, &got.evidence, &got.projectName)
	}); err != nil {
		t.Fatalf("reading the captured message's stamp: %v", err)
	}
	return got
}

// The ladder classifies what it files. Capture is the highest-volume writer of
// project links, so an unstamped one here is the most likely way a Handelsbrief
// reaches an erasure unclassified — and the stamp commits with the link rather
// than trailing it, so there is no window at all.
func TestCaptureStampsTheMessageItFilesUnderAProject(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	seed.project(t, "ERP replacement", "ERP")

	sync(t,
		emailAbout("stamp1@acme.example", "", "[ERP] weekly status"),
		emailAbout("stamp2@acme.example", "", "lunch on Thursday"),
	)

	filed := stampOfCaptured(t, e, "stamp1@acme.example")
	if filed.class == nil {
		t.Fatal("the ladder filed a message under a project and left it unclassified — an unstamped Handelsbrief is one the next erasure destroys")
	}
	if *filed.class != "commercial_correspondence" {
		t.Errorf("retention_class = %q, want commercial_correspondence", *filed.class)
	}
	if filed.evidence != 1 {
		t.Errorf("project_linked evidence rows = %d, want 1 — a class with nothing behind it is an assertion the controller cannot substantiate", filed.evidence)
	}
	if filed.projectName == nil || *filed.projectName != "ERP replacement" {
		t.Errorf("evidence project_name = %v, want the project's name frozen at qualification", filed.projectName)
	}

	// The other half of the rule: mail belonging to no project is not
	// correspondence this product must keep, and must NOT be stamped. A writer
	// that stamped everything would pass the assertion above while quietly
	// putting a six-year floor on the whole mailbox.
	unfiled := stampOfCaptured(t, e, "stamp2@acme.example")
	if unfiled.class != nil {
		t.Errorf("a message the ladder filed under nothing carries class %q; only a project link qualifies it", *unfiled.class)
	}
	if unfiled.evidence != 0 {
		t.Errorf("project_linked evidence rows = %d on an unattributed message, want 0", unfiled.evidence)
	}
}

// The subject-key rung and every way it refuses: a key only counts bracketed,
// never as a bare word, never as a substring, and never when two are named.
func TestCaptureFilesAMessageUnderTheProjectItsSubjectNames(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	erp := seed.project(t, "ERP replacement", "ERP")
	seed.project(t, "CRM rollout", "CRM")
	// A project keyed RE is the case the bracket rule exists for: without it,
	// every "Re:" in the installation files here.
	seed.project(t, "Regulatory review", "RE")

	sync(t,
		emailAbout("pk1@acme.example", "", "[ERP] weekly status"),
		emailAbout("pk2@acme.example", "", "[ERPNEXT] evaluation"),
		emailAbout("pk3@acme.example", "", "[ERP] and [CRM] together"),
		emailAbout("pk4@acme.example", "", "lunch on Thursday"),
		emailAbout("pk5@acme.example", "", "ERP weekly status"),
		emailAbout("pk6@acme.example", "", "Re: your message"),
	)

	if got := linkedProject(t, e, "pk1@acme.example"); got != erp {
		t.Fatalf("the subject naming [ERP] filed the message under %s, want the ERP project %s", got, erp)
	}
	// A key must be the whole bracketed token: ERPNEXT is a different word, and
	// nothing downstream of this ladder would catch the message landing on ERP.
	if got := linkedProject(t, e, "pk2@acme.example"); !got.IsZero() {
		t.Fatalf("[ERPNEXT] filed a message under project %s; a key must never match a substring", got)
	}
	// Two projects named in one subject is not evidence for either.
	if got := linkedProject(t, e, "pk3@acme.example"); !got.IsZero() {
		t.Fatalf("an ambiguous subject filed a message under project %s, want nothing", got)
	}
	// Most mail belongs to no project, and that is the correct answer.
	if got := linkedProject(t, e, "pk4@acme.example"); !got.IsZero() {
		t.Fatalf("a subject naming no project filed a message under %s, want nothing", got)
	}
	// A bare word is not a key reference, however exactly it spells one.
	if got := linkedProject(t, e, "pk5@acme.example"); !got.IsZero() {
		t.Fatalf("an unbracketed ERP filed a message under %s; a key counts only in brackets", got)
	}
	// The mass-misfiling case: a bare "Re:" must reach no project, even when a
	// project is keyed RE.
	if got := linkedProject(t, e, "pk6@acme.example"); !got.IsZero() {
		t.Fatalf("a plain reply filed under %s — a project keyed RE must not swallow every Re:", got)
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

// revokeCaptureGrant drops one object grant from the live capture role. The
// production authority re-resolves the granting human's role on every sync, so
// this is a real permission change rather than a fixture pretending to be one.
func revokeCaptureGrant(t *testing.T, e *integration.SearchEnv, object string) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE role SET permissions = jsonb_set(permissions, '{objects}', (permissions->'objects') - $1)
			  WHERE key = 'capture_rep'`, object)
		return err
	})
	if err != nil {
		t.Fatalf("revoking the %s grant: %v", object, err)
	}
}

// A principal that may not read projects never has one copied onto its
// timeline. EnsureLinkTarget at the write asks whether a row is VISIBLE, which
// is a different question from whether this role may open the project table at
// all — so the ladder asks the object grant itself, up front.
func TestCaptureAttributesNothingWithoutTheProjectReadGrant(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	erp := seed.project(t, "ERP replacement", "ERP")

	// The positive control first, on the role as seeded: without it a revoked
	// grant and a broken ladder look identical.
	sync(t, emailAbout("pg1@acme.example", "", "[ERP] before"))
	if got := linkedProject(t, e, "pg1@acme.example"); got != erp {
		t.Fatalf("the ladder filed under %s before the revoke, want %s — the fixture proves nothing", got, erp)
	}

	// A deal on that project too, so the revoked role's mail can reach a
	// project through EVERY rung rather than only the subject one.
	dealID := seed.dealOnProject(t, "Phase two", erp)

	revokeCaptureGrant(t, e, "project")

	// The subject rung.
	sync(t, emailAbout("pg2@acme.example", "", "[ERP] after"))
	if got := linkedProject(t, e, "pg2@acme.example"); !got.IsZero() {
		t.Fatalf("the subject rung filed %s for a role with no project grant", got)
	}
	// The THREAD rung, which reads project_id straight off a sibling's link
	// row. It is the one that reaches a project id without ever touching the
	// module that owns the table, so it needs the gate most.
	sync(t, emailAbout("pg3@acme.example", "pg1@acme.example", "Re: no key here"))
	if got := linkedProject(t, e, "pg3@acme.example"); !got.IsZero() {
		t.Fatalf("the thread rung filed %s for a role with no project grant", got)
	}
	// The DEAL rung, which reaches a project id by joining through the deal.
	env.syncFiledUnderDeal(t, map[string][]ids.UUID{"pg4@acme.example": {dealID}},
		emailAbout("pg4@acme.example", "", "no key here either"))
	if got := linkedProject(t, e, "pg4@acme.example"); !got.IsZero() {
		t.Fatalf("the deal rung filed %s for a role with no project grant", got)
	}
}

// Filing requires activity.update, not the activity.create the capture already
// made. Bumping the version and changing who the activity reaches is an update
// by every test that matters, and the audit row claims that grant — so the
// write has to actually hold it.
func TestCaptureAttributesNothingWithoutTheActivityUpdateGrant(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	erp := seed.project(t, "ERP replacement", "ERP")

	sync(t, emailAbout("ag1@acme.example", "", "[ERP] before"))
	if got := linkedProject(t, e, "ag1@acme.example"); got != erp {
		t.Fatalf("the ladder filed under %s before the revoke, want %s — the fixture proves nothing", got, erp)
	}

	// Narrow the role to create+read, which is what an ordinary capture-only
	// role holds. Capture must go on working; only the filing stops.
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(context.Background(), `
			UPDATE role
			   SET permissions = jsonb_set(permissions, '{objects,activity}',
			                               '{"create":true,"read":true}'::jsonb)
			 WHERE key = 'capture_rep'`)
		return execErr
	})
	if err != nil {
		t.Fatalf("narrowing the activity grant: %v", err)
	}
	sync(t, emailAbout("ag2@acme.example", "", "[ERP] after"))

	// The message still lands — losing the filing must never cost the mail.
	if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id = 'ag2@acme.example'`); n != 1 {
		t.Fatalf("%d activities for a message captured without activity.update, want 1", n)
	}
	if got := linkedProject(t, e, "ag2@acme.example"); !got.IsZero() {
		t.Fatalf("a role without activity.update had %s filed onto its mail", got)
	}
}

// Thread stickiness holds within ONE medium. thread_key is a single flat
// namespace across mail and every channel, and the mail half is chosen verbatim
// by the sender in a References header — so a forged root naming another
// medium's conversation must not file the forger's mail onto that
// conversation's project.
func TestCaptureDoesNotInheritAProjectAcrossMedia(t *testing.T) {
	env := newCaptureEnv(t)
	e := env.e
	seed := newProjectSeeder(t, e)
	erp := seed.project(t, "ERP replacement", "ERP")

	// The sibling lands as a meeting, filed under ERP by its own subject.
	env.syncAsKind(t, map[string]string{"xm1@acme.example": "meeting"},
		emailAbout("xm1@acme.example", "", "[ERP] kickoff"))
	if got := linkedProject(t, e, "xm1@acme.example"); got != erp {
		t.Fatalf("the meeting landed on %s, want %s — the fixture itself is wrong", got, erp)
	}
	// An email quoting that thread root. Same thread_key, different medium.
	env.sync(t, emailAbout("xm2@acme.example", "xm1@acme.example", "no key here"))

	if got := linkedProject(t, e, "xm2@acme.example"); !got.IsZero() {
		t.Fatalf("mail inherited %s across media from a meeting's thread; the medium match is what stops a forged References header", got)
	}
}

// An unusable match on a higher rung does not end the ladder. The thread's
// project is archived here, so T0 has nothing to offer — and the subject's own
// key must still be read, rather than the message ending up filed under nothing
// because a rung above it matched something dead.
func TestCaptureFallsThroughAnArchivedThreadProject(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	stale := seed.project(t, "Retired programme", "OLD")
	live := seed.project(t, "ERP replacement", "ERP")

	sync(t, emailAbout("ft1@acme.example", "", "[OLD] kickoff"))
	if got := linkedProject(t, e, "ft1@acme.example"); got != stale {
		t.Fatalf("the thread root landed on %s, want %s — the fixture itself is wrong", got, stale)
	}
	seed.archiveProject(t, stale)

	// A reply on that thread, naming a LIVE project in its own subject. T0
	// finds the archived one, which is no match; T1 must then still run.
	sync(t, emailAbout("ft2@acme.example", "ft1@acme.example", "Re: [ERP] kickoff"))

	if got := linkedProject(t, e, "ft2@acme.example"); got != live {
		t.Fatalf("the reply landed on %s, want %s — an archived thread match must fall through, not end the ladder", got, live)
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

	// A second project, and a deal on it, so one message can name two projects
	// through two deals — the ambiguity this rung has to refuse.
	crm := seed.project(t, "CRM rollout", "CRM")
	onOtherProject := seed.dealOnProject(t, "CRM phase one", crm)

	env.syncFiledUnderDeal(t,
		map[string][]ids.UUID{
			"dl1@acme.example": {onProject},
			"dl2@acme.example": {offProject},
			"dl3@acme.example": {onProject, onOtherProject},
			"dl4@acme.example": {onProject, offProject},
		},
		emailAbout("dl1@acme.example", "", "quick question"),
		emailAbout("dl2@acme.example", "", "separate question"),
		emailAbout("dl3@acme.example", "", "third question"),
		emailAbout("dl4@acme.example", "", "fourth question"),
	)

	if got := linkedProject(t, e, "dl1@acme.example"); got != erp {
		t.Fatalf("the message on a project's deal landed on %s, want %s", got, erp)
	}
	// A deal that belongs to no project inherits nothing — there is nothing to
	// inherit, and inventing one would be the guess this ladder never makes.
	if got := linkedProject(t, e, "dl2@acme.example"); !got.IsZero() {
		t.Fatalf("a deal with no project filed its message under %s, want nothing", got)
	}
	// Two deals naming two DIFFERENT projects is ambiguity, answered the same
	// way an ambiguous subject key is: with no project at all.
	if got := linkedProject(t, e, "dl3@acme.example"); !got.IsZero() {
		t.Fatalf("two deals on two projects filed the message under %s, want nothing", got)
	}
	// Two deals resolving to ONE project is not ambiguity — the second deal
	// carries no project and so contributes no rival answer.
	if got := linkedProject(t, e, "dl4@acme.example"); got != erp {
		t.Fatalf("two deals agreeing on one project filed the message under %s, want %s", got, erp)
	}
}

// Filing an activity under a project MOVES its version, and audits the move
// under the verb whose grant the write actually required.
//
// The version is what a staged approval pins: without the bump, an approval
// authorized against "this conversation" still redeems after the ladder has
// repointed the conversation underneath it. The audit row matters for the
// mirror-image reason — audit_log is append-only, so a row naming a rule the
// write never checked is a lie nobody can correct later.
func TestCaptureFilingBumpsTheActivityVersionAndAuditsIt(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	seed := newProjectSeeder(t, e)
	seed.project(t, "ERP replacement", "ERP")

	sync(t, emailAbout("vb1@acme.example", "", "[ERP] kickoff"))
	sync(t, emailAbout("vb2@acme.example", "", "no key at all"))

	// A freshly captured row is born at version 1; the filing is the only thing
	// that has touched vb1 since, so anything above 1 is that bump and nothing
	// else. vb2 is the control: same capture, no filing, still 1.
	if v := countRows(t, e, `SELECT version FROM activity WHERE source_id = 'vb1@acme.example'`); v <= 1 {
		t.Fatalf("the filed activity is at version %d — filing must move the version a staged approval pins", v)
	}
	if v := countRows(t, e, `SELECT version FROM activity WHERE source_id = 'vb2@acme.example'`); v != 1 {
		t.Fatalf("an unfiled activity is at version %d, want 1 — nothing but a filing may move it", v)
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM audit_log al
		  JOIN activity a ON a.id = al.entity_id
		 WHERE a.source_id = 'vb1@acme.example' AND al.action = 'activity_relink'`); n != 1 {
		t.Fatalf("%d activity_relink audit rows for the filing, want exactly 1", n)
	}
	// The rule the ledger records has to be the grant the write really took.
	if n := countRows(t, e, `
		SELECT count(*) FROM audit_log al
		  JOIN activity a ON a.id = al.entity_id
		 WHERE a.source_id = 'vb1@acme.example' AND al.action = 'activity_relink'
		   AND al.authorization_rule LIKE '%activity.update%'`); n != 1 {
		t.Fatal("the filing's audit row must name activity.update — the grant linkActivityToProject requires")
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
