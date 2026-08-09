// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// Where a vocabulary field actually lives.
//
// E1 derived the vocabulary from the CONTRACT. Execution happens against the
// DATABASE, and the two do not agree. `strength.score` is a view assembled from
// activity rows, `partner.margin_tier` belongs to another table, `deal.stalled`
// is computed in a mapper, and `organization.logo_url` is minted from an object
// key at read time. Roughly forty published fields have no column on the
// record's own table.
//
// So a field is askable when the contract declares it AND the record's own
// table can answer it. Both halves stay derived — the second from
// information_schema, which is the database's own statement of what it holds —
// so neither becomes a list to maintain, and the property C-7 asked for
// survives. TestEveryPublishedFieldCompilesToAStoragePath is the fitness
// function that keeps the two halves in agreement.
//
// Two rules make the catalog read safe to use here:
//
//  1. It FILTERS; it never CONTRIBUTES. A name only ever leaves the vocabulary
//     by it. cf_* columns are physically present for every workspace that
//     declared one, so a catalog read used as a source would hand one
//     workspace's private column to the next caller. The custom half keeps
//     coming from the workspace-scoped fieldcatalog seam.
//  2. It is a SCHEMA read, not a tenant read — information_schema carries no
//     rows of anyone's data.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StoredColumn is one physical column, as this module needs to see it: its
// name, and the SQL type that decides which vocabulary kinds it can be
// compared under.
type StoredColumn struct {
	Name string
	// Type is the data type information_schema reports (`text`, `numeric`,
	// `timestamp with time zone`, …).
	Type string
}

// ColumnReader answers the physical columns of one table. It is a seam for
// the same reason fieldcatalog.Reader is: the vocabulary resolver runs in
// tests with no database, and a nil reader is the pass-through — the
// vocabulary is then the contract's, WIDER rather than narrower, so an unwired
// deployment cannot be tricked into publishing more than it can answer.
type ColumnReader interface {
	Columns(ctx context.Context, table string) ([]StoredColumn, error)
}

// ColumnCatalog reads the live schema.
type ColumnCatalog struct {
	pool *pgxpool.Pool
}

// NewColumnCatalog builds the reader over the pool.
func NewColumnCatalog(pool *pgxpool.Pool) *ColumnCatalog { return &ColumnCatalog{pool: pool} }

// Columns answers one table's column names.
//
// Nothing is memoized. A cached schema is a schema that keeps answering as it
// was — and the one runtime ALTER TABLE this product has (the custom-field
// engine) would then leave a workspace's own new column unaskable until the
// process restarted, for a saving of one indexed catalog read per plan.
func (c *ColumnCatalog) Columns(ctx context.Context, table string) ([]StoredColumn, error) {
	// rls-exempt: information_schema is the database's own schema catalog; it holds no tenant rows, so there is no workspace to bind
	rows, err := c.pool.Query(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		  WHERE table_schema = current_schema() AND table_name = $1`, table)
	if err != nil {
		return nil, fmt.Errorf("search: reading the columns of %s: %w", table, err)
	}
	defer rows.Close()
	var columns []StoredColumn
	for rows.Next() {
		var column StoredColumn
		if err := rows.Scan(&column.Name, &column.Type); err != nil {
			return nil, fmt.Errorf("search: reading the columns of %s: %w", table, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: reading the columns of %s: %w", table, err)
	}
	return columns, nil
}

var _ ColumnReader = (*ColumnCatalog)(nil)

// storage is one table's columns, in the form the vocabulary filter and the
// SQL builder both read: column name → the vocabulary kind that column can be
// compared under.
type storage struct {
	kinds map[string]FieldKind
	// unfiltered is the pass-through the vocabulary resolver gets when no
	// schema reader is wired. It answers every field and locates none, which
	// is the honest pair: without a schema to check against the vocabulary is
	// the contract's, and a vocabulary nobody checked is not a place to
	// execute from.
	unfiltered bool
}

// unfilteredStorage is the no-schema pass-through.
func unfilteredStorage() *storage { return &storage{unfiltered: true} }

// newStorage indexes a table's columns by the kind each can answer. A column
// of a type this vocabulary has no kind for indexes as the empty kind, which
// no field matches — so it is present and unaskable rather than absent and
// indistinguishable from a typo.
func newStorage(columns []StoredColumn) *storage {
	s := &storage{kinds: make(map[string]FieldKind, len(columns))}
	for _, c := range columns {
		s.kinds[c.Name] = kindsByColumnType[c.Type]
	}
	return s
}

// kindsByColumnType maps a SQL type onto the ONE vocabulary kind it can be
// compared under.
//
// It exists because the contract and the schema disagree about more than
// names. `deal.fx_rate_to_base` is a STRING in the contract — a decimal
// rendered as text, so a ten-place rate never rounds through a float — and a
// `numeric` column in the table. Binding a text operand against it is not a
// narrower answer; it is `invalid input syntax for type numeric`, a fault the
// caller cannot act on for a field the schema told them they could ask. So a
// field whose contract kind the column cannot answer is out of the vocabulary,
// the same as a field with no column at all.
var kindsByColumnType = map[string]FieldKind{
	"text": KindText, "character varying": KindText, "character": KindText,
	"uuid":     KindID,
	"boolean":  KindBoolean,
	"smallint": KindNumber, "integer": KindNumber, "bigint": KindNumber,
	"numeric": KindNumber, "real": KindNumber, "double precision": KindNumber,
	"date":                     KindDate,
	"timestamp with time zone": KindTimestamp, "timestamp without time zone": KindTimestamp,
}

// schemaReads memoizes one resolve's schema reads. Composing a record type's
// vocabulary asks about several tables — an inverse hop is declared by the
// REFERRING record — so without it the published document re-reads the same
// table once per target that names it.
//
// It lives for ONE resolve and no longer. A cache that outlived the call would
// be exactly what Columns refuses to be: a schema that keeps answering as it
// was while the custom-field engine adds a column under it.
type schemaReads struct {
	reader ColumnReader
	byName map[string]*storage
}

func newSchemaReads(reader ColumnReader) *schemaReads {
	return &schemaReads{reader: reader, byName: map[string]*storage{}}
}

// of answers one record type's storage, reading it at most once per resolve.
func (s *schemaReads) of(ctx context.Context, entity string) (*storage, error) {
	if cached, ok := s.byName[entity]; ok {
		return cached, nil
	}
	stored, err := storageFor(ctx, s.reader, entity)
	if err != nil {
		return nil, err
	}
	s.byName[entity] = stored
	return stored, nil
}

// storageFor reads one record type's columns through the seam, or the
// unfiltered pass-through when no reader is wired.
func storageFor(ctx context.Context, reader ColumnReader, entity string) (*storage, error) {
	if reader == nil {
		return unfilteredStorage(), nil
	}
	table, ok := tableFor(entity)
	if !ok {
		return nil, fmt.Errorf("search: %q is not a searchable record type", entity)
	}
	columns, err := reader.Columns(ctx, table)
	if err != nil {
		return nil, err
	}
	return newStorage(columns), nil
}

// answers reports whether this table can answer the field.
//
// A PLACE is the one field published without a column behind it, and
// deliberately: SEARCH-AC-17 declares `within_radius` so that it answers
// `distance_ranking_unavailable` rather than reading as an unknown operator.
// Filtering the place out would turn that honest note into an
// unknown-vocabulary refusal, which sends a caller to a text match on a city
// name — the quietly wrong answer declaring the operator exists to avoid.
func (s *storage) answers(field Field) bool {
	if s.unfiltered || field.Kind == KindGeo {
		return true
	}
	_, ok := s.locate(field)
	return ok
}

// locate resolves a vocabulary field onto a column that can answer it under
// the field's own kind.
//
// A nested contract object is stored FLAT — the contract's `address.city` is
// the column `address_city` (migration 0051 replaced the jsonb document with
// one column per leaf) — so a dotted name is looked up under its flattened
// spelling. The lookup still decides: a path with no column behind it, or one
// whose column answers a different kind, is not askable however it is spelled.
func (s *storage) locate(field Field) (column string, ok bool) {
	for _, candidate := range []string{field.Name, strings.ReplaceAll(field.Name, ".", "_")} {
		if kind, exists := s.kinds[candidate]; exists {
			return candidate, kind == field.Kind
		}
	}
	return "", false
}

// expr renders the SQL expression a predicate compares against.
//
// A place has no expression: it is what a radius would be measured from, and
// `within_radius` answers distance_ranking_unavailable before any SQL is
// built. Answering false rather than an expression keeps that a loud wiring
// fault instead of a comparison against a column that does not exist.
func (s *storage) expr(alias string, field Field) (string, bool) {
	if field.Kind == KindGeo {
		return "", false
	}
	column, ok := s.locate(field)
	if !ok {
		return "", false
	}
	return alias + "." + sanitize(column), true
}

// sanitize quotes an identifier before it is interpolated into a statement.
// Every identifier that reaches it was named by the database's own catalog, so
// this is the second lock on a door that is already shut — kept because the
// argument that it is already shut lives in a comment, and the next edit to
// this file does not have to have read it.
func sanitize(identifier string) string { return pgx.Identifier{identifier}.Sanitize() }

// tableFor answers the physical table one searchable record type lives in,
// read off the branch declarations the lexical arm already rides so a plan
// and a search cannot disagree about where a record type is stored.
func tableFor(entity string) (string, bool) {
	branch, ok := branchFor(entity)
	return branch.table, ok
}

// branchFor answers the whole branch declaration for a record type — its
// table, its display title, and the discovery narrowing every read of it
// carries.
func branchFor(entity string) (searchBranch, bool) {
	for _, branch := range searchBranches {
		if branch.entity == entity {
			return branch, true
		}
	}
	return searchBranch{}, false
}
