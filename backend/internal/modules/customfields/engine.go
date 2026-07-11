// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package customfields is the governed add-field engine (custom-fields.md):
// the single chokepoint in the system allowed to run a runtime ALTER TABLE.
// It validates a field definition against the closed type/object sets,
// derives its namespaced physical column identifier, generates the DDL only
// from the validated spec (never raw user text), and detects a structural
// request so a caller can refuse it. This file is the pure engine core —
// no DB handle, no HTTP — so the DDL strings it returns can be exercised
// without a database in the unit-test lane; a later ticket wires the
// transaction that runs them (ALTER TABLE + custom_field catalog INSERT +
// one audit_log row, atomically, per the module's write-shape rule).
package customfields

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The closed type/object sets (CUSTOM-FIELDS-PARAM-1/PARAM-2). No cap,
// no widening — the surface itself is the knob (custom-fields.md). The
// literal spellings mirror the migration's CHECK constraints
// (migrations/core/0063_custom_field_catalog.up.sql) exactly — Validate,
// the catalog CHECK, and this package must never drift apart.
const (
	TypeText     = "text"
	TypeNumber   = "number"
	TypeDate     = "date"
	TypeCurrency = "currency"
	TypePicklist = "picklist"
	TypeBoolean  = "boolean"
)

// FieldTypes is the closed, ordered set of supported field types, spelled
// the way the custom_field.type CHECK constraint spells them.
var FieldTypes = []string{TypeText, TypeNumber, TypeDate, TypeCurrency, TypePicklist, TypeBoolean}

// FieldObjects is the closed, ordered set of core objects a custom field can
// attach to, spelled the way the custom_field.object CHECK constraint
// spells them.
var FieldObjects = []string{"person", "organization", "deal", "lead", "activity"}

var allowedObjects = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, "activity": true,
}

var allowedTypes = map[string]bool{
	TypeText: true, TypeNumber: true, TypeDate: true, TypeCurrency: true, TypePicklist: true, TypeBoolean: true,
}

var currencyCodeRe = regexp.MustCompile(`^[A-Z]{3}$`)

// FieldSpec is a candidate custom-field definition — the only source the
// DDL generator is permitted to read from. Unlike the poc-1 reference this
// carries no CapturedBy: this repo's write shape stamps provenance
// (captured_by) from the authenticated principal, never from a request
// body field, so the engine core has no business holding it.
type FieldSpec struct {
	Object   string
	Label    string
	Type     string
	Currency *string
	Options  []string
	Source   string
}

// FieldError is one field-level validation failure, matching the contract's
// ValidationError `details.errors[]` shape ({field, code}) — the json tags
// are load-bearing: without them the wire body would marshal as
// {"Field":...,"Code":...} (capitalised), violating the contract.
type FieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// Field-name and code constants shared across Validate and (in a later
// ticket) the transaction's audit-entry diff map — extracted so the
// repeated literal satisfies golangci-lint's goconst rule.
const (
	fieldObject   = "object"
	fieldType     = "type"
	fieldLabel    = "label"
	fieldCurrency = "currency"
	fieldOptions  = "options"
	fieldSource   = "source"
	codeRequired  = "required"

	// codeInvalidCharacters flags a picklist option carrying a NUL byte or
	// invalid UTF-8 — the same class of malformed input the identifier path
	// already refuses (pgx.Identifier.Sanitize strips NULs). An option value
	// has no such protection: it is Sprintf'd raw into generated DDL, so it
	// must be refused at the request boundary rather than forwarded.
	codeInvalidCharacters = "invalid_characters"
)

// validOptionText reports whether a picklist option is safe to carry into
// generated DDL and, downstream, onto the wire: valid UTF-8 with no
// embedded NUL byte. NUL terminates a wire-protocol string, so a
// NUL-carrying option would silently truncate whatever message the DDL
// ends up embedded in rather than fail loudly — this is the request-side
// half of the same defense quoteLiteral applies again at the DDL boundary
// (checkConstraintClause), matching this package's belt-and-suspenders
// posture for identifiers.
func validOptionText(s string) bool {
	return utf8.ValidString(s) && !strings.Contains(s, "\x00")
}

// Validate checks spec against the closed type/object sets and the
// conditional-required rules (currency required iff type=currency, options
// required non-empty iff type=picklist). Returns every violation found, not
// just the first, so a caller can render a complete field-level error list
// in one round trip.
func Validate(spec FieldSpec) []FieldError {
	var errs []FieldError
	if !allowedObjects[spec.Object] {
		errs = append(errs, FieldError{Field: fieldObject, Code: "unsupported_object"})
	}
	if !allowedTypes[spec.Type] {
		errs = append(errs, FieldError{Field: fieldType, Code: "unsupported_type"})
	}
	if strings.TrimSpace(spec.Label) == "" {
		errs = append(errs, FieldError{Field: fieldLabel, Code: codeRequired})
	}
	if spec.Type == TypeCurrency && (spec.Currency == nil || !currencyCodeRe.MatchString(*spec.Currency)) {
		errs = append(errs, FieldError{Field: fieldCurrency, Code: "required_for_type_currency"})
	}
	if spec.Type == TypePicklist && len(spec.Options) == 0 {
		errs = append(errs, FieldError{Field: fieldOptions, Code: "required_for_type_picklist"})
	}
	if spec.Type == TypePicklist {
		for _, o := range spec.Options {
			if !validOptionText(o) {
				errs = append(errs, FieldError{Field: fieldOptions, Code: codeInvalidCharacters})
				break
			}
		}
	}
	if strings.TrimSpace(spec.Source) == "" {
		errs = append(errs, FieldError{Field: fieldSource, Code: codeRequired})
	}
	return errs
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// maxSlugLen keeps `cf_<slug>_check` (the longest identifier this package
// generates) safely under Postgres's 63-byte identifier cap.
const maxSlugLen = 40

// DeriveSlug turns a display label into the admin-facing key the physical
// column name derives from (CUSTOM-FIELDS-PARAM-3): lowercased,
// non-alphanumeric runs collapsed to a single underscore, trimmed, and
// length-capped. Never returns raw label text.
func DeriveSlug(label string) string {
	s := strings.ToLower(strings.TrimSpace(label))
	s = nonAlnum.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "field"
	}
	if len(s) > maxSlugLen {
		s = strings.Trim(s[:maxSlugLen], "_")
	}
	return s
}

// ColumnName derives the cf_-prefixed physical column identifier from slug
// (CUSTOM-FIELDS-PARAM-3) — never client-supplied, immutable once live.
func ColumnName(slug string) string {
	return "cf_" + slug
}

var identifierRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// validIdentifier defensively re-checks an identifier even though both
// object and columnName are always server-derived by this point — belt and
// suspenders per the spec's explicit instruction.
func validIdentifier(s string) bool {
	return len(s) > 0 && len(s) <= 63 && identifierRe.MatchString(s)
}

// structuralKeywords are the AC-custom-fields-5 example phrases: a label
// smelling like a new object, relationship, or logic is refused, never
// silently accepted (CUSTOM-FIELDS-AC-4/AC-8).
var structuralKeywords = []string{
	"object", "relationship", "link to", "lookup to", "formula", "validation rule",
}

// IsStructural reports whether label smells like a structural request
// rather than a bounded scalar attribute.
func IsStructural(label string) bool {
	l := strings.ToLower(label)
	for _, kw := range structuralKeywords {
		if strings.Contains(l, kw) {
			return true
		}
	}
	return false
}

// sqlType maps a validated field type to its storage type
// (CUSTOM-FIELDS-PARAM-4): text->text, number->numeric (string round-trip,
// no float), date->date, currency->bigint minor-units (the ISO-4217 code
// lives in the catalog row, not the column), boolean->boolean,
// picklist->text+generated CHECK.
func sqlType(fieldType string) (string, error) {
	switch fieldType {
	case TypeText:
		return "text", nil
	case TypeNumber:
		return "numeric", nil
	case TypeDate:
		return "date", nil
	case TypeCurrency:
		return "bigint", nil
	case TypePicklist:
		return "text", nil
	case TypeBoolean:
		return "boolean", nil
	default:
		return "", fmt.Errorf("customfields: unsupported type %q", fieldType)
	}
}

// quoteIdentifier wraps name as a double-quoted Postgres identifier via
// pgx.Identifier — the ONE spelling this package uses for identifier
// quoting. pgx (unlike lib/pq) exposes no literal quoter, so it is used
// here for identifiers only; quoteLiteral below covers string literals.
func quoteIdentifier(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// quoteLiteral wraps s as a single-quoted Postgres string literal, safe to
// splice into generated DDL. pgx exposes no public literal quoter (only an
// internal one used by its query-log sanitizer), so this package carries
// its own — deliberately narrow and specific to the CHECK-constraint
// option lists BuildDDL/BuildOptionsDDL generate.
//
// Postgres's standard_conforming_strings has defaulted to on since 9.1, so
// a plain string literal treats a backslash as a literal character, not an
// escape — doubling the single quote is the only escaping a plain '...'
// literal needs. Defensively, a value that still contains a backslash is
// wrapped in the C-style E'...' form (with backslashes themselves doubled)
// so behaviour stays correct even if a session ever overrides that
// default — mirroring libpq's PQescapeStringInternal, which lib/pq's
// QuoteLiteral (this package's poc-1 predecessor) also follows.
//
// Rejects an embedded NUL byte even though every caller is expected to have
// already run validOptionText: NUL terminates a wire-protocol string, and
// this literal is Sprintf'd raw into a DDL string headed for exactly such a
// message, so a NUL that slipped past validation must fail loudly here
// rather than silently truncate the statement downstream — the identifier
// path gets the equivalent guarantee for free from pgx.Identifier.Sanitize,
// which strips NULs; a plain string literal has no such built-in escape.
func quoteLiteral(s string) (string, error) {
	if strings.Contains(s, "\x00") {
		return "", fmt.Errorf("customfields: option literal contains a NUL byte")
	}
	s = strings.ReplaceAll(s, `'`, `''`)
	if strings.Contains(s, `\`) {
		s = strings.ReplaceAll(s, `\`, `\\`)
		return `E'` + s + `'`, nil
	}
	return `'` + s + `'`, nil
}

// BuildDDL returns the ALTER TABLE statement adding the validated field's
// column — CUSTOM-FIELDS-SCHEMA-2: `ALTER TABLE <object> ADD COLUMN
// cf_<slug> <mapped-type> NULL`, plus a generated CHECK constraint for a
// picklist's allowed values (CUSTOM-FIELDS-PARAM-4). Every token is derived
// only from spec (already closed-set validated by Validate) and quoted
// identifiers/literals — never raw request text, so an injection attempt
// in Label cannot reach the database as free text (CUSTOM-FIELDS-AC-12).
func BuildDDL(object, columnName string, spec FieldSpec) (string, error) {
	if !validIdentifier(object) {
		return "", fmt.Errorf("customfields: invalid object identifier %q", object)
	}
	if !validIdentifier(columnName) {
		return "", fmt.Errorf("customfields: invalid column identifier %q", columnName)
	}
	colType, err := sqlType(spec.Type)
	if err != nil {
		return "", err
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s NULL",
		quoteIdentifier(object), quoteIdentifier(columnName), colType)
	if spec.Type == TypePicklist {
		checkName, clause, err := checkConstraintClause(columnName, spec.Options)
		if err != nil {
			return "", err
		}
		stmt += fmt.Sprintf(", ADD CONSTRAINT %s %s", quoteIdentifier(checkName), clause)
	}
	return stmt, nil
}

// checkConstraintClause builds the "<col>_check" identifier and its
// "CHECK (<col> IS NULL OR <col> IN (...))" clause shared by a picklist's
// initial CHECK (BuildDDL, at create time) and a picklist's regenerated
// CHECK after an options edit (BuildOptionsDDL) — extracted so the quoting
// logic lives in exactly one place (CUSTOM-FIELDS-PARAM-5). Every token is
// quoted; never raw text (CUSTOM-FIELDS-AC-12).
//
// Re-validates each option with validOptionText even though Validate is
// expected to have already run: BuildDDL/BuildOptionsDDL are this
// package's DDL-generation boundary, not just its request-validation
// boundary, so they carry the same defense-in-depth check rather than
// trust an upstream caller.
func checkConstraintClause(columnName string, options []string) (checkName, clause string, err error) {
	checkName = columnName + "_check"
	if !validIdentifier(checkName) {
		return "", "", fmt.Errorf("customfields: invalid check-constraint identifier %q", checkName)
	}
	quotedCol := quoteIdentifier(columnName)
	quotedOpts := make([]string, len(options))
	for i, o := range options {
		if !validOptionText(o) {
			return "", "", fmt.Errorf("customfields: picklist option contains a NUL byte or invalid UTF-8")
		}
		lit, err := quoteLiteral(o)
		if err != nil {
			return "", "", fmt.Errorf("customfields: %w", err)
		}
		quotedOpts[i] = lit
	}
	clause = fmt.Sprintf("CHECK (%s IS NULL OR %s IN (%s))", quotedCol, quotedCol, strings.Join(quotedOpts, ", "))
	return checkName, clause, nil
}

// BuildOptionsDDL returns the ALTER TABLE statement that regenerates an
// existing picklist column's CHECK constraint from a new option set
// (CUSTOM-FIELDS-PARAM-5): DROP CONSTRAINT IF EXISTS <col>_check, then ADD
// CONSTRAINT rebuilt via checkConstraintClause — the same quoting helper
// BuildDDL's picklist branch uses. object/columnName are always
// server-derived (never client text) by the time this is called, but both
// are re-validated defensively, matching BuildDDL's own posture.
func BuildOptionsDDL(object, columnName string, options []string) (string, error) {
	if !validIdentifier(object) {
		return "", fmt.Errorf("customfields: invalid object identifier %q", object)
	}
	if !validIdentifier(columnName) {
		return "", fmt.Errorf("customfields: invalid column identifier %q", columnName)
	}
	checkName, clause, err := checkConstraintClause(columnName, options)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s, ADD CONSTRAINT %s %s",
		quoteIdentifier(object), quoteIdentifier(checkName), quoteIdentifier(checkName), clause), nil
}
