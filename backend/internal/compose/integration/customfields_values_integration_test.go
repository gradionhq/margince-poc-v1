// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Custom-field VALUES riding person/organization records (CF-T05, arc
// 2a-ii T2): the fieldcatalog seam wired into the people store makes a
// workspace's active cf_* columns participate in create/update writes
// and get/list reads like core fields. Store-level suites prove the
// value semantics (six-type round trip, drop-on-mismatch, unknown-key
// drop, retired-field hiding, workspace isolation, the audit diff); the
// HTTP suite proves the wire flatten — cf_ keys ride the payload
// TOP-LEVEL through the generated types' additionalProperties — plus
// the picklist CHECK → 422 mapping over the real compose wiring.

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// cfvPerms is cfAdminPerms plus the organization grants this suite's
// org round trip needs.
var cfvPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": {Create: true, Read: true, Update: true, Delete: true},
		"person":       {Create: true, Read: true, Update: true, Delete: true},
		"organization": {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeAll,
}

// cfvFixture is the store-level fixture: one Env plus a catalog-wired
// people store and the schema-pool-backed customfields service that
// defines the fields the tests write into.
type cfvFixture struct {
	e     *Env
	svc   *customfields.Service
	store *people.Store
	ctx   context.Context
}

func setupCFV(t *testing.T) cfvFixture {
	t.Helper()
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	return cfvFixture{
		e:     e,
		svc:   svc,
		store: people.NewStore(e.Pool).WithFieldCatalog(svc),
		ctx:   e.As(e.Rep1, nil, cfvPerms),
	}
}

// defineField creates one active custom field and returns its physical
// column name.
func (f cfvFixture) defineField(t *testing.T, spec customfields.FieldSpec) string {
	t.Helper()
	field, err := f.svc.Create(f.ctx, spec)
	if err != nil {
		t.Fatalf("defining %s field %q: %v", spec.Type, spec.Label, err)
	}
	if field.ColumnName == nil {
		t.Fatalf("defined field %q carries no column_name", spec.Label)
	}
	return *field.ColumnName
}

//craft:ignore naked-any want is whichever wire shape the field's type round-trips (string/bool/int64/json.Number) — the assertion seam mirrors ExtractValues' output map
func assertCF(t *testing.T, got map[string]any, key string, want any) {
	t.Helper()
	v, ok := got[key]
	if !ok {
		t.Fatalf("custom field %q absent from payload %v", key, got)
	}
	if v != want {
		t.Fatalf("custom field %q = %v (%T), want %v (%T)", key, v, v, want, want)
	}
}

func assertNoCF(t *testing.T, got map[string]any, key string) {
	t.Helper()
	if v, ok := got[key]; ok {
		t.Fatalf("custom field %q = %v, want key absent", key, v)
	}
}

func TestCustomFieldValues_PersonRoundTrip(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Tier", Type: customfields.TypeText, Source: "ui"})

	created, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Ada Lovelace", Source: "ui",
		CustomFields: map[string]any{col: "gold"},
	})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}
	assertCF(t, created.AdditionalProperties, col, "gold")

	got, err := f.store.GetPerson(f.ctx, personIDOf(ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	assertCF(t, got.AdditionalProperties, col, "gold")

	updated, err := f.store.UpdatePerson(f.ctx, personIDOf(ids.UUID(created.Id)), people.UpdatePersonInput{
		CustomFields: map[string]any{col: "silver"},
	})
	if err != nil {
		t.Fatalf("UpdatePerson: %v", err)
	}
	assertCF(t, updated.AdditionalProperties, col, "silver")

	list, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{})
	if err != nil {
		t.Fatalf("ListPeople: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListPeople returned %d rows, want 1", len(list))
	}
	assertCF(t, list[0].AdditionalProperties, col, "silver")
}

func TestCustomFieldValues_OrganizationRoundTrip(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "organization", Label: "Region", Type: customfields.TypeText, Source: "ui"})

	created, err := f.store.CreateOrganization(f.ctx, people.CreateOrganizationInput{
		DisplayName: "Acme GmbH", Source: "ui",
		CustomFields: map[string]any{col: "emea"},
	})
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	assertCF(t, created.AdditionalProperties, col, "emea")

	got, err := f.store.GetOrganization(f.ctx, orgIDOf(ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetOrganization: %v", err)
	}
	assertCF(t, got.AdditionalProperties, col, "emea")

	updated, err := f.store.UpdateOrganization(f.ctx, orgIDOf(ids.UUID(created.Id)), people.UpdateOrganizationInput{
		CustomFields: map[string]any{col: "apac"},
	})
	if err != nil {
		t.Fatalf("UpdateOrganization: %v", err)
	}
	assertCF(t, updated.AdditionalProperties, col, "apac")

	list, _, err := f.store.ListOrganizations(f.ctx, people.ListOrganizationsInput{})
	if err != nil {
		t.Fatalf("ListOrganizations: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListOrganizations returned %d rows, want 1", len(list))
	}
	assertCF(t, list[0].AdditionalProperties, col, "apac")
}

// TestCustomFieldValues_AllSixTypesRoundTrip writes every field type in
// one create with the shape a JSON body decode hands the store (numbers
// arrive as float64 — the generated types' additionalProperties decode)
// and asserts each documented wire read shape: currency as int64 minor
// units, number as json.Number, date as "YYYY-MM-DD", boolean, text and
// picklist as themselves.
func TestCustomFieldValues_AllSixTypesRoundTrip(t *testing.T) {
	f := setupCFV(t)
	eur := "EUR"
	text := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Note", Type: customfields.TypeText, Source: "ui"})
	number := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})
	date := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Renewal", Type: customfields.TypeDate, Source: "ui"})
	currency := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Budget", Type: customfields.TypeCurrency, Currency: &eur, Source: "ui"})
	picklist := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Route", Type: customfields.TypePicklist, Options: []string{"direct", "partner"}, Source: "ui"})
	boolean := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Strategic", Type: customfields.TypeBoolean, Source: "ui"})

	created, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Grace Hopper", Source: "ui",
		CustomFields: map[string]any{
			text:     "prefers morning calls",
			number:   float64(42.5),
			date:     "2026-07-11",
			currency: float64(129900),
			picklist: "partner",
			boolean:  true,
		},
	})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}

	got, err := f.store.GetPerson(f.ctx, personIDOf(ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	assertCF(t, got.AdditionalProperties, text, "prefers morning calls")
	assertCF(t, got.AdditionalProperties, number, json.Number("42.5"))
	assertCF(t, got.AdditionalProperties, date, "2026-07-11")
	assertCF(t, got.AdditionalProperties, currency, int64(129900))
	assertCF(t, got.AdditionalProperties, picklist, "partner")
	assertCF(t, got.AdditionalProperties, boolean, true)
}

// TestCustomFieldValues_WrongShapeDropped: additionalProperties carries
// no per-key schema, so a value whose shape does not match its column's
// type is dropped — the write succeeds, the key never lands (and an
// update's drop leaves the stored value standing).
func TestCustomFieldValues_WrongShapeDropped(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})

	created, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Mismatch On Create", Source: "ui",
		CustomFields: map[string]any{col: true},
	})
	if err != nil {
		t.Fatalf("CreatePerson with mismatched value shape: %v", err)
	}
	assertNoCF(t, created.AdditionalProperties, col)

	if _, err := f.store.UpdatePerson(f.ctx, personIDOf(ids.UUID(created.Id)), people.UpdatePersonInput{
		CustomFields: map[string]any{col: float64(7)},
	}); err != nil {
		t.Fatalf("UpdatePerson (valid shape): %v", err)
	}
	if _, err := f.store.UpdatePerson(f.ctx, personIDOf(ids.UUID(created.Id)), people.UpdatePersonInput{
		CustomFields: map[string]any{col: "not-a-number"},
	}); err != nil {
		t.Fatalf("UpdatePerson with mismatched value shape: %v", err)
	}
	got, err := f.store.GetPerson(f.ctx, personIDOf(ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	assertCF(t, got.AdditionalProperties, col, json.Number("7"))
}

// TestCustomFieldValues_WrongShapeDroppedAcrossTypes: WrongShapeDropped
// proves the drop only for a number-typed mismatch; SQLValue's shape
// check runs once per column type (currency wants float64, date wants
// string, boolean wants bool), so each type's own mismatch needs its
// own proof that the write still succeeds with the key simply absent.
func TestCustomFieldValues_WrongShapeDroppedAcrossTypes(t *testing.T) {
	f := setupCFV(t)
	eur := "EUR"
	currency := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Budget", Type: customfields.TypeCurrency, Currency: &eur, Source: "ui"})
	date := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Renewal", Type: customfields.TypeDate, Source: "ui"})
	boolean := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Strategic", Type: customfields.TypeBoolean, Source: "ui"})

	cases := map[string]struct {
		col   string
		wrong any
	}{
		"currency takes a float64, not a string": {currency, "not-an-amount"},
		"date takes a string, not a bool":        {date, true},
		"boolean takes a bool, not a string":     {boolean, "yes"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			created, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
				FullName: "Mismatch", Source: "ui",
				CustomFields: map[string]any{tc.col: tc.wrong},
			})
			if err != nil {
				t.Fatalf("CreatePerson: %v", err)
			}
			assertNoCF(t, created.AdditionalProperties, tc.col)
		})
	}
}

// TestCustomFieldValues_NumberAcceptsJSONNumberAndDecimalString:
// sqlNumber's doc comment promises three input shapes bind a number
// column: the float64 every other suite writes through, plus the
// json.Number/plain-decimal-string a UseNumber-decoding caller could
// hand it — this proves those other two shapes actually round-trip
// (and that a string that fails to parse as a number still drops,
// same posture as every other type mismatch).
func TestCustomFieldValues_NumberAcceptsJSONNumberAndDecimalString(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})

	fromJSONNumber, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Via json.Number", Source: "ui",
		CustomFields: map[string]any{col: json.Number("42.5")},
	})
	if err != nil {
		t.Fatalf("CreatePerson (json.Number): %v", err)
	}
	assertCF(t, fromJSONNumber.AdditionalProperties, col, json.Number("42.5"))

	fromString, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Via decimal string", Source: "ui",
		CustomFields: map[string]any{col: "7.25"},
	})
	if err != nil {
		t.Fatalf("CreatePerson (decimal string): %v", err)
	}
	assertCF(t, fromString.AdditionalProperties, col, json.Number("7.25"))

	unparseable, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Via unparseable string", Source: "ui",
		CustomFields: map[string]any{col: "not-a-number-string"},
	})
	if err != nil {
		t.Fatalf("CreatePerson (unparseable string): %v", err)
	}
	assertNoCF(t, unparseable.AdditionalProperties, col)
}

// TestCustomFieldValues_NumberNaNDropped: a number-typed value that
// stores as Postgres NUMERIC 'NaN' round-trips through pgtype.Numeric
// with Valid=true but NaN=true — extractNumber's documented "NaN has no
// JSON-number wire shape" rule drops it from the read, the same
// drop-on-mismatch posture every other unrepresentable value gets.
func TestCustomFieldValues_NumberNaNDropped(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})

	created, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "NaN Score", Source: "ui",
		CustomFields: map[string]any{col: math.NaN()},
	})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}
	assertNoCF(t, created.AdditionalProperties, col)
}

func TestCustomFieldValues_UnknownKeyDropped(t *testing.T) {
	f := setupCFV(t)

	created, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "No Such Column", Source: "ui",
		CustomFields: map[string]any{"cf_never_defined": "x"},
	})
	if err != nil {
		t.Fatalf("CreatePerson with unknown cf_ key: %v", err)
	}
	assertNoCF(t, created.AdditionalProperties, "cf_never_defined")
}

// TestCustomFieldValues_RetiredFieldHiddenButPreserved: retiring a field
// removes its column from ActiveColumns, so its key stops appearing in
// reads and stops being writable — hidden from the API — while the
// stored value survives physically (retire never drops the column;
// un-retire is a catalog re-activation away).
func TestCustomFieldValues_RetiredFieldHiddenButPreserved(t *testing.T) {
	f := setupCFV(t)
	field, err := f.svc.Create(f.ctx, customfields.FieldSpec{Object: "person", Label: "Legacy Tier", Type: customfields.TypeText, Source: "ui"})
	if err != nil {
		t.Fatalf("defining field: %v", err)
	}
	col := *field.ColumnName

	created, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Retire Me", Source: "ui",
		CustomFields: map[string]any{col: "gold"},
	})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}
	assertCF(t, created.AdditionalProperties, col, "gold")

	if _, err := f.svc.Retire(f.ctx, ids.UUID(field.Id)); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	got, err := f.store.GetPerson(f.ctx, personIDOf(ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetPerson after retire: %v", err)
	}
	assertNoCF(t, got.AdditionalProperties, col)

	// A write against the retired key is dropped like any unknown key.
	if _, err := f.store.UpdatePerson(f.ctx, personIDOf(ids.UUID(created.Id)), people.UpdatePersonInput{
		CustomFields: map[string]any{col: "silver"},
	}); err != nil {
		t.Fatalf("UpdatePerson against retired key: %v", err)
	}

	// The stored value is untouched underneath.
	var stored *string
	err = database.WithWorkspaceTx(f.ctx, f.e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT `+col+` FROM person WHERE id = $1`, ids.UUID(created.Id)).Scan(&stored)
	})
	if err != nil {
		t.Fatalf("reading retired column directly: %v", err)
	}
	if stored == nil || *stored != "gold" {
		t.Fatalf("retired column value = %v, want preserved \"gold\"", stored)
	}
}

// TestCustomFieldValues_WorkspaceIsolation: the physical cf_ column is
// shared across tenants, but the catalog is workspace-scoped — a
// workspace that never defined the field neither writes nor reads it.
func TestCustomFieldValues_WorkspaceIsolation(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Tier", Type: customfields.TypeText, Source: "ui"})

	inA, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Tenant A Person", Source: "ui",
		CustomFields: map[string]any{col: "gold"},
	})
	if err != nil {
		t.Fatalf("CreatePerson (tenant A): %v", err)
	}
	assertCF(t, inA.AdditionalProperties, col, "gold")

	_, ctxB := seedSecondWorkspace(t, OwnerConn(t))
	inB, err := f.store.CreatePerson(ctxB, people.CreatePersonInput{
		FullName: "Tenant B Person", Source: "ui",
		CustomFields: map[string]any{col: "gold"},
	})
	if err != nil {
		t.Fatalf("CreatePerson (tenant B): %v", err)
	}
	assertNoCF(t, inB.AdditionalProperties, col)

	// The dropped write really never landed: B's row holds NULL in the
	// shared physical column.
	var stored *string
	err = database.WithWorkspaceTx(ctxB, f.e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT `+col+` FROM person WHERE id = $1`, ids.UUID(inB.Id)).Scan(&stored)
	})
	if err != nil {
		t.Fatalf("reading tenant B's column directly: %v", err)
	}
	if stored != nil {
		t.Fatalf("tenant B's %s = %q, want NULL (write must be dropped)", col, *stored)
	}

	// Tenant A still reads its value.
	gotA, err := f.store.GetPerson(f.ctx, personIDOf(ids.UUID(inA.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetPerson (tenant A): %v", err)
	}
	assertCF(t, gotA.AdditionalProperties, col, "gold")
}

// TestCustomFieldValues_UpdateAuditCarriesDiff: cf_ updates ride the
// same storekit.Patch as core fields, so the audit row's before/after
// carries the change with no extra bookkeeping.
func TestCustomFieldValues_UpdateAuditCarriesDiff(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "person", Label: "Tier", Type: customfields.TypeText, Source: "ui"})

	created, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{
		FullName: "Audit Trail", Source: "ui",
		CustomFields: map[string]any{col: "gold"},
	})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}
	if _, err := f.store.UpdatePerson(f.ctx, personIDOf(ids.UUID(created.Id)), people.UpdatePersonInput{
		CustomFields: map[string]any{col: "silver"},
	}); err != nil {
		t.Fatalf("UpdatePerson: %v", err)
	}

	var before, after map[string]any
	err = database.WithWorkspaceTx(f.ctx, f.e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT before, after FROM audit_log
			 WHERE entity_type = 'person' AND entity_id = $1 AND action = 'update'
			 ORDER BY occurred_at DESC LIMIT 1`, ids.UUID(created.Id)).Scan(&before, &after)
	})
	if err != nil {
		t.Fatalf("reading update audit row: %v", err)
	}
	if got := before[col]; got != "gold" {
		t.Errorf("audit before[%s] = %v, want \"gold\"", col, got)
	}
	if got := after[col]; got != "silver" {
		t.Errorf("audit after[%s] = %v, want \"silver\"", col, got)
	}
}
