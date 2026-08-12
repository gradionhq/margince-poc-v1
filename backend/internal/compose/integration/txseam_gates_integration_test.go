// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A caller-opened create refuses exactly what the store-opened one refuses.
//
// Each Create*Tx seam claims to run "the same gates in the same order" as the
// entry point it shares a body with, and nothing tested that claim: the store's
// own suites drive the store-opened path, and the landing suites drive the
// caller-opened one with a seat that holds every grant and inputs that are
// always valid. Between them sat the half that matters — a seam that admitted
// what its twin refuses would be an ungoverned door, and one that refused what
// its twin admits would break the flip on estate data nobody could predict.
//
// So each arm asserts the pair: the same refusal, from both entry points, for
// the same input. Refusals BEFORE any row is written, which is why the
// transaction here is opened by the test rather than by the store — a gate that
// ran late would still refuse, and still have written the record first.

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

// inTx runs one caller-opened create and answers what it refused with. The
// transaction is deliberately the test's: a store that opened its own would
// prove nothing about a seam meant to run inside somebody else's.
func (f gateFixture) inTx(ctx context.Context, create func(tx pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, f.e.Pool, create)
}

func TestBothPersonCreatesRefuseTheSameThings(t *testing.T) {
	f := setupGates(t)
	valid := people.CreatePersonInput{FullName: "Ada Lovelace", Source: "ui"}

	// No person:create.
	if _, err := f.people.CreatePerson(f.ungated, valid); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the store-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	if err := f.inTx(f.ungated, func(tx pgx.Tx) error {
		_, err := f.people.CreatePersonTx(f.ungated, tx, valid)
		return err
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the caller-opened create answered %v for a seat without the grant, want the refusal", err)
	}

	// A contact that does not parse — the validation both settle before any
	// transaction opens.
	malformed := people.CreatePersonInput{
		FullName: "Ada Lovelace", Source: "ui",
		Emails: []people.PersonEmailInput{{Email: "not-an-address", EmailType: "work", IsPrimary: true}},
	}
	_, storeOpened := f.people.CreatePerson(f.granted, malformed)
	callerOpened := f.inTx(f.granted, func(tx pgx.Tx) error {
		_, err := f.people.CreatePersonTx(f.granted, tx, malformed)
		return err
	})
	if storeOpened == nil || callerOpened == nil {
		t.Fatalf("a malformed contact was accepted: store-opened err = %v, caller-opened err = %v", storeOpened, callerOpened)
	}
	if storeOpened.Error() != callerOpened.Error() {
		t.Errorf("the two entry points refuse differently:\n  store-opened:  %v\n  caller-opened: %v", storeOpened, callerOpened)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = 'Ada Lovelace'`); n != 0 {
		t.Errorf("person rows = %d, want 0 — a refusal that wrote the record first is not a gate", n)
	}
}

func TestBothOrganizationCreatesRefuseTheSameThings(t *testing.T) {
	f := setupGates(t)
	valid := people.CreateOrganizationInput{DisplayName: "Analytical Engines", Source: "ui"}

	if _, err := f.people.CreateOrganization(f.ungated, valid); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the store-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	if err := f.inTx(f.ungated, func(tx pgx.Tx) error {
		_, err := f.people.CreateOrganizationTx(f.ungated, tx, valid)
		return err
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the caller-opened create answered %v for a seat without the grant, want the refusal", err)
	}

	// A size band outside the vocabulary: refused on create, not only on the
	// patch, so the database never has to answer for it.
	band := "enormous"
	bad := people.CreateOrganizationInput{DisplayName: "Analytical Engines", Source: "ui", SizeBand: &band}
	_, storeOpened := f.people.CreateOrganization(f.granted, bad)
	callerOpened := f.inTx(f.granted, func(tx pgx.Tx) error {
		_, err := f.people.CreateOrganizationTx(f.granted, tx, bad)
		return err
	})
	if storeOpened == nil || callerOpened == nil {
		t.Fatalf("an unknown size band was accepted: store-opened err = %v, caller-opened err = %v", storeOpened, callerOpened)
	}
	if storeOpened.Error() != callerOpened.Error() {
		t.Errorf("the two entry points refuse differently:\n  store-opened:  %v\n  caller-opened: %v", storeOpened, callerOpened)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM organization WHERE display_name = 'Analytical Engines'`); n != 0 {
		t.Errorf("organization rows = %d, want 0 — a refusal that wrote the record first is not a gate", n)
	}
}

func TestBothLeadCreatesRefuseASeatWithoutTheGrant(t *testing.T) {
	f := setupGates(t)
	email := "jean@bartik.test"
	valid := people.CreateLeadInput{Email: &email, Status: "new", Source: "ui"}

	if _, _, err := f.people.CreateLead(f.ungated, valid); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the store-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	if err := f.inTx(f.ungated, func(tx pgx.Tx) error {
		_, _, err := f.people.CreateLeadTx(f.ungated, tx, valid)
		return err
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the caller-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM lead WHERE email = 'jean@bartik.test'`); n != 0 {
		t.Errorf("lead rows = %d, want 0", n)
	}
}

func TestBothDealCreatesRefuseTheSameThings(t *testing.T) {
	f := setupGates(t)
	valid := deals.CreateDealInput{Name: "Difference Engine", PipelineID: f.pipeline, StageID: f.stage, Source: "ui"}

	if _, err := f.deals.CreateDeal(f.ungated, valid); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the store-opened create answered %v for a seat without the grant, want the refusal", err)
	}
	if err := f.inTx(f.ungated, func(tx pgx.Tx) error {
		_, err := f.deals.CreateDealTx(f.ungated, tx, valid)
		return err
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("the caller-opened create answered %v for a seat without the grant, want the refusal", err)
	}

	// Half a money value. The pair is atomic from birth, or the FX freeze at
	// close has nothing to freeze.
	amount := int64(1000)
	half := deals.CreateDealInput{
		Name: "Difference Engine", PipelineID: f.pipeline, StageID: f.stage, Source: "ui",
		AmountMinor: &amount,
	}
	_, storeOpened := f.deals.CreateDeal(f.granted, half)
	callerOpened := f.inTx(f.granted, func(tx pgx.Tx) error {
		_, err := f.deals.CreateDealTx(f.granted, tx, half)
		return err
	})
	if storeOpened == nil || callerOpened == nil {
		t.Fatalf("an amount without its currency was accepted: store-opened err = %v, caller-opened err = %v", storeOpened, callerOpened)
	}
	if storeOpened.Error() != callerOpened.Error() {
		t.Errorf("the two entry points refuse differently:\n  store-opened:  %v\n  caller-opened: %v", storeOpened, callerOpened)
	}
	if n := f.e.WsCount(t, `SELECT count(*) FROM deal WHERE name = 'Difference Engine'`); n != 0 {
		t.Errorf("deal rows = %d, want 0 — a refusal that wrote the record first is not a gate", n)
	}
}
