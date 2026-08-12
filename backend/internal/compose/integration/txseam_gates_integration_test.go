// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A caller-opened create admits exactly what its store-opened twin admits, and
// refuses before it writes.
//
// Each Create*Tx seam shares a body with the store-opened entry point beside it
// and claims to run the same gates in the same order. Two things can go wrong
// with that and neither shows up in the suites either path already had: a seam
// that admitted what its twin refuses is an ungoverned door, and one that
// refused what its twin admits breaks the flip on estate data nobody predicted.
// So every arm below drives BOTH entry points with the same input and asserts
// the same answer.
//
// One asymmetry is deliberate and is not tested here as a difference: a
// caller-opened create refuses custom-field values, because the catalog they
// are matched against cannot be read from inside the caller's transaction.
// That refusal has its own arms in txseam_singleconn.
//
// The row count runs INSIDE the transaction the test opened, before the
// rollback. That ordering is the point: a seam that inserted the row and then
// refused would have its write rolled back by the wrapper, so counting
// afterwards cannot tell an early gate from a late one — every arm would pass
// either way.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/installseam"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// gateFixture is one Env with both stores and two seats: one holding every
// create grant, one holding none of them.
type gateFixture struct {
	e        *Env
	people   *people.Store
	deals    *deals.Store
	granted  context.Context
	ungated  context.Context
	pipeline ids.PipelineID
	stage    ids.StageID
}

func setupGates(t *testing.T) gateFixture {
	t.Helper()
	e := Setup(t)
	pipeline, stage, _ := DealFixture(t, e)
	return gateFixture{
		e:        e,
		people:   people.NewStore(e.DB()),
		deals:    deals.NewStore(e.DB(), installseam.Deals()),
		granted:  e.As(e.Rep1, nil, txSeamPerms),
		ungated:  e.As(e.Rep2, nil, principal.Permissions{RoleKeys: []string{"rep"}, RowScope: principal.RowScopeAll}),
		pipeline: pipeline,
		stage:    stage,
	}
}

// refused is what one caller-opened create answered, beside what its table held
// at that moment. Not an (error, int) pair: the error here is the ANSWER under
// test, not a failure to propagate.
type refused struct {
	err  error
	rows int
}

// refusedInTx runs one caller-opened create inside a transaction the TEST
// opens, and reads the row count on that same transaction — so a row the seam
// wrote before refusing is still visible. The transaction always rolls back.
func (f gateFixture) refusedInTx(ctx context.Context, t *testing.T, table string, create func(tx pgx.Tx) error) refused {
	t.Helper()
	out := refused{rows: -1}
	rollback := errors.New("rolling the probe back")
	err := database.WithWorkspaceTx(ctx, f.e.Pool, func(tx pgx.Tx) error {
		out.err = create(tx)
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&out.rows); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("the probe transaction ended with %v, want its own rollback", err)
	}
	return out
}

// assertSameRefusal pins the parity both entry points owe: neither accepts the
// input, and both say the same thing about it.
func assertSameRefusal(t *testing.T, storeOpened, callerOpened error) {
	t.Helper()
	if storeOpened == nil || callerOpened == nil {
		t.Fatalf("the input was accepted: store-opened err = %v, caller-opened err = %v", storeOpened, callerOpened)
	}
	if storeOpened.Error() != callerOpened.Error() {
		t.Errorf("the two entry points refuse differently:\n  store-opened:  %v\n  caller-opened: %v", storeOpened, callerOpened)
	}
}

func TestBothPersonCreatesRefuseTheSameThings(t *testing.T) {
	f := setupGates(t)
	valid := people.CreatePersonInput{FullName: "Ada Lovelace", Source: "ui"}

	if _, err := f.people.CreatePerson(f.ungated, valid); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the store-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	probe := f.refusedInTx(f.ungated, t, "person", func(tx pgx.Tx) error {
		_, err := f.people.CreatePersonTx(f.ungated, tx, valid)
		return err
	})
	if !errors.Is(probe.err, apperrors.ErrPermissionDenied) {
		t.Errorf("the caller-opened create answered %v for a seat without the grant, want the refusal", probe.err)
	}
	if probe.rows != 0 {
		t.Errorf("person rows inside the refusing transaction = %d, want 0 — the gate ran after the write", probe.rows)
	}

	// A contact that does not parse: the validation both settle before any
	// transaction opens.
	malformed := people.CreatePersonInput{
		FullName: "Ada Lovelace", Source: "ui",
		Emails: []people.PersonEmailInput{{Email: "not-an-address", EmailType: "work", IsPrimary: true}},
	}
	_, storeOpened := f.people.CreatePerson(f.granted, malformed)
	probe = f.refusedInTx(f.granted, t, "person", func(tx pgx.Tx) error {
		_, err := f.people.CreatePersonTx(f.granted, tx, malformed)
		return err
	})
	assertSameRefusal(t, storeOpened, probe.err)
	if probe.rows != 0 {
		t.Errorf("person rows inside the refusing transaction = %d, want 0 — the validation ran after the write", probe.rows)
	}
}

func TestBothOrganizationCreatesRefuseTheSameThings(t *testing.T) {
	f := setupGates(t)
	valid := people.CreateOrganizationInput{DisplayName: "Analytical Engines", Source: "ui"}

	if _, err := f.people.CreateOrganization(f.ungated, valid); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the store-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	probe := f.refusedInTx(f.ungated, t, "organization", func(tx pgx.Tx) error {
		_, err := f.people.CreateOrganizationTx(f.ungated, tx, valid)
		return err
	})
	if !errors.Is(probe.err, apperrors.ErrPermissionDenied) {
		t.Errorf("the caller-opened create answered %v for a seat without the grant, want the refusal", probe.err)
	}
	if probe.rows != 0 {
		t.Errorf("organization rows inside the refusing transaction = %d, want 0 — the gate ran after the write", probe.rows)
	}

	// A size band outside the vocabulary: refused on create, not only on the
	// patch, so the database never has to answer for it.
	band := "enormous"
	bad := people.CreateOrganizationInput{DisplayName: "Analytical Engines", Source: "ui", SizeBand: &band}
	_, storeOpened := f.people.CreateOrganization(f.granted, bad)
	probe = f.refusedInTx(f.granted, t, "organization", func(tx pgx.Tx) error {
		_, err := f.people.CreateOrganizationTx(f.granted, tx, bad)
		return err
	})
	assertSameRefusal(t, storeOpened, probe.err)
	if probe.rows != 0 {
		t.Errorf("organization rows inside the refusing transaction = %d, want 0 — the validation ran after the write", probe.rows)
	}
}

func TestBothLeadCreatesRefuseTheSameThings(t *testing.T) {
	f := setupGates(t)
	email := "jean@bartik.test"
	valid := people.CreateLeadInput{Email: &email, Status: "new", Source: "ui"}

	if _, _, err := f.people.CreateLead(f.ungated, valid); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the store-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	probe := f.refusedInTx(f.ungated, t, "lead", func(tx pgx.Tx) error {
		_, _, err := f.people.CreateLeadTx(f.ungated, tx, valid)
		return err
	})
	if !errors.Is(probe.err, apperrors.ErrPermissionDenied) {
		t.Errorf("the caller-opened create answered %v for a seat without the grant, want the refusal", probe.err)
	}
	if probe.rows != 0 {
		t.Errorf("lead rows inside the refusing transaction = %d, want 0 — the gate ran after the write", probe.rows)
	}

	// A status outside the writable vocabulary — the normalization both
	// entry points settle before any transaction opens.
	bad := people.CreateLeadInput{Email: &email, Status: "promoted", Source: "ui"}
	_, _, storeOpened := f.people.CreateLead(f.granted, bad)
	probe = f.refusedInTx(f.granted, t, "lead", func(tx pgx.Tx) error {
		_, _, err := f.people.CreateLeadTx(f.granted, tx, bad)
		return err
	})
	assertSameRefusal(t, storeOpened, probe.err)
	if probe.rows != 0 {
		t.Errorf("lead rows inside the refusing transaction = %d, want 0 — the validation ran after the write", probe.rows)
	}
}

func TestBothDealCreatesRefuseTheSameThings(t *testing.T) {
	f := setupGates(t)
	valid := deals.CreateDealInput{Name: "Difference Engine", PipelineID: f.pipeline, StageID: f.stage, Source: "ui"}

	if _, err := f.deals.CreateDeal(f.ungated, valid); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the store-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	probe := f.refusedInTx(f.ungated, t, "deal", func(tx pgx.Tx) error {
		_, err := f.deals.CreateDealTx(f.ungated, tx, valid)
		return err
	})
	if !errors.Is(probe.err, apperrors.ErrPermissionDenied) {
		t.Errorf("the caller-opened create answered %v for a seat without the grant, want the refusal", probe.err)
	}
	if probe.rows != 0 {
		t.Errorf("deal rows inside the refusing transaction = %d, want 0 — the gate ran after the write", probe.rows)
	}

	// Half a money value. The pair is atomic from birth, or the FX freeze at
	// close has nothing to freeze.
	amount := int64(1000)
	half := deals.CreateDealInput{
		Name: "Difference Engine", PipelineID: f.pipeline, StageID: f.stage, Source: "ui",
		AmountMinor: &amount,
	}
	_, storeOpened := f.deals.CreateDeal(f.granted, half)
	probe = f.refusedInTx(f.granted, t, "deal", func(tx pgx.Tx) error {
		_, err := f.deals.CreateDealTx(f.granted, tx, half)
		return err
	})
	assertSameRefusal(t, storeOpened, probe.err)
	// Both name the same fault, and it is the money pair rather than whatever
	// else could have refused this input.
	var pair *deals.AmountCurrencyPairError
	if !errors.As(probe.err, &pair) {
		t.Errorf("the caller-opened create refused with %v, want the money-pair fault", probe.err)
	}
	if probe.rows != 0 {
		t.Errorf("deal rows inside the refusing transaction = %d, want 0 — the validation ran after the write", probe.rows)
	}
}
