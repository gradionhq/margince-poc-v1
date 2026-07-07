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
type Cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        ids.UUID  `json:"id"`
}

func EncodeCursor(createdAt time.Time, id ids.UUID) string {
	raw, _ := json.Marshal(Cursor{CreatedAt: createdAt, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// MalformedCursorError is a client fault: the opaque keyset token is
// client-supplied input, so failing to decode it maps to a 4xx at the
// transport (httperr), never a 500.
type MalformedCursorError struct{}

func (*MalformedCursorError) Error() string { return "store: malformed cursor" }

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
