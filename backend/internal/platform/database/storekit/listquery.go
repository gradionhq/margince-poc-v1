// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The list sort/filter vocabulary (data-model §13, CF-T05 AC-OPEN-1):
// the three record lists (people, organizations, deals) validate their
// `sort` spec and cf_* equality filters against a closed per-resource
// vocabulary — a fixed core set each store declares plus the workspace's
// ACTIVE custom columns (fieldcatalog seam), so a retired or unknown
// cf_ field is refused with the contract's 422 codes, never guessed.
//
// V1 scope, deliberately (poc-1 parity, honest cursor): ONE sort field
// per request on top of the house (created_at, id) tie-breaker — the
// keyset cursor then extends to (sort key, created_at, id) with an exact
// NULLS-LAST continuation clause, so a sorted list paginates stably.
// poc-1 accepted multi-field sorts but kept its id-only cursor, which
// mis-pages every non-default sort; this repo refuses what it cannot
// paginate instead. Filters are equality-only, matching poc-1's
// cf_<name>=<value> list parameters.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// KindTimestamp and KindUUID type the core vocabulary entries the six
// fieldcatalog custom-column types do not cover: the timestamptz columns
// (created_at, updated_at, last_activity_at) and the uuid references the
// spec's sort tables name (owner_id, DM-VOCAB-1/2).
const (
	KindTimestamp = "timestamptz"
	KindUUID      = "uuid"
)

// defaultSortSpelling is the contract's documented default sort. It IS
// the default, so the spelling is accepted and normalized to it — the
// one multi-field spec that means something under the house cursor.
const defaultSortSpelling = "-created_at,id"

// SortError is the typed sort-spec refusal; the transport maps it onto
// the httperr.Validation 422 shape (data-model §13.5's
// "anything else → 422"), keyed to the `sort` parameter.
type SortError struct {
	Code    string
	Message string
}

func (e *SortError) Error() string { return "sort: " + e.Message + " (" + e.Code + ")" }

// The SortError codes: the §13.5-documented refusal for a field outside
// the vocabulary, and the honest-scope refusal for the multi-field specs
// V1's keyset cursor cannot paginate.
const (
	CodeSortFieldNotAllowed = "sort_field_not_allowed"
	CodeSortUnsupported     = "sort_unsupported"
)

// SortVocabulary merges a resource's fixed core sortable fields (name →
// kind) with the workspace's active custom columns. Retired columns left
// ActiveColumns, so they leave the vocabulary — and its 422 — for free.
func SortVocabulary(core map[string]string, active []fieldcatalog.Column) map[string]string {
	vocab := make(map[string]string, len(core)+len(active))
	for name, kind := range core {
		vocab[name] = kind
	}
	for _, c := range active {
		vocab[c.Name] = c.Type
	}
	return vocab
}

// ListSort is one validated sort: a vocabulary field plus direction. The
// nil *ListSort is the default sort (-created_at, id) and every method
// answers its default shape, so stores thread one value through
// unconditionally.
type ListSort struct {
	name string
	kind string
	desc bool
}

// ParseListSort validates a list's sort spec against the resource's
// vocabulary. nil/empty (and the documented default spelling) mean the
// default sort; anything the vocabulary does not name — or a multi-field
// spec the keyset cursor cannot continue — is a typed refusal.
//
//nolint:nilnil // the nil *ListSort IS the default sort by design: every ListSort method answers its default shape on a nil receiver, so callers thread one value unconditionally
func ParseListSort(spec *string, vocab map[string]string) (*ListSort, error) {
	if spec == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(*spec)
	if raw == "" || raw == defaultSortSpelling {
		return nil, nil
	}
	if strings.Contains(raw, ",") {
		return nil, &SortError{
			Code:    CodeSortUnsupported,
			Message: "sort takes one field (the created_at,id tie-breaker is always appended); only the default \"-created_at,id\" spelling names two",
		}
	}
	name := strings.TrimPrefix(raw, "-")
	kind, ok := vocab[name]
	if !ok || name == "" {
		return nil, &SortError{
			Code:    CodeSortFieldNotAllowed,
			Message: fmt.Sprintf("field %q is not sortable on this resource", name),
		}
	}
	return &ListSort{name: name, kind: kind, desc: strings.HasPrefix(raw, "-")}, nil
}

// OrderBy renders the ORDER BY clause: the sort field first (NULLS LAST
// under both directions, so the NULL tail sits at the end where the
// keyset continuation expects it), then the house (created_at, id)
// tie-breaker that makes every ordering total.
func (s *ListSort) OrderBy() string {
	if s == nil {
		return " ORDER BY created_at DESC, id DESC"
	}
	dir := "ASC"
	if s.desc {
		dir = "DESC"
	}
	return " ORDER BY " + quoteColumnIdentifier(s.name) + " " + dir + " NULLS LAST, created_at DESC, id DESC"
}

// CursorKeySuffix is the trailing SELECT expression a sorted list scans
// its cursor key from: the sort column in Postgres text form, which
// round-trips exactly through the typed bind cast on the next page
// (empty for the default sort — the house cursor needs no extra key).
// The alias keeps the output name distinct from the sort column itself,
// which the same SELECT already carries — without it, ORDER BY <column>
// would be ambiguous between the two.
func (s *ListSort) CursorKeySuffix() string {
	if s == nil {
		return ""
	}
	return ", (" + quoteColumnIdentifier(s.name) + `)::text AS "__cursor_key"`
}

// EncodePageCursor mints the next-page token: the house (created_at, id)
// tuple, extended under a non-default sort with the sort field, its
// direction, and the last row's key (nil = the row sits in the NULL
// tail) so the next page can only continue the SAME ordering.
func (s *ListSort) EncodePageCursor(sortKey *string, createdAt time.Time, id ids.UUID) string {
	if s == nil {
		return EncodeCursor(createdAt, id)
	}
	return mintCursorToken(Cursor{CreatedAt: createdAt, ID: id, SortField: s.name, SortDesc: s.desc, SortKey: sortKey})
}

// KeysetClause renders the WHERE fragment that continues the page after
// token under this sort. A token minted under a different sort (or
// direction) is a CursorSortMismatchError — its keyset tuple cannot
// order this list; a token whose sort key does not parse as the sort
// column's kind is malformed, refused here so a crafted key never
// reaches the typed bind cast where Postgres would fail the query. For a
// non-null key the continuation is exact — strictly past the key, OR
// equal-key rows past the (created_at, id) tie-breaker, OR the NULL tail
// that sorts after every key; once inside the NULL tail only the house
// tuple advances.
func (s *ListSort) KeysetClause(token string, arg func(any) int) (string, error) {
	c, err := DecodeCursor(token)
	if err != nil {
		return "", err
	}
	if s == nil {
		if c.SortField != "" {
			return "", &CursorSortMismatchError{}
		}
		return SQLf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)), nil
	}
	if c.SortField != s.name || c.SortDesc != s.desc {
		return "", &CursorSortMismatchError{}
	}

	col := quoteColumnIdentifier(s.name)
	if c.SortKey == nil {
		return SQLf("(%s IS NULL AND (created_at, id) < ($%d, $%d))", col, arg(c.CreatedAt), arg(c.ID)), nil
	}
	if !parsesAsKind(s.kind, *c.SortKey) {
		return "", &MalformedCursorError{}
	}
	cmp := ">"
	if s.desc {
		cmp = "<"
	}
	key := SQLf("$%d%s", arg(*c.SortKey), listBindCast(s.kind))
	return SQLf("(%s %s %s OR (%s = %s AND (created_at, id) < ($%d, $%d)) OR %s IS NULL)",
		col, cmp, key, col, key, arg(c.CreatedAt), arg(c.ID), col), nil
}

// CustomFilterClauses renders a list's cf_* equality filters (V1's whole
// filter grammar for custom columns — poc-1 parity; the typed-operator
// predicate engine stays the dynamic-list/export surface). Each filter
// must name an ACTIVE custom column and carry a value parseable as the
// column's type; refusals reuse the predicate engine's 422 codes. Clause
// order is sorted for a deterministic statement; every value binds as a
// parameter under the column's typed cast.
func CustomFilterClauses(active []fieldcatalog.Column, filters map[string]string, arg func(any) int) ([]string, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	byName := make(map[string]fieldcatalog.Column, len(active))
	for _, c := range active {
		byName[c.Name] = c
	}
	names := make([]string, 0, len(filters))
	for name := range filters {
		names = append(names, name)
	}
	sort.Strings(names)

	clauses := make([]string, 0, len(names))
	for _, name := range names {
		c, ok := byName[name]
		if !ok {
			return nil, &PredicateError{
				Field: name, Code: CodeFilterFieldNotAllowed,
				Message: fmt.Sprintf("field %q is not filterable on this resource", name),
			}
		}
		value := filters[name]
		if err := checkListFilterValue(c, value); err != nil {
			return nil, err
		}
		clauses = append(clauses, SQLf("%s = $%d%s", quoteColumnIdentifier(c.Name), arg(value), listBindCast(c.Type)))
	}
	return clauses, nil
}

// checkListFilterValue validates one query-parameter filter value (text
// on the wire) against its column's type before it may bind under that
// type's cast — a malformed value answers the typed 422, never a query
// execution error.
func checkListFilterValue(c fieldcatalog.Column, value string) error {
	if parsesAsKind(c.Type, value) {
		return nil
	}
	return &PredicateError{
		Field: c.Name, Code: CodeFilterValueInvalid,
		Message: fmt.Sprintf("filtering the %s field %q takes %s", c.Type, c.Name, kindOperandShape(c.Type)),
	}
}

// parsesAsKind reports whether a text-form value (a cf_* query parameter
// or a cursor's sort key) parses as the vocabulary kind, so it may bind
// under the kind's typed cast without risking a query execution error.
// text and picklist accept any string.
func parsesAsKind(kind, value string) bool {
	switch kind {
	case fieldcatalog.TypeNumber:
		_, err := strconv.ParseFloat(value, 64)
		return err == nil
	case fieldcatalog.TypeCurrency:
		_, err := strconv.ParseInt(value, 10, 64)
		return err == nil
	case fieldcatalog.TypeDate:
		_, err := time.Parse("2006-01-02", value)
		return err == nil
	case fieldcatalog.TypeBoolean:
		return value == "true" || value == "false"
	case KindUUID:
		_, err := ids.Parse(value)
		return err == nil
	case KindTimestamp:
		return parsesAsTimestamptz(value)
	default: // text, picklist
		return true
	}
}

// pgTimestamptzTextLayouts cover Postgres's timestamptz text output —
// the form a sorted cursor key round-trips through — with and without
// fractional seconds, under whole-hour (`+00`) and minute (`+05:30`)
// session-timezone offsets, plus the RFC 3339 spelling for good measure.
var pgTimestamptzTextLayouts = []string{
	"2006-01-02 15:04:05.999999-07",
	"2006-01-02 15:04:05.999999-07:00",
	time.RFC3339Nano,
}

func parsesAsTimestamptz(value string) bool {
	for _, layout := range pgTimestamptzTextLayouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

// kindOperandShape names the text form a kind's values take — the
// actionable half of a value-invalid refusal.
func kindOperandShape(kind string) string {
	switch kind {
	case fieldcatalog.TypeNumber:
		return "a number"
	case fieldcatalog.TypeCurrency:
		return "an integer minor-unit amount"
	case fieldcatalog.TypeDate:
		return "an ISO date (YYYY-MM-DD)"
	case fieldcatalog.TypeBoolean:
		return "true or false"
	case KindUUID:
		return "a UUID"
	case KindTimestamp:
		return "a timestamp"
	default: // text, picklist
		return "a string"
	}
}

// listBindCast is the explicit bind cast per vocabulary kind, so a
// text-form value (cursor key or query parameter) compares under the
// column's own type — never as text.
func listBindCast(kind string) string {
	switch kind {
	case fieldcatalog.TypeNumber:
		return "::numeric"
	case fieldcatalog.TypeDate:
		return "::date"
	case fieldcatalog.TypeBoolean:
		return "::boolean"
	case fieldcatalog.TypeCurrency:
		return "::bigint"
	case KindTimestamp:
		return "::timestamptz"
	case KindUUID:
		return "::uuid"
	default: // text, picklist
		return "::text"
	}
}
