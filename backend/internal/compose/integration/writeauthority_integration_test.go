// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// record_grant.access, against a real database (#1373).
//
// The column has always carried two levels and the schema has always said
// "write satisfies read". Nothing read it: the only consumer was the visibility
// arm, which counts every live grant by design, and every mutation gated on
// that arm — so a colleague handed a `read` share could edit, archive, merge
// and erase the record the sharing screen told them they could only open.
//
// These tests are written as pairs on purpose. A refusal alone proves nothing
// about the rule: a probe that refused every write would pass it. Each refusal
// is therefore followed by the same call under a `write` grant on the same row,
// so the only thing that moved is the column.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sharedPersonFixture is one person rep3 captured PRIVATELY — a person who is
// merely owned is readable by every seat with the grant, so capture privacy
// is what makes a share rep1's only path to it, and every outcome below is
// attributable to the grant rather than to the scope tier.
type sharedPersonFixture struct {
	env    *SearchEnv
	person ids.UUID
	owner  context.Context // rep3, who may share it
	holder context.Context // rep1, who holds whatever share is current
}

func seedSharedPerson(t *testing.T, name string) sharedPersonFixture {
	t.Helper()
	e := SetupSearch(t)
	person := e.SeedID(t,
		`INSERT INTO person (id, full_name, owner_id, visibility, source, captured_by)
		 VALUES ($1, $3, $2, 'owner', 'manual', 'human:x')`, e.Rep3, name)
	return sharedPersonFixture{
		env:    e,
		person: person,
		owner:  recordActor(e, e.Rep3, principal.RowScopeOwn, nil),
		holder: recordActor(e, e.Rep1, principal.RowScopeTeam, []ids.UUID{e.Team1}),
	}
}

// recordActor mints a human who may read, update and delete people at one row
// scope. Delete is included because the archive and erasure arms need it, and
// leaving it out would make those refusals ambiguous — an object-grant miss and
// a row-authority miss are both refusals, and only one of them is under test.
func recordActor(e *SearchEnv, user ids.UUID, scope principal.RowScope, teams []ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs: teams,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"person": {Read: true, Update: true, Delete: true}},
			RowScope: scope,
		},
	})
}

// share writes the grant through the REAL writer. A hand-inserted row would
// prove the rule against a state production cannot reach, and this one in
// particular has an upsert behind it: re-asserting a share at a new level is
// the path an installation actually takes from `read` to `write`.
func (f sharedPersonFixture) share(t *testing.T, access string) {
	t.Helper()
	svc := identity.NewService(f.env.Pool)
	if _, err := svc.CreateRecordGrant(f.owner, identity.CreateGrantInput{
		RecordType: "person", RecordID: f.person,
		SubjectType: "user", SubjectID: f.env.Rep1, Access: access,
	}); err != nil {
		t.Fatalf("owner shares %s → %v", access, err)
	}
}

func TestAReadShareOpensAPersonButCannotEditIt(t *testing.T) {
	f := seedSharedPerson(t, "Read Share Subject")
	store := people.NewStore(f.env.DB())
	id := PersonIDOf(f.person)
	rename := func(to string) error {
		_, err := store.UpdatePerson(f.holder, id, people.UpdatePersonInput{FullName: &to, Source: "manual"})
		return err
	}

	// Nothing shared yet: the record is not there as far as rep1 is concerned,
	// and this is the arm that must not change. Existence-hiding comes FIRST —
	// a write-authority probe that answered 403 here would tell a stranger the
	// row exists, trading one disclosure for another.
	if err := rename("Guessed"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("editing an unshared person → %v, want not-found (existence-hiding)", err)
	}

	f.share(t, "read")

	// The share widens the READ, which is the feature and must survive.
	if _, err := store.GetPerson(f.holder, id, storekit.LiveOnly); err != nil {
		t.Fatalf("a read share does not open the record: %v", err)
	}
	// …and stops there. Permission-denied rather than not-found, because the
	// caller has just been shown the row: a 404 now would send them hunting
	// for a typo in an id they can read back from their own screen.
	if err := rename("Rewritten By A Reader"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("editing under a read share → %v, want permission-denied", err)
	}
	// Not merely refused — unchanged. A 403 raised after the update ran is
	// indistinguishable from one raised instead of it.
	if got := f.fullName(t); got != "Read Share Subject" {
		t.Fatalf("the person reads %q after a refused edit, want it untouched", got)
	}

	// The allow arm, on the same row and the same caller: only the column moved.
	f.share(t, "write")
	if err := rename("Rewritten By A Writer"); err != nil {
		t.Fatalf("editing under a write share → %v, want allowed", err)
	}
	if got := f.fullName(t); got != "Rewritten By A Writer" {
		t.Fatalf("the person reads %q after a permitted edit, want the new name", got)
	}
}

func TestAReadShareCannotArchiveOrEraseThePersonItOpens(t *testing.T) {
	f := seedSharedPerson(t, "Destructible Subject")
	store := people.NewStore(f.env.DB())
	eraser := privacy.NewEraser(f.env.DB())
	id := PersonIDOf(f.person)

	f.share(t, "read")

	// Archive is gated on person:delete, which this caller holds — so the only
	// thing that can refuse it is the row authority.
	if _, err := store.ArchivePerson(f.holder, id, nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("archiving under a read share → %v, want permission-denied", err)
	}
	// Erasure rides the subject-rights probe, which lifts capture privacy and
	// so needed its own write-authority twin rather than inheriting one.
	if err := eraser.ErasePerson(f.holder, f.person, "dsr"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("erasing under a read share → %v, want permission-denied", err)
	}
	if got := f.fullName(t); got != "Destructible Subject" {
		t.Fatalf("the person reads %q after two refused destructions, want it untouched", got)
	}

	f.share(t, "write")
	if _, err := store.ArchivePerson(f.holder, id, nil); err != nil {
		t.Fatalf("archiving under a write share → %v, want allowed", err)
	}
}

// The LIVE probe's twin, on a second record type. Several apply paths want more
// than visibility — the row must not be a tombstone either — and they were the
// ones most likely to be missed in the sweep, because their spelling already
// looked stricter than the plain probe. A manual scoring input is one of them,
// and it changes what the pipeline says a lead is worth.
func TestAReadShareOfALeadCannotScoreIt(t *testing.T) {
	e := SetupSearch(t)
	lead := e.SeedID(t,
		`INSERT INTO lead (id, full_name, owner_id, source, captured_by)
		 VALUES ($1, 'Shared Lead', $2, 'manual', 'human:x')`, e.Rep3)
	owner := leadActor(e, e.Rep3, principal.RowScopeOwn, nil)
	holder := leadActor(e, e.Rep1, principal.RowScopeTeam, []ids.UUID{e.Team1})
	store := people.NewStore(e.DB())

	score := func() error {
		_, err := store.SetLeadManualSignal(holder, ids.From[ids.LeadKind](lead),
			people.SetLeadManualSignalInput{
				Factor: "employees", Band: "51-200",
				SignalKind: "fact", Reason: "checked their careers page",
			})
		return err
	}
	shareLead := func(access string) {
		t.Helper()
		if _, err := identity.NewService(e.Pool).CreateRecordGrant(owner, identity.CreateGrantInput{
			RecordType: "lead", RecordID: lead,
			SubjectType: "user", SubjectID: e.Rep1, Access: access,
		}); err != nil {
			t.Fatalf("owner shares the lead as %s → %v", access, err)
		}
	}

	// Unshared first: a lead is readable by every seat holding the lead grant,
	// so the caller can already see the row and the refusal is the write
	// arm's — permission-denied, not a 404 that would send them hunting for
	// a typo in an id they can read back from their own screen.
	if err := score(); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("scoring an unshared lead → %v, want permission-denied (the row is readable, not writable)", err)
	}

	shareLead("read")
	if err := score(); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("scoring a lead under a read share → %v, want permission-denied", err)
	}
	shareLead("write")
	if err := score(); err != nil {
		t.Fatalf("scoring a lead under a write share → %v, want allowed", err)
	}
}

// leadActor mints a human who may read and update leads at one row scope.
func leadActor(e *SearchEnv, user ids.UUID, scope principal.RowScope, teams []ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs: teams,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"lead": {Read: true, Update: true}},
			RowScope: scope,
		},
	})
}

// A merge changes BOTH ends — the source is archived onto the survivor, the
// survivor absorbs its fields — so both ends carry the write-authority probe.
// The survivor's refusal is a bare conflict rather than a 403: naming it would
// disclose more than the caller could already read.
func TestAMergeNeedsWriteAuthorityOnBothEnds(t *testing.T) {
	f := seedSharedPerson(t, "Merge Source")
	e := f.env
	survivor := e.SeedID(t,
		`INSERT INTO person (id, full_name, owner_id, source, captured_by)
		 VALUES ($1, 'Merge Survivor', $2, 'manual', 'human:x')`, e.Rep3)
	store := people.NewStore(e.DB())

	// Read on the source, write on the survivor: the caller may change what the
	// record folds INTO and not the record itself. The source is archived and
	// its fields are rewritten onto the survivor, so a read share is not enough
	// there either — and this end answers not-found rather than conflict,
	// because the source is the record the CALLER named.
	f.share(t, "read")
	shareWith(f.owner, t, e, survivor, e.Rep1, "write")
	if _, err := store.MergePerson(f.holder, PersonIDOf(f.person), PersonIDOf(survivor)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("merging away a source held on a read share → %v, want permission-denied", err)
	}

	// Write on the source, nothing on the survivor: the caller may change what
	// they are folding away and not what it folds into.
	f.share(t, "write")
	revokeShare(t, e, survivor, e.Rep1)
	_, err := store.MergePerson(f.holder, PersonIDOf(f.person), PersonIDOf(survivor))
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("merging into a survivor the caller cannot change → %v, want conflict", err)
	}

	// Read on the survivor is not enough either — it is the arm the visibility
	// probe used to accept, and the one this change closes.
	shareWith(f.owner, t, e, survivor, e.Rep1, "read")
	if _, err := store.MergePerson(f.holder, PersonIDOf(f.person), PersonIDOf(survivor)); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("merging into a survivor held on a read share → %v, want conflict", err)
	}

	// Write on both: the merge goes through, so the two refusals above are the
	// rule and not a merge that never worked.
	shareWith(f.owner, t, e, survivor, e.Rep1, "write")
	if _, err := store.MergePerson(f.holder, PersonIDOf(f.person), PersonIDOf(survivor)); err != nil {
		t.Fatalf("merging with write on both ends → %v, want allowed", err)
	}
}

// revokeShare removes a grant so a later arm starts from "nothing shared". It
// goes through SQL rather than the revoke endpoint because what it is setting
// up is the absence of a grant, not the revocation path — which has its own
// suite in grants_integration_test.go.
func revokeShare(t *testing.T, e *SearchEnv, record, subject ids.UUID) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`DELETE FROM record_grant WHERE record_id = $1 AND subject_id = $2`, record, subject)
		return err
	}); err != nil {
		t.Fatalf("revoking a share: %v", err)
	}
}

func shareWith(owner context.Context, t *testing.T, e *SearchEnv, record, subject ids.UUID, access string) {
	t.Helper()
	if _, err := identity.NewService(e.Pool).CreateRecordGrant(owner, identity.CreateGrantInput{
		RecordType: "person", RecordID: record,
		SubjectType: "user", SubjectID: subject, Access: access,
	}); err != nil {
		t.Fatalf("sharing %s → %v", access, err)
	}
}

// An expired write grant is not a write grant. The visibility arm has always
// checked expiry; the write arm has to check it too, or a share that lapsed
// would keep conferring the wider half of what it once granted.
func TestAnExpiredWriteShareConfersNothing(t *testing.T) {
	f := seedSharedPerson(t, "Lapsed Share Subject")
	store := people.NewStore(f.env.DB())
	f.share(t, "write")

	if err := database.WithWorkspaceTx(f.env.Admin(), f.env.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE record_grant SET expires_at = now() - interval '1 hour' WHERE record_id = $1`, f.person)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	name := "After The Lapse"
	_, err := store.UpdatePerson(f.holder, PersonIDOf(f.person), people.UpdatePersonInput{FullName: &name, Source: "manual"})
	// Not-found, not permission-denied: an expired grant leaves the caller
	// unable to SEE the row at all, so existence-hiding is the right answer
	// again and the write arm never gets a question to answer.
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("editing under an expired write share → %v, want not-found", err)
	}
}

// An offer has no owner of its own — it inherits its deal's row scope — so a
// read share of the DEAL used to carry through to every offer edit hanging off
// it. This is the arm the person suite above cannot reach: the probe there is
// not on the record being written but on the record it belongs to.
func TestAReadShareOfADealCannotEditItsOffer(t *testing.T) {
	e := SetupSearch(t)
	pipeline := e.SeedID(t, `INSERT INTO pipeline (id, name, is_default, position)
		VALUES ($1, 'Sales', true, 0)`)
	stage := e.SeedID(t, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, 'Qualify', 0, 'open', 10)`, pipeline)
	deal := e.SeedID(t,
		`INSERT INTO deal (id, owner_id, name, pipeline_id, stage_id, source, captured_by)
		 VALUES ($1, $2, 'Shared Deal', $3, $4, 'manual', 'human:x')`, e.Rep3, pipeline, stage)
	offer := ids.NewV7()
	if _, err := e.Owner.Exec(context.Background(),
		`INSERT INTO offer (id, deal_id, offer_number, currency, source, captured_by)
		 VALUES ($1, $2, $3, 'EUR', 'manual', 'human:fixture')`,
		offer, deal, "AN-"+offer.String()); err != nil {
		t.Fatalf("seeding the offer: %v", err)
	}

	owner := dealActor(e, e.Rep3, principal.RowScopeOwn, nil)
	holder := dealActor(e, e.Rep1, principal.RowScopeTeam, []ids.UUID{e.Team1})
	store := deals.NewStore(e.DB(), deals.Installation{})
	intro := "Rewritten by a reader"
	editOffer := func() error {
		_, err := store.UpdateOffer(holder, ids.From[ids.OfferKind](offer), deals.UpdateOfferInput{IntroText: &intro})
		return err
	}
	shareDeal := func(access string) {
		t.Helper()
		if _, err := identity.NewService(e.Pool).CreateRecordGrant(owner, identity.CreateGrantInput{
			RecordType: "deal", RecordID: deal,
			SubjectType: "user", SubjectID: e.Rep1, Access: access,
		}); err != nil {
			t.Fatalf("owner shares the deal as %s → %v", access, err)
		}
	}

	shareDeal("read")
	// The deal itself first: the direct mutation, refused.
	name := "Renamed by a reader"
	if _, err := store.UpdateDeal(holder, ids.From[ids.DealKind](deal), deals.UpdateDealInput{Name: &name}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("editing a deal under a read share → %v, want permission-denied", err)
	}
	// Then the row that inherits its authority.
	if err := editOffer(); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("editing an offer under a read share of its deal → %v, want permission-denied", err)
	}

	shareDeal("write")
	if err := editOffer(); err != nil {
		t.Fatalf("editing an offer under a write share of its deal → %v, want allowed", err)
	}
}

// dealActor mints a human who may read and update deals and offers at one row
// scope — the grants an offer edit needs, and nothing else, so a refusal can
// only come from the rule under test.
func dealActor(e *SearchEnv, user ids.UUID, scope principal.RowScope, teams []ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs: teams,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"deal":  {Read: true, Update: true},
				"offer": {Read: true, Update: true},
			},
			RowScope: scope,
		},
	})
}

func (f sharedPersonFixture) fullName(t *testing.T) string {
	t.Helper()
	var name string
	if err := database.WithWorkspaceTx(f.env.Admin(), f.env.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT full_name FROM person WHERE id = $1`, f.person).Scan(&name)
	}); err != nil {
		t.Fatalf("reading the person back: %v", err)
	}
	return name
}
