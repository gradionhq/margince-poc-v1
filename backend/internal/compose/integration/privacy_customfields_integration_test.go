// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The GDPR engines must reach workspace-defined cf_ columns like core
// columns: Art. 17 erasure scrubs every custom column on the subject row
// (and the segregated lead twin), Art. 15 SAR exports every stored custom
// value. Both suites derive the column set from the custom_field catalog
// with ANY status — retiring a field preserves the physical column and
// every value in it, so an active-only pass would leave (or withhold)
// PII the workspace still holds.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// eur is the ISO-4217 pointer FieldSpec.Currency wants.
var eur = "EUR"

// definePersonFieldPerType creates one active custom field of every
// closed type on person, returning the physical column names.
func definePersonFieldPerType(t *testing.T, f cfvFixture) []string {
	t.Helper()
	specs := []customfields.FieldSpec{
		{Object: "person", Label: "Secret Note", Type: customfields.TypeText, Source: "ui"},
		{Object: "person", Label: "Risk Score", Type: customfields.TypeNumber, Source: "ui"},
		{Object: "person", Label: "Birthday", Type: customfields.TypeDate, Source: "ui"},
		{Object: "person", Label: "Net Worth", Type: customfields.TypeCurrency, Currency: &eur, Source: "ui"},
		{Object: "person", Label: "Tier Band", Type: customfields.TypePicklist, Options: []string{"gold", "silver"}, Source: "ui"},
		{Object: "person", Label: "Is Vip", Type: customfields.TypeBoolean, Source: "ui"},
	}
	cols := make([]string, len(specs))
	for i, spec := range specs {
		cols[i] = f.defineField(t, spec)
	}
	return cols
}

// writeSubjectCustomValues stores one value of every type on the subject
// row, raw SQL on purpose: the suite proves the privacy engines against
// what the DATABASE holds, independent of the record surface's write path.
func writeSubjectCustomValues(t *testing.T, e *Env, personID ids.UUID, cols []string) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), fmt.Sprintf(
			`UPDATE person SET %s = 'route-66-secret', %s = 42.5, %s = '2026-01-02',
			   %s = 199900, %s = 'gold', %s = true WHERE id = $1`,
			quoted(cols[0]), quoted(cols[1]), quoted(cols[2]),
			quoted(cols[3]), quoted(cols[4]), quoted(cols[5]),
		), personID)
		return err
	})
	if err != nil {
		t.Fatalf("writing subject custom values: %v", err)
	}
}

func quoted(column string) string { return pgx.Identifier{column}.Sanitize() }

// catalogColumns reads the catalog's column list for one object with ANY
// status — the same derivation the engines under test must use, so the
// assertions cannot drift from the columns that actually exist.
func catalogColumns(t *testing.T, e *Env, object string) []string {
	t.Helper()
	var cols []string
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT column_name FROM custom_field WHERE object = $1 ORDER BY column_name`, object)
		if err != nil {
			return err
		}
		cols, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatalf("reading the %s custom-field catalog: %v", object, err)
	}
	return cols
}

// countStoredCustomValues counts non-NULL cf_ values on one row of table.
func countStoredCustomValues(t *testing.T, e *Env, table string, rowID ids.UUID, cols []string) int {
	t.Helper()
	stored := 0
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		for _, col := range cols {
			var present bool
			if err := tx.QueryRow(context.Background(), fmt.Sprintf(
				`SELECT %s IS NOT NULL FROM %s WHERE id = $1`,
				quoted(col), quoted(table),
			), rowID).Scan(&present); err != nil {
				return fmt.Errorf("probing %s.%s: %w", table, col, err)
			}
			if present {
				stored++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestErasureScrubsCustomFieldColumns(t *testing.T) {
	f := setupCFV(t)
	personCols := definePersonFieldPerType(t, f)
	leadCol := f.defineField(t, customfields.FieldSpec{
		Object: "lead", Label: "Private Remark", Type: customfields.TypeText, Source: "ui",
	})

	personID := seedSubject(t, f.e)
	leadID := ids.NewV7()
	err := database.WithWorkspaceTx(f.e.Admin(), f.e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), fmt.Sprintf(
			`INSERT INTO lead (id, workspace_id, full_name, email, source, captured_by, %s)
			 VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			         'Selma Subject', $2, 'manual', 'human:x', 'about the subject')`,
			quoted(leadCol),
		), leadID, subjectEmail)
		return err
	})
	if err != nil {
		t.Fatalf("seeding the lead twin: %v", err)
	}
	writeSubjectCustomValues(t, f.e, personID, personCols)

	// A retired field's column keeps its stored value, so the scrub must
	// cover retired columns too — retire one AFTER its value landed.
	retiredField, err := f.svc.Create(f.ctx, customfields.FieldSpec{
		Object: "person", Label: "Legacy Code", Type: customfields.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the to-be-retired field: %v", err)
	}
	retiredCol := *retiredField.ColumnName
	err = database.WithWorkspaceTx(f.e.Admin(), f.e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), fmt.Sprintf(
			`UPDATE person SET %s = 'legacy-77' WHERE id = $1`, quoted(retiredCol),
		), personID)
		return err
	})
	if err != nil {
		t.Fatalf("storing the retired field's value: %v", err)
	}
	if _, err := f.svc.Retire(f.ctx, ids.UUID(retiredField.Id)); err != nil {
		t.Fatalf("retiring the field: %v", err)
	}

	allPersonCols := catalogColumns(t, f.e, "person")
	if len(allPersonCols) != len(personCols)+1 {
		t.Fatalf("person catalog carries %d columns, want %d", len(allPersonCols), len(personCols)+1)
	}
	if stored := countStoredCustomValues(t, f.e, "person", personID, allPersonCols); stored != len(allPersonCols) {
		t.Fatalf("fixture stored %d custom values, want %d — the scrub assertion would be vacuous", stored, len(allPersonCols))
	}

	if err := privacy.NewEraser(f.e.Pool).ErasePerson(f.e.Admin(), personID, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	if left := countStoredCustomValues(t, f.e, "person", personID, allPersonCols); left != 0 {
		t.Fatalf("%d custom-field values survived erasure on the person row", left)
	}
	if left := countStoredCustomValues(t, f.e, "lead", leadID, catalogColumns(t, f.e, "lead")); left != 0 {
		t.Fatalf("%d custom-field values survived erasure on the lead twin", left)
	}

	assertEraseTombstoneShape(t, f.e, personID)
}

// assertEraseTombstoneShape proves the scrub left the audit contract
// alone: one erase tombstone on the person carrying counts-only
// evidence, and no tombstone anywhere re-storing a scrubbed cf_ value.
func assertEraseTombstoneShape(t *testing.T, e *Env, personID ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		var tombstones int
		if err := tx.QueryRow(context.Background(), `
			SELECT count(*) FROM audit_log
			WHERE action = 'erase' AND entity_type = 'person' AND entity_id = $1
			  AND evidence->>'reason' = 'test'`, personID).Scan(&tombstones); err != nil {
			return err
		}
		if tombstones != 1 {
			return fmt.Errorf("erase tombstones on the person = %d, want 1", tombstones)
		}
		var leaked bool
		if err := tx.QueryRow(context.Background(), `
			SELECT EXISTS (SELECT 1 FROM audit_log
			WHERE action = 'erase'
			  AND (coalesce(before::text, '') || coalesce(after::text, '') || coalesce(evidence::text, ''))
			      ILIKE '%route-66-secret%')`).Scan(&leaked); err != nil {
			return err
		}
		if leaked {
			return fmt.Errorf("an erase tombstone re-stores a scrubbed custom-field value")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSARExportsCustomFieldValues(t *testing.T) {
	f := setupCFV(t)
	segmentCol := f.defineField(t, customfields.FieldSpec{
		Object: "person", Label: "Segment", Type: customfields.TypeText, Source: "ui",
	})
	volumeCol := f.defineField(t, customfields.FieldSpec{
		Object: "person", Label: "Annual Volume", Type: customfields.TypeNumber, Source: "ui",
	})
	legacyField, err := f.svc.Create(f.ctx, customfields.FieldSpec{
		Object: "person", Label: "Legacy Code", Type: customfields.TypeText, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the to-be-retired field: %v", err)
	}
	legacyCol := *legacyField.ColumnName

	personID := seedSubject(t, f.e)
	err = database.WithWorkspaceTx(f.e.Admin(), f.e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), fmt.Sprintf(
			`UPDATE person SET %s = 'vip', %s = 1234.5, %s = 'legacy-77' WHERE id = $1`,
			quoted(segmentCol), quoted(volumeCol), quoted(legacyCol),
		), personID)
		return err
	})
	if err != nil {
		t.Fatalf("storing the subject's custom values: %v", err)
	}
	if _, err := f.svc.Retire(f.ctx, ids.UUID(legacyField.Id)); err != nil {
		t.Fatalf("retiring the field: %v", err)
	}

	pkg, err := privacy.AssembleSAR(f.e.Admin(), f.e.Pool, ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}
	assertCF(t, pkg.Subject, segmentCol, "vip")
	assertCF(t, pkg.Subject, volumeCol, json.Number("1234.5"))
	// Art. 15 owes the subject everything HELD, not everything active: a
	// retired field's stored value is still held and must export.
	assertCF(t, pkg.Subject, legacyCol, "legacy-77")
}
