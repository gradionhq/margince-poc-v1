// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// A flip landing is one transaction: the native record and the identity-map
// row that names it commit together, or neither does.
//
// Before this, the two were separate transactions and a process that died
// between them left a record the resume could not see — which is what
// flipreconcile.go exists to repair, by scanning for live rows carrying the
// reserved import provenance that the map does not know. That repair stays
// (it adopts orphans from attempts that predate this change, and from the
// classes that still land in two steps); what these suites pin is that the
// three people classes stop producing new ones.
//
// The forced failure is the production one rather than a test hook: the
// identity row's composite FK names the import run, so a landing recorded
// against a run this workspace does not have fails at exactly the point a
// crash would — after the record is written, before the transaction commits.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
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

// landingFixture is the flip writer under test, bound to a real import run.
type landingFixture struct {
	e   *integration.Env
	w   *flipWriters
	ctx context.Context
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
	return landingFixture{e: e, w: w, ctx: ctx}
}

// brokenRun re-binds the writer to a run id no workspace holds, so the
// identity write fails after the record is created.
func (f landingFixture) brokenRun() *flipWriters {
	operator := ids.From[ids.UserKind](f.e.Rep1)
	return f.w.forRun(ids.NewV7(), &operator)
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
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map WHERE object = 'person' AND external_id = 'hs-person-1'`); n != 1 {
		t.Errorf("identity rows = %d, want 1 — the landing committed the record without its map row", n)
	}
}

func TestAFailedIdentityWriteLeavesNoPersonBehind(t *testing.T) {
	f := setupLanding(t)

	_, err := f.brokenRun().Ensure(f.ctx, flipObjectPerson, landingRow("hs-person-2", map[string]any{"full_name": "Grace Hopper"}))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want the identity write's refusal for a run this workspace does not hold", err)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = 'Grace Hopper'`); n != 0 {
		t.Errorf("person rows = %d, want 0 — the record outlived the transaction that was supposed to carry its identity, which is the orphan the reconcile has to clean up", n)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'person'`); n != 0 {
		t.Errorf("audit rows = %d, want 0 — the write shape's audit row committed without the record it describes", n)
	}
}

// The cache is what `lookup` answers from before it ever asks the map, so an
// entry written for a rolled-back landing would make this run's later pages —
// and the association phase — resolve an id that does not exist.
func TestARolledBackLandingCachesNothing(t *testing.T) {
	f := setupLanding(t)
	broken := f.brokenRun()

	if _, err := broken.Ensure(f.ctx, flipObjectPerson, landingRow("hs-person-3", map[string]any{"full_name": "Katherine Johnson"})); err == nil {
		t.Fatal("the landing succeeded against a run this workspace does not hold")
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
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map WHERE object = 'organization' AND external_id = 'hs-org-1'`); n != 1 {
		t.Errorf("identity rows = %d, want 1", n)
	}
}

func TestAFailedIdentityWriteLeavesNoOrganizationBehind(t *testing.T) {
	f := setupLanding(t)

	if _, err := f.brokenRun().Ensure(f.ctx, flipObjectOrganization, landingRow("hs-org-2", map[string]any{"display_name": "Difference Engines"})); err == nil {
		t.Fatal("the landing succeeded against a run this workspace does not hold")
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
	if n := f.e.WsCount(t, `SELECT count(*) FROM import_record_map WHERE object = 'lead' AND external_id = 'hs-lead-1'`); n != 1 {
		t.Errorf("identity rows = %d, want 1", n)
	}
}

func TestAFailedIdentityWriteLeavesNoLeadBehind(t *testing.T) {
	f := setupLanding(t)

	if _, err := f.brokenRun().Ensure(f.ctx, flipObjectLead, landingRow("hs-lead-2", map[string]any{"full_name": "Betty Holberton", "email": "betty@holberton.test"})); err == nil {
		t.Fatal("the landing succeeded against a run this workspace does not hold")
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
	operator := ids.From[ids.UserKind](f.e.Rep1)
	second := newFlipWriters(f.e.DB(), nil, "hubspot").forRun(f.w.runID, &operator)

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
