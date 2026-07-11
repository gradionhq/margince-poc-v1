// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Page is a keyset-paginated result window.
type Page struct {
	NextCursor string
	HasMore    bool
}

// Cursor is the opaque keyset token: the last row's (created_at, id)
// under the default -created_at,id sort. Keyset, never offset (CAP-PAGE).
// A non-default sort (listquery.go) extends the tuple with the sort
// field, its direction, and the last row's key in Postgres text form
// (nil = the row sits in the NULL tail), so a token can only continue
// the ordering it was minted under.
type Cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        ids.UUID  `json:"id"`
	SortField string    `json:"s,omitempty"`
	SortDesc  bool      `json:"d,omitempty"`
	SortKey   *string   `json:"v,omitempty"`
}

func EncodeCursor(createdAt time.Time, id ids.UUID) string {
	return mintCursorToken(Cursor{CreatedAt: createdAt, ID: id})
}

func mintCursorToken(c Cursor) string {
	//craft:ignore swallowed-errors Cursor is plain data (time, uuid, string fields) — json.Marshal cannot fail on it, and a token mint has no error channel to a caller mid-page
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// MalformedCursorError is a client fault: the opaque keyset token is
// client-supplied input, so failing to decode it — or a decoded sort key
// that does not parse as the sort column's kind — maps to a 4xx at the
// transport (httperr), never a 500.
type MalformedCursorError struct{}

func (*MalformedCursorError) Error() string { return "store: malformed cursor" }

// CursorSortMismatchError is the other cursor client fault: the token
// decodes fine but was minted under a different sort (field or
// direction), so its keyset tuple cannot continue this list. Distinct
// from MalformedCursorError because the contract's Cursor parameter
// promises its own code (422 cursor_param_mismatch) for exactly this
// case — the caller drops the cursor or restores the original sort.
type CursorSortMismatchError struct{}

func (*CursorSortMismatchError) Error() string {
	return "store: cursor was minted under a different sort"
}

func DecodeCursor(token string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, &MalformedCursorError{}
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, &MalformedCursorError{}
	}
	return c, nil
}

// SQLf keeps store-side SQL assembly lines readable; arguments are
// always positional parameters or fixed identifiers, never user input.
func SQLf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// ClampLimit applies the contract's CAP-PAGE bounds (default 50, max 200).
func ClampLimit(limit *int) int {
	switch {
	case limit == nil:
		return 50
	case *limit < 1:
		return 1
	case *limit > 200:
		return 200
	default:
		return *limit
	}
}

// QuickFindClause renders the list-q predicate: the full-text match
// (websearch syntax, accent-folded) OR a trigram contains-match on the
// entity's name expression — the as-you-type quick-find ("Rech" finds
// "Rechnung GmbH", "Muller" finds "Müller") that token-based tsquery
// cannot serve. nameExpr must mirror the expression of the entity's
// *_name_trgm index so the LIKE stays indexed; the query text is a bind
// parameter (LIKE metacharacters at worst widen the caller's own match).
func QuickFindClause(pos int, nameExpr string) string {
	return fmt.Sprintf(`(search_tsv @@ websearch_to_tsquery('simple', f_unaccent($%[1]d))
	   OR f_unaccent(lower(%[2]s)) LIKE '%%' || f_unaccent(lower($%[1]d)) || '%%')`, pos, nameExpr)
}
