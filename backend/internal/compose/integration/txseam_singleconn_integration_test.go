// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The C5 shared-tx seams, run against a pool that has exactly ONE connection.
//
// A seam that accepts a caller's transaction and then reaches for the pool is
// wrong twice over, and neither failure shows up on a developer's machine: the
// second connection commits separately, so the "both writes or neither" claim
// the shape exists for is false; and it blocks against any lock the caller's
// transaction already holds, which is a deadlock Postgres cannot break because
// it sees two unrelated sessions rather than one goroutine waiting on itself.
// A sixteen-connection pool hides both — the second acquire simply succeeds.
//
// One connection is what makes the defect deterministic: the caller's
// transaction holds it, so a seam that acquires waits for a connection that
// cannot be returned until the seam it is inside of returns. These suites
// therefore carry a context deadline, and the deadline is the failure mode
// rather than the assertion — without it a regression hangs until the package
// timeout with nothing in the output naming the cause.
//
// The catalog is genuinely wired (the real customfields.Service, on the same
// single-connection pool), because an unwired catalog short-circuits to nil
// before it touches the pool: a suite that omitted it would pass over the very
// defect it exists to catch. txseamacquire_test.go is the static half — it
// judges the class across the tree in the diff; this is the half that proves
// the instance against a database, and that the columns the caller now fetches
// still ride the read and the write.

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/installseam"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// txSeamBudget bounds a seam that acquires: long enough that a loaded lane
// never trips it, short enough that the failure arrives as a test result
// rather than as a package timeout.
const txSeamBudget = 20 * time.Second

// txSeamPerms holds every object the three seams gate on, so a refusal in
// these suites is about connections rather than grants.
var txSeamPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field":          {Create: true, Read: true, Update: true, Delete: true},
		"person":                {Create: true, Read: true, Update: true, Delete: true},
		"organization":          {Create: true, Read: true, Update: true, Delete: true},
		"deal":                  {Create: true, Read: true, Update: true, Delete: true},
		"pipeline":              {Create: true, Read: true, Update: true, Delete: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeAll,
}

// txSeamFixture is one Env whose stores run on a single-connection pool with a
// real field catalog wired to that same pool.
type txSeamFixture struct {
	e      *Env
	pool   *pgxpool.Pool
	svc    *customfields.Service
	people *people.Store
	deals  *deals.Store
	ctx    context.Context
}

func setupTxSeam(t *testing.T) txSeamFixture {
	t.Helper()
	e := Setup(t)
	pool := singleConnPool(t)
	svc := customfields.NewService(pool, SchemaPool(t))
	ctx, cancel := context.WithTimeout(e.As(e.Rep1, nil, txSeamPerms), txSeamBudget)
	t.Cleanup(cancel)
	return txSeamFixture{
		e:      e,
		pool:   pool,
		svc:    svc,
		people: people.NewStore(harnessDB(pool, e.WS)).WithFieldCatalog(svc),
		deals:  deals.NewStore(harnessDB(pool, e.WS), installseam.Deals()).WithFieldCatalog(svc),
		ctx:    ctx,
	}
}

// singleConnPool opens the product's own pool constructor against the app DSN
// with its size pinned to one. The size rides the DSN because that is the knob
// database.NewPool honours from a caller (it fills only what the DSN leaves
// unset), so this stays the shipped pool with one named difference.
func singleConnPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing the app DSN: %v", err)
	}
	q := u.Query()
	q.Set("pool_max_conns", "1")
	q.Set("pool_min_conns", "1")
	u.RawQuery = q.Encode()

	pool, err := database.NewPool(context.Background(), u.String())
	if err != nil {
		t.Fatalf("opening the single-connection pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// defineTxSeamField creates one active custom field and answers its physical
// column name.
func (f txSeamFixture) defineTxSeamField(t *testing.T, object, label string) string {
	t.Helper()
	field, err := f.svc.Create(f.ctx, customfields.FieldSpec{
		Object: object, Label: label, Type: customfields.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the %s field %q: %v", object, label, err)
	}
	if field.ColumnName == nil {
		t.Fatalf("the defined %s field %q carries no column_name", object, label)
	}
	return *field.ColumnName
}

// requireCatalogAnswers fails the suite when the workspace has no active
// custom column for the object. Without one the seam below runs its core-only
// path, which acquires nothing whatever the code does — a green suite that
// proved nothing.
func (f txSeamFixture) requireCatalogAnswers(t *testing.T, object string) {
	t.Helper()
	cols, err := f.svc.ActiveColumns(f.ctx, object)
	if err != nil {
		t.Fatalf("reading the %s catalog: %v", object, err)
	}
	if len(cols) == 0 {
		t.Fatalf("the catalog answers no active %s column, so this suite would pass without exercising the catalog read it exists for", object)
	}
}

func TestGetPersonTxRunsOnTheCallersOnlyConnection(t *testing.T) {
	f := setupTxSeam(t)
	col := f.defineTxSeamField(t, "person", "Tier")

	created, err := f.people.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Ada Lovelace", Source: "ui",
		CustomFields: map[string]any{col: "gold"},
	})
	if err != nil {
		t.Fatalf("creating the person: %v", err)
	}

	active, err := f.people.ActivePersonColumns(f.ctx)
	if err != nil {
		t.Fatalf("reading the person's active custom columns: %v", err)
	}
	f.requireCatalogAnswers(t, "person")

	var got map[string]any
	if err := database.WithWorkspaceTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		person, err := f.people.GetPersonTx(f.ctx, tx, ids.From[ids.PersonKind](ids.UUID(created.Id)), storekit.LiveOnly, active)
		if err != nil {
			return err
		}
		got = person.AdditionalProperties
		return nil
	}); err != nil {
		t.Fatalf("reading the person inside the caller's transaction: %v — a timeout here is the seam waiting for a second connection the caller's transaction holds", err)
	}
	if got[col] != "gold" {
		t.Errorf("the custom field the caller fetched did not ride the read: %s = %v, want \"gold\"", col, got[col])
	}
}

func TestGetOrganizationTxRunsOnTheCallersOnlyConnection(t *testing.T) {
	f := setupTxSeam(t)
	col := f.defineTxSeamField(t, "organization", "Segment")

	created, err := f.people.CreateOrganization(f.ctx, people.CreateOrganizationInput{
		DisplayName: "Analytical Engines", Source: "ui",
		CustomFields: map[string]any{col: "enterprise"},
	})
	if err != nil {
		t.Fatalf("creating the organization: %v", err)
	}

	active, err := f.people.ActiveOrganizationColumns(f.ctx)
	if err != nil {
		t.Fatalf("reading the organization's active custom columns: %v", err)
	}
	f.requireCatalogAnswers(t, "organization")

	var got map[string]any
	if err := database.WithWorkspaceTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		org, err := f.people.GetOrganizationTx(f.ctx, tx, ids.From[ids.OrganizationKind](ids.UUID(created.Id)), storekit.LiveOnly, active)
		if err != nil {
			return err
		}
		got = org.AdditionalProperties
		return nil
	}); err != nil {
		t.Fatalf("reading the organization inside the caller's transaction: %v — a timeout here is the seam waiting for a second connection the caller's transaction holds", err)
	}
	if got[col] != "enterprise" {
		t.Errorf("the custom field the caller fetched did not ride the read: %s = %v, want \"enterprise\"", col, got[col])
	}
}

func TestUpdateDealTxRunsOnTheCallersOnlyConnection(t *testing.T) {
	f := setupTxSeam(t)
	pipeline, stage, _ := DealFixture(t, f.e)
	col := f.defineTxSeamField(t, "deal", "Renewal Risk")

	created, err := f.deals.CreateDeal(f.ctx, deals.CreateDealInput{
		Name: "Difference Engine", PipelineID: pipeline, StageID: stage, Source: "ui",
	})
	if err != nil {
		t.Fatalf("creating the deal: %v", err)
	}

	active, err := f.deals.ActiveDealColumns(f.ctx)
	if err != nil {
		t.Fatalf("reading the deal's active custom columns: %v", err)
	}
	f.requireCatalogAnswers(t, "deal")

	var got map[string]any
	if err := database.WithWorkspaceTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		updated, err := f.deals.UpdateDealTx(f.ctx, tx, ids.From[ids.DealKind](ids.UUID(created.Id)), deals.UpdateDealInput{
			CustomFields: map[string]any{col: "high"},
		}, active)
		if err != nil {
			return err
		}
		got = updated.AdditionalProperties
		return nil
	}); err != nil {
		t.Fatalf("updating the deal inside the caller's transaction: %v — a timeout here is the seam waiting for a second connection the caller's transaction holds", err)
	}
	if got[col] != "high" {
		t.Errorf("the custom field the caller fetched did not ride the write: %s = %v, want \"high\"", col, got[col])
	}
}
