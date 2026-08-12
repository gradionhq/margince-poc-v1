// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// A flip landing is one transaction: the native record and the identity-map
// row that names it commit together, or neither does.
//
// The two used to be separate transactions, and a process that died between
// them left a record the resume could not see — which is what flipreconcile.go
// exists to repair, by scanning for live rows carrying the reserved import
// provenance that the map does not know. That repair stays, for the orphans an
// estate already carries; what these suites pin is that no class produces new
// ones. All five land atomically, and the deal's remaining window — mapped and
// open, because the terminal stage is asserted in a second transaction — is
// settleAdoptedDeal's to close.
//
// The forced failure is the production one rather than a test hook: the
// identity row's composite FK names the import run, so a landing recorded
// against a run this workspace does not have fails at exactly the point a
// crash would — after the record is written, before the transaction commits.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// landingPerms is the admin fixture plus the import-run grant the engine
// takes: the flip runs as an operator who may land an estate, and AdminPerms
// deliberately does not carry that.
func landingPerms() principal.Permissions {
	perms := integration.AdminPerms
	objects := make(map[string]principal.ObjectGrant, len(perms.Objects)+1)
	for object, grant := range perms.Objects {
		objects[object] = grant
	}
	objects["import_run"] = principal.ObjectGrant{Create: true, Read: true, Update: true}
	perms.Objects = objects
	return perms
}

// landingPermsWithoutDealUpdate is the landing seat minus deal:update, so a
// closed estate deal lands and then fails to advance.
func landingPermsWithoutDealUpdate() principal.Permissions {
	perms := landingPerms()
	objects := make(map[string]principal.ObjectGrant, len(perms.Objects))
	for object, grant := range perms.Objects {
		objects[object] = grant
	}
	deal := objects["deal"]
	deal.Update = false
	objects["deal"] = deal
	perms.Objects = objects
	return perms
}

// landingFixture is the flip writer under test, bound to a real import run.
type landingFixture struct {
	e   *integration.Env
	w   *flipWriters
	ctx context.Context
	// noUpdateCtx may land a deal and not advance it — the seat that stops a
	// landing exactly where a crash between the two transactions would.
	noUpdateCtx context.Context
}

func setupLanding(t *testing.T) landingFixture {
	t.Helper()
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, nil, landingPerms())
	operator := ids.From[ids.UserKind](e.Rep1)
	run, err := migration.NewRunStore(e.DB()).Create(ctx, migration.CreateRunInput{
		Connector: "hubspot", SourceRef: "landing-suite", Source: "overlay:flip",
	})
	if err != nil {
		t.Fatalf("creating the import run: %v", err)
	}
	// The mirror store is nil deliberately: every row below names no
	// incumbent owner, and that path answers with the operator without
	// consulting the mirror. A fixture that wired one would be claiming
	// coverage of a resolution these suites do not exercise.
	w := newFlipWriters(e.DB(), nil, "hubspot").forRun(run.ID, &operator)
	return landingFixture{e: e, w: w, ctx: ctx, noUpdateCtx: e.As(e.Rep1, nil, landingPermsWithoutDealUpdate())}
}

// brokenRun answers a SEPARATE writer bound to a run id no workspace holds, so
// the identity write fails after the record is created. Separate because
// forRun re-binds its receiver and hands the same pointer back: rebinding the
// fixture's own writer would leave every later call in the test landing
// against the broken run.
func (f landingFixture) brokenRun() *flipWriters {
	operator := ids.From[ids.UserKind](f.e.Rep1)
	return f.freshWriter().forRun(ids.NewV7(), &operator)
}

// freshWriter builds another writer over the same workspace and incumbent —
// the shape a resumed run has, with an empty cache of its own.
func (f landingFixture) freshWriter() *flipWriters {
	operator := ids.From[ids.UserKind](f.e.Rep1)
	return newFlipWriters(f.e.DB(), nil, "hubspot").forRun(f.w.runID, &operator)
}

func landingRow(ext string, fields map[string]any) migration.Row {
	return migration.Row{ExternalID: ext, Fields: fields}
}

func TestFlipLandsAPersonAndItsIdentityInOneTransaction(t *testing.T) {
	f := setupLanding(t)

	res, err := f.w.Ensure(f.ctx, flipObjectPerson, landingRow("hs-person-1", map[string]any{"full_name": "Ada Lovelace"}))
	if err != nil {
		t.Fatalf("landing the person: %v", err)
	}
	if !res.Created {
		t.Fatalf("result = %+v, want a created person", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = 'Ada Lovelace'`); n != 1 {
		t.Errorf("person rows = %d, want 1", n)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map m JOIN person p ON p.id = m.native_id
		WHERE m.object = 'person' AND m.external_id = 'hs-person-1' AND p.full_name = 'Ada Lovelace'`); n != 1 {
		t.Errorf("mapped persons = %d, want 1 — the landing committed the record without its map row, or a map row naming nothing", n)
	}
}

func TestAFailedIdentityWriteLeavesNoPersonBehind(t *testing.T) {
	f := setupLanding(t)
	audits := f.e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'person'`)

	_, err := f.brokenRun().Ensure(f.ctx, flipObjectPerson, landingRow("hs-person-2", map[string]any{"full_name": "Grace Hopper"}))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want the identity write's refusal for a run this workspace does not hold", err)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = 'Grace Hopper'`); n != 0 {
		t.Errorf("person rows = %d, want 0 — the record outlived the transaction that was supposed to carry its identity, which is the orphan the reconcile has to clean up", n)
	}
	// The write shape's audit row rides the same transaction as the record, so
	// it must be gone too. Counted as a delta rather than as an absence, so
	// this stays an assertion about the landing even if the fixture ever seeds
	// a person of its own.
	if n := f.e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'person'`); n != audits {
		t.Errorf("person audit rows = %d, want the %d there were before the failed landing — the audit row committed without the record it describes", n, audits)
	}
}

// The cache is what `lookup` answers from before it ever asks the map, so an
// entry written for a rolled-back landing would make this run's later pages —
// and the association phase — resolve an id that does not exist.
func TestARolledBackLandingCachesNothing(t *testing.T) {
	f := setupLanding(t)
	broken := f.brokenRun()

	if _, err := broken.Ensure(f.ctx, flipObjectPerson, landingRow("hs-person-3", map[string]any{"full_name": "Katherine Johnson"})); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want the identity write's refusal", err)
	}
	if _, found, err := broken.lookup(f.ctx, flipObjectPerson, "hs-person-3"); err != nil {
		t.Fatalf("lookup after the failed landing: %v", err)
	} else if found {
		t.Error("the run cache names a person the failed landing never committed, so the resume would skip creating it")
	}
}

func TestFlipLandsAnOrganizationAndItsIdentityInOneTransaction(t *testing.T) {
	f := setupLanding(t)

	res, err := f.w.Ensure(f.ctx, flipObjectOrganization, landingRow("hs-org-1", map[string]any{"display_name": "Analytical Engines"}))
	if err != nil {
		t.Fatalf("landing the organization: %v", err)
	}
	if !res.Created {
		t.Fatalf("result = %+v, want a created organization", res)
	}
	// The map row is joined back to the record it names: import_record_map
	// carries no FK to the native tables, so counting it alone would pass over
	// a map row pointing at nothing.
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map m JOIN organization o ON o.id = m.native_id
		WHERE m.object = 'organization' AND m.external_id = 'hs-org-1' AND o.display_name = 'Analytical Engines'`); n != 1 {
		t.Errorf("mapped organizations = %d, want 1 — the identity row and the record it names must both be there", n)
	}
}

func TestAFailedIdentityWriteLeavesNoOrganizationBehind(t *testing.T) {
	f := setupLanding(t)

	if _, err := f.brokenRun().Ensure(f.ctx, flipObjectOrganization, landingRow("hs-org-2", map[string]any{"display_name": "Difference Engines"})); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want the identity write's refusal — any other error means the landing failed before it, and this arm proved nothing about the rollback", err)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM organization WHERE display_name = 'Difference Engines'`); n != 0 {
		t.Errorf("organization rows = %d, want 0 — an orphan the resume cannot name", n)
	}
}

func TestFlipLandsALeadAndItsIdentityInOneTransaction(t *testing.T) {
	f := setupLanding(t)

	res, err := f.w.Ensure(f.ctx, flipObjectLead, landingRow("hs-lead-1", map[string]any{"full_name": "Jean Bartik", "email": "jean@bartik.test"}))
	if err != nil {
		t.Fatalf("landing the lead: %v", err)
	}
	if !res.Created {
		t.Fatalf("result = %+v, want a created lead", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map m JOIN lead l ON l.id = m.native_id
		WHERE m.object = 'lead' AND m.external_id = 'hs-lead-1' AND l.email = 'jean@bartik.test'`); n != 1 {
		t.Errorf("mapped leads = %d, want 1 — the identity row and the record it names must both be there", n)
	}
}

func TestAFailedIdentityWriteLeavesNoLeadBehind(t *testing.T) {
	f := setupLanding(t)

	if _, err := f.brokenRun().Ensure(f.ctx, flipObjectLead, landingRow("hs-lead-2", map[string]any{"full_name": "Betty Holberton", "email": "betty@holberton.test"})); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want the identity write's refusal — any other error means the landing failed before it, and this arm proved nothing about the rollback", err)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM lead WHERE email = 'betty@holberton.test'`); n != 0 {
		t.Errorf("lead rows = %d, want 0 — an orphan the resume cannot name", n)
	}
}

// The replay path writes nothing, and must not be recorded: a lead the store
// answered with under its natural key was created by something else, and
// mapping it would make the next attempt report the estate converged with a
// disclosure nobody ever saw.
func TestALeadReplayedUnderItsNaturalKeyIsSkippedAndNotMapped(t *testing.T) {
	f := setupLanding(t)
	row := landingRow("hs-lead-3", map[string]any{"full_name": "Frances Spence", "email": "frances@spence.test"})

	if _, err := f.w.Ensure(f.ctx, flipObjectLead, row); err != nil {
		t.Fatalf("landing the lead: %v", err)
	}
	// A second run over the same estate row, with the identity map emptied
	// under it: the store replays its own idempotency key while the map has
	// no record of it — the exact state the skip exists for.
	f.e.WsExec(t, `DELETE FROM import_record_map WHERE object = 'lead' AND external_id = 'hs-lead-3'`)
	second := f.freshWriter()

	res, err := second.Ensure(f.ctx, flipObjectLead, row)
	if err != nil {
		t.Fatalf("the replayed landing: %v", err)
	}
	if !res.Skipped || res.SkipReason != skipReasonNaturalKeyTaken {
		t.Fatalf("result = %+v, want a skip naming the taken natural key", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map WHERE object = 'lead' AND external_id = 'hs-lead-3'`); n != 0 {
		t.Error("the replay recorded an identity for a lead this run did not create — the next attempt would report it converged")
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM lead WHERE email = 'frances@spence.test'`); n != 1 {
		t.Errorf("lead rows = %d, want the one the first landing created", n)
	}
}

// An estate contact whose email a native person already holds is disclosed as
// a skip rather than merged — and this is the one preserved behaviour that now
// depends on the store's error travelling out through a rolled-back landing.
// Without this arm, a refactor that wrapped the landing error without %w would
// turn every such skip into a failed run and nothing would go red.
func TestAContactWhoseEmailIsTakenIsSkippedAndLeavesNothingBehind(t *testing.T) {
	f := setupLanding(t)
	const taken = "ada@lovelace.test"
	if _, err := f.e.People.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Ada Lovelace", Source: "ui",
		Emails: []people.PersonEmailInput{{Email: taken, EmailType: "work", IsPrimary: true}},
	}); err != nil {
		t.Fatalf("seeding the native person who already holds the email: %v", err)
	}

	res, err := f.w.Ensure(f.ctx, flipObjectPerson, migration.Row{
		ExternalID: "hs-person-dup",
		// The email rides the nested TargetChild map the mapper writes, which
		// is the shape overlayPersonEmail reads — a flat "email" key is
		// silently ignored, and with it the whole duplicate check.
		Fields: map[string]any{"full_name": "A. Lovelace", "person_email": map[string]any{"email": taken}},
	})
	if err != nil {
		t.Fatalf("the duplicate-email landing answered an error rather than a disclosed skip: %v", err)
	}
	if !res.Skipped || res.SkipReason != skipReasonDuplicateEmail {
		t.Fatalf("result = %+v, want a skip naming the duplicate email", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = 'A. Lovelace'`); n != 0 {
		t.Errorf("person rows = %d, want 0 — the estate contact must not land beside the person who holds its email", n)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map WHERE object = 'person' AND external_id = 'hs-person-dup'`); n != 0 {
		t.Error("the skipped contact was mapped, so the next attempt would report it converged")
	}
}

func TestFlipLandsAnActivityAndItsIdentityInOneTransaction(t *testing.T) {
	f := setupLanding(t)

	res, err := f.w.Ensure(f.ctx, flipObjectActivity, landingRow("hs-act-1", map[string]any{"kind": "note", "body": "a call happened"}))
	if err != nil {
		t.Fatalf("landing the activity: %v", err)
	}
	if !res.Created {
		t.Fatalf("result = %+v, want a created activity", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map m JOIN activity a ON a.id = m.native_id
		WHERE m.object = 'activity' AND m.external_id = 'hs-act-1'`); n != 1 {
		t.Errorf("mapped activities = %d, want 1", n)
	}
}

func TestAFailedIdentityWriteLeavesNoActivityBehind(t *testing.T) {
	f := setupLanding(t)

	if _, err := f.brokenRun().Ensure(f.ctx, flipObjectActivity, landingRow("hs-act-2", map[string]any{"kind": "note", "body": "never committed"})); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want the identity write's refusal", err)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM activity WHERE body = 'never committed'`); n != 0 {
		t.Errorf("activity rows = %d, want 0 — an orphan the resume cannot name", n)
	}
}

// A deal lands open and is advanced afterwards, so only its LANDING is one
// transaction. That is the window this closes; the close's own window stays
// settleAdoptedDeal's to finish, which is why the reconcile keeps its deal arm.
func TestFlipLandsADealAndItsIdentityInOneTransaction(t *testing.T) {
	f := setupLanding(t)
	// A deal needs somewhere to be born: the flip resolves the workspace's
	// default pipeline and its first open stage.
	integration.DealFixture(t, f.e)

	res, err := f.w.Ensure(f.ctx, flipObjectDeal, landingRow("hs-deal-1", map[string]any{"name": "Analytical Engine order"}))
	if err != nil {
		t.Fatalf("landing the deal: %v", err)
	}
	if !res.Created {
		t.Fatalf("result = %+v, want a created deal", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map m JOIN deal d ON d.id = m.native_id
		WHERE m.object = 'deal' AND m.external_id = 'hs-deal-1' AND d.name = 'Analytical Engine order'`); n != 1 {
		t.Errorf("mapped deals = %d, want 1", n)
	}
}

func TestAFailedIdentityWriteLeavesNoDealBehind(t *testing.T) {
	f := setupLanding(t)
	integration.DealFixture(t, f.e)

	// ErrNotFound alone would not be enough here: the deal create maps its own
	// FK misses to the same sentinel, so an arm that accepted it could pass on
	// a create that never reached the identity write. The identity write is the
	// one that names the run it could not find.
	_, err := f.brokenRun().Ensure(f.ctx, flipObjectDeal, landingRow("hs-deal-2", map[string]any{"name": "Difference Engine order"}))
	if !errors.Is(err, apperrors.ErrNotFound) || !strings.Contains(err.Error(), "import run") {
		t.Fatalf("err = %v, want the identity write's refusal naming the run", err)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM deal WHERE name = 'Difference Engine order'`); n != 0 {
		t.Errorf("deal rows = %d, want 0 — an orphan the resume cannot name", n)
	}
}

// The replay path each idempotent class carries: the store answers with a
// record that already exists under its natural key, so the landing wrote
// nothing and must map nothing. The lead's arm is above; this is the
// activity's, which routes through the same sentinel.
func TestAnActivityReplayedUnderItsNaturalKeyIsSkippedAndNotMapped(t *testing.T) {
	f := setupLanding(t)
	row := landingRow("hs-act-3", map[string]any{"kind": "note", "body": "logged once"})

	if _, err := f.w.Ensure(f.ctx, flipObjectActivity, row); err != nil {
		t.Fatalf("landing the activity: %v", err)
	}
	// The map emptied under a second run: the store replays its own
	// idempotency key while the map has no record of it.
	f.e.WsExec(t, `DELETE FROM import_record_map WHERE object = 'activity' AND external_id = 'hs-act-3'`)

	res, err := f.freshWriter().Ensure(f.ctx, flipObjectActivity, row)
	if err != nil {
		t.Fatalf("the replayed landing: %v — the sentinel escaped as a real failure", err)
	}
	if !res.Skipped || res.SkipReason != skipReasonNaturalKeyTaken {
		t.Fatalf("result = %+v, want a skip naming the taken natural key", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map WHERE object = 'activity' AND external_id = 'hs-act-3'`); n != 0 {
		t.Error("the replay recorded an identity for an activity this run did not create")
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM activity WHERE body = 'logged once'`); n != 1 {
		t.Errorf("activity rows = %d, want the one the first landing created", n)
	}
}

// A closed estate deal is the two-transaction case that survives on purpose:
// the landing commits the OPEN deal with its identity row, and the terminal
// stage is asserted afterwards. Both halves have to happen for the estate to
// arrive closed.
func TestAClosedEstateDealLandsMappedAndThenReachesItsTerminalStage(t *testing.T) {
	f := setupLanding(t)
	integration.DealFixture(t, f.e)

	// "closedwon" is the incumbent's own stage vocabulary, which is what the
	// estate carries; the catalog resolves it to this workspace's won stage.
	res, err := f.w.Ensure(f.ctx, flipObjectDeal, landingRow("hs-deal-won", map[string]any{
		"name": "Closed estate order", "stage_id": "closedwon",
	}))
	if err != nil {
		t.Fatalf("landing the closed deal: %v", err)
	}
	if !res.Created {
		t.Fatalf("result = %+v, want a created deal", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map m JOIN deal d ON d.id = m.native_id
		WHERE m.object = 'deal' AND m.external_id = 'hs-deal-won' AND d.status = 'won'`); n != 1 {
		t.Errorf("mapped won deals = %d, want 1 — the landing and the advance both have to land for the estate to arrive closed", n)
	}
}

// The resume after the landing committed and the advance did not.
//
// The state is built the way production reaches it, from the SAME frozen
// estate row both times: the first pass runs under a seat that may create a
// deal but not update one, so the landing commits and AdvanceDeal refuses,
// leaving the deal mapped and open. The retry then has to settle it rather
// than report the estate converged.
func TestAMappedButOpenDealIsClosedOnTheNextPass(t *testing.T) {
	f := setupLanding(t)
	integration.DealFixture(t, f.e)
	row := landingRow("hs-deal-resume", map[string]any{
		"name": "Interrupted order", "stage_id": "closedwon",
	})

	if _, err := f.w.Ensure(f.noUpdateCtx, flipObjectDeal, row); err == nil {
		t.Fatal("the first pass closed the deal, so there is no interrupted landing to recover from")
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM deal d JOIN import_record_map m ON m.native_id = d.id
		WHERE m.external_id = 'hs-deal-resume' AND d.status = 'open'`); n != 1 {
		t.Fatalf("mapped open deals after the interrupted pass = %d, want 1 — the state this test recovers from", n)
	}

	res, err := f.freshWriter().Ensure(f.ctx, flipObjectDeal, row)
	if err != nil {
		t.Fatalf("the resumed pass: %v", err)
	}
	if res.Created {
		t.Fatalf("result = %+v, want the mapped deal settled rather than a second one landed", res)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM deal d JOIN import_record_map m ON m.native_id = d.id
		WHERE m.external_id = 'hs-deal-resume' AND d.status = 'won'`); n != 1 {
		t.Errorf("closed deals = %d, want the mapped one settled — a deal parked open here is counted as converged and never closes", n)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM deal WHERE name = 'Interrupted order'`); n != 1 {
		t.Errorf("deal rows = %d, want exactly the one the interrupted pass landed", n)
	}
}
