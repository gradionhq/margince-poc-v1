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

// SweepCursor is a position in a walk across SEVERAL streams: which stream the
// page stopped in, and where inside that stream it stopped.
//
// A walk with no ordering to interleave its streams by — a search across
// record types, whose rows carry no common rank — can still be resumed if the
// token says both. The stream alone would restart it; the inner position alone
// would not say what it indexes into.
//
// Both providers that sweep mint this ONE token, rather than a shape each:
// what a caller pages with must not depend on which system of record answered
// them, and two codecs for one wire value drift the first time either changes.
// The inner half stays opaque here — a keyset token on one side, an incumbent
// mirror's own cursor on the other — because this type carries a position, not
// a meaning.
type SweepCursor struct {
	Stream string `json:"s"`
	Inner  string `json:"c"`
}

// EncodeSweepCursor renders a resume position opaquely: a caller never builds
// or edits one.
//
// It answers an error rather than an empty token because the caller pairs the
// result with "there is more" — a silent empty cursor there would report a
// remainder with no way to reach it, which is the defect a resumable sweep
// exists to remove.
func EncodeSweepCursor(position SweepCursor) (string, error) {
	raw, err := json.Marshal(position)
	if err != nil {
		return "", fmt.Errorf("store: encoding the sweep position in %s: %w", position.Stream, err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeSweepCursor reads a resume position back. An empty token is the start
// of the walk, not a fault.
//
// It answers MalformedCursorError for anything this package could not have
// minted. Whether the CALLER still walks the stream named is a different
// question with a different answer — a narrowed request, or a grant lost
// between pages, is not the caller mistyping a token — so it is left to the
// provider, which knows its own vocabulary.
func DecodeSweepCursor(token string) (SweepCursor, error) {
	if token == "" {
		return SweepCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return SweepCursor{}, &MalformedCursorError{}
	}
	var position SweepCursor
	if err := json.Unmarshal(raw, &position); err != nil {
		return SweepCursor{}, &MalformedCursorError{}
	}
	if position.Stream == "" {
		return SweepCursor{}, &MalformedCursorError{}
	}
	return position, nil
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
// cannot serve. The contains-match folds apostrophes on both sides
// ("oreilly" finds "Tim O'Reilly"; f_unaccent maps the typographic ’
// to ' first, so every spelling collapses the same way). nameExpr must
// mirror the expression of the entity's *_name_trgm index so the LIKE
// stays indexed; the query text is a bind parameter (LIKE
// metacharacters at worst widen the caller's own match).
func QuickFindClause(pos int, nameExpr string) string {
	return fmt.Sprintf(`(search_tsv @@ websearch_to_tsquery('simple', f_unaccent($%[1]d))
	   OR f_fold_apostrophes(lower(%[2]s)) LIKE '%%' || f_fold_apostrophes(lower($%[1]d)) || '%%')`, pos, nameExpr)
}
