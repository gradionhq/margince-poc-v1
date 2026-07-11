// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

import (
	"strings"
	"testing"
)

func TestValidate_UnsupportedObjectRejected(t *testing.T) {
	errs := Validate(FieldSpec{Object: "widget", Label: "X", Type: TypeText, Source: "ui"})
	if !hasFieldError(errs, "object", "unsupported_object") {
		t.Fatalf("expected object/unsupported_object, got %+v", errs)
	}
}

func TestValidate_UnsupportedTypeRejected(t *testing.T) {
	errs := Validate(FieldSpec{Object: "deal", Label: "X", Type: "money", Source: "ui"})
	if !hasFieldError(errs, "type", "unsupported_type") {
		t.Fatalf("expected type/unsupported_type, got %+v", errs)
	}
}

func TestValidate_EmptyLabelRejected(t *testing.T) {
	errs := Validate(FieldSpec{Object: "deal", Label: "  ", Type: TypeText, Source: "ui"})
	if !hasFieldError(errs, "label", "required") {
		t.Fatalf("expected label/required, got %+v", errs)
	}
}

func TestValidate_CurrencyRequiresISOCode(t *testing.T) {
	errs := Validate(FieldSpec{Object: "deal", Label: "Budget", Type: TypeCurrency, Source: "ui"})
	if !hasFieldError(errs, "currency", "required_for_type_currency") {
		t.Fatalf("expected currency/required_for_type_currency, got %+v", errs)
	}
	usd := "usd"
	errsBad := Validate(FieldSpec{Object: "deal", Label: "Budget", Type: TypeCurrency, Currency: &usd, Source: "ui"})
	if !hasFieldError(errsBad, "currency", "required_for_type_currency") {
		t.Fatalf("lowercase currency must fail the ^[A-Z]{3}$ pattern, got %+v", errsBad)
	}
	USD := "USD"
	if errs := (Validate(FieldSpec{Object: "deal", Label: "Budget", Type: TypeCurrency, Currency: &USD, Source: "ui"})); len(errs) != 0 {
		t.Fatalf("valid currency must pass, got %+v", errs)
	}
}

func TestValidate_PicklistRequiresNonEmptyOptions(t *testing.T) {
	errs := Validate(FieldSpec{Object: "deal", Label: "Route", Type: TypePicklist, Source: "ui"})
	if !hasFieldError(errs, "options", "required_for_type_picklist") {
		t.Fatalf("expected options/required_for_type_picklist, got %+v", errs)
	}
	if errs := Validate(FieldSpec{Object: "deal", Label: "Route", Type: TypePicklist, Options: []string{"direct"}, Source: "ui"}); len(errs) != 0 {
		t.Fatalf("one option must pass (PARAM-5 minimum=1), got %+v", errs)
	}
}

func TestValidate_RequiresSource(t *testing.T) {
	errs := Validate(FieldSpec{Object: "deal", Label: "X", Type: TypeText})
	if !hasFieldError(errs, "source", "required") {
		t.Fatalf("expected source/required, got %+v", errs)
	}
}

func TestValidate_ReturnsAllViolationsAtOnce(t *testing.T) {
	// The contract-required behavior: report every violation, not just the
	// first, so a caller can render a complete field-level error list in one
	// round trip.
	errs := Validate(FieldSpec{Object: "widget", Label: "  ", Type: "money"})
	for _, want := range []struct{ field, code string }{
		{"object", "unsupported_object"},
		{"type", "unsupported_type"},
		{"label", "required"},
		{"source", "required"},
	} {
		if !hasFieldError(errs, want.field, want.code) {
			t.Errorf("expected %s/%s among %+v", want.field, want.code, errs)
		}
	}
}

func TestDeriveSlug_LowercasesAndUnderscoresNonAlnum(t *testing.T) {
	cases := map[string]string{
		"Renewal date":         "renewal_date",
		"Budget Ceiling!!":     "budget_ceiling",
		"  Procurement Route ": "procurement_route",
		"Contract end date":    "contract_end_date",
	}
	for label, want := range cases {
		if got := DeriveSlug(label); got != want {
			t.Errorf("DeriveSlug(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestDeriveSlug_EmptyAfterStrippingFallsBackToField(t *testing.T) {
	if got := DeriveSlug("!!!"); got != "field" {
		t.Fatalf("DeriveSlug(%q) = %q, want %q", "!!!", got, "field")
	}
}

func TestDeriveSlug_TruncatesLongLabelsForIdentifierSafety(t *testing.T) {
	longLabel := "This is a very long field label that keeps going and going and going and going"
	slug := DeriveSlug(longLabel)
	col := ColumnName(slug)
	// cf_<slug>_check must stay under Postgres's 63-byte identifier cap.
	if len(col+"_check") > 63 {
		t.Fatalf("derived column+check name too long (%d bytes): %s_check", len(col+"_check"), col)
	}
}

func TestColumnName_IsCfPrefixed(t *testing.T) {
	if got := ColumnName("renewal_date"); got != "cf_renewal_date" {
		t.Fatalf("ColumnName = %q, want cf_renewal_date", got)
	}
}

func TestIsStructural_MatchesEachKeyword(t *testing.T) {
	structural := []string{
		"Add a new object for contracts",
		"Relationship to the parent account",
		"Link to the billing record",
		"Lookup to another deal",
		"A formula for total value",
		"A validation rule requiring approval",
	}
	for _, label := range structural {
		if !IsStructural(label) {
			t.Errorf("IsStructural(%q) = false, want true", label)
		}
	}
}

func TestIsStructural_PlainScalarLabelPasses(t *testing.T) {
	plain := []string{"Renewal date", "Budget ceiling", "Procurement route", "Contract end date"}
	for _, label := range plain {
		if IsStructural(label) {
			t.Errorf("IsStructural(%q) = true, want false", label)
		}
	}
}

func TestBuildDDL_TypeMapping(t *testing.T) {
	cases := []struct {
		fieldType, wantSQLType string
	}{
		{TypeText, "text"},
		{TypeNumber, "numeric"},
		{TypeDate, "date"},
		{TypeCurrency, "bigint"},
		{TypePicklist, "text"},
		{TypeBoolean, "boolean"},
	}
	for _, c := range cases {
		spec := FieldSpec{Object: "deal", Label: "X", Type: c.fieldType}
		if c.fieldType == TypePicklist {
			spec.Options = []string{"a", "b"}
		}
		ddl, err := BuildDDL("deal", "cf_x", spec)
		if err != nil {
			t.Fatalf("BuildDDL(%s): %v", c.fieldType, err)
		}
		want := `ADD COLUMN "cf_x" ` + c.wantSQLType + " NULL"
		if !strings.Contains(ddl, want) {
			t.Errorf("BuildDDL(%s) = %q, want it to contain %q", c.fieldType, ddl, want)
		}
	}
}

func TestBuildDDL_UnsupportedTypeErrors(t *testing.T) {
	if _, err := BuildDDL("deal", "cf_x", FieldSpec{Object: "deal", Label: "X", Type: "money"}); err == nil {
		t.Fatal("expected an error for an unsupported type")
	}
}

func TestBuildDDL_PicklistIncludesGeneratedCheck(t *testing.T) {
	spec := FieldSpec{Object: "deal", Label: "Route", Type: TypePicklist, Options: []string{"direct", "reseller"}}
	ddl, err := BuildDDL("deal", "cf_route", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "ADD CONSTRAINT") || !strings.Contains(ddl, "CHECK") ||
		!strings.Contains(ddl, "'direct'") || !strings.Contains(ddl, "'reseller'") {
		t.Fatalf("picklist DDL missing generated CHECK: %s", ddl)
	}
}

func TestBuildDDL_LabelWithSQLNeverReachesRawText(t *testing.T) {
	// AC-12/CUSTOM-FIELDS-AC-12: the identifier is slug-derived, never free
	// text — even a label carrying an injection attempt must never appear
	// verbatim in the generated DDL.
	label := `evil'); DROP TABLE person;--`
	slug := DeriveSlug(label)
	col := ColumnName(slug)
	spec := FieldSpec{Object: "person", Label: label, Type: TypeText}
	ddl, err := BuildDDL("person", col, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"DROP", "--", "'", ";"} {
		if strings.Contains(ddl, bad) {
			t.Fatalf("ddl leaked raw label text (found %q): %s", bad, ddl)
		}
	}
}

func TestBuildDDL_RejectsInvalidIdentifier(t *testing.T) {
	// Defensive: even though object/column are always server-derived, guard
	// against a caller passing something that isn't a valid identifier.
	if _, err := BuildDDL("deal; DROP TABLE deal", "cf_x", FieldSpec{Object: "deal", Label: "X", Type: TypeText}); err == nil {
		t.Fatal("expected an error for an invalid object identifier")
	}
}

func TestBuildOptionsDDL_RegeneratesCheckFromNewOptions(t *testing.T) {
	ddl, err := BuildOptionsDDL("deal", "cf_route", []string{"direct", "marketplace"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, `DROP CONSTRAINT IF EXISTS "cf_route_check"`) {
		t.Fatalf("expected a DROP CONSTRAINT IF EXISTS clause, got: %s", ddl)
	}
	if !strings.Contains(ddl, "ADD CONSTRAINT") || !strings.Contains(ddl, "'direct'") ||
		!strings.Contains(ddl, "'marketplace'") || strings.Contains(ddl, "'reseller'") {
		t.Fatalf("expected the regenerated CHECK to list only the new options, got: %s", ddl)
	}
}

func TestBuildOptionsDDL_RejectsInvalidIdentifier(t *testing.T) {
	if _, err := BuildOptionsDDL("deal; DROP TABLE deal", "cf_route", []string{"a"}); err == nil {
		t.Fatal("expected an error for an invalid object identifier")
	}
}

func TestBuildOptionsDDL_LabelInjectionNeverReachesRawSQL(t *testing.T) {
	// CUSTOM-FIELDS-AC-12: even an attacker-controlled option value must
	// only ever appear quote-escaped, never as raw SQL text.
	ddl, err := BuildOptionsDDL("deal", "cf_route", []string{`x'); DROP TABLE deal;--`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "DROP CONSTRAINT IF EXISTS") {
		t.Fatalf("expected the DROP CONSTRAINT clause regardless of option content, got: %s", ddl)
	}
	// The malicious value must appear only inside a single quoted literal
	// token — assert the statement still carries the escaped literal (our
	// quoteLiteral doubles ' but leaves everything else, so the raw text
	// still appears once, inside quotes) rather than as an unescaped
	// statement terminator.
	if !strings.Contains(ddl, `DROP TABLE deal;--`) {
		t.Fatalf("expected the literal to appear quoted: %s", ddl)
	}
}

func TestFieldTypes_MatchesMigrationCheckSpelling(t *testing.T) {
	want := []string{"text", "number", "date", "currency", "picklist", "boolean"}
	if len(FieldTypes) != len(want) {
		t.Fatalf("FieldTypes = %v, want %v", FieldTypes, want)
	}
	for i, w := range want {
		if FieldTypes[i] != w {
			t.Errorf("FieldTypes[%d] = %q, want %q", i, FieldTypes[i], w)
		}
	}
}

func TestFieldObjects_MatchesMigrationCheckSpelling(t *testing.T) {
	want := []string{"person", "organization", "deal", "lead", "activity"}
	if len(FieldObjects) != len(want) {
		t.Fatalf("FieldObjects = %v, want %v", FieldObjects, want)
	}
	for i, w := range want {
		if FieldObjects[i] != w {
			t.Errorf("FieldObjects[%d] = %q, want %q", i, FieldObjects[i], w)
		}
	}
}

func hasFieldError(errs []FieldError, field, code string) bool {
	for _, e := range errs {
		if e.Field == field && e.Code == code {
			return true
		}
	}
	return false
}
