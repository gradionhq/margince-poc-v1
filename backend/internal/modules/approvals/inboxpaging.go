// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// How an inbox read walks the table: where a scan resumes, and how a filled
// page becomes the has_more/next_cursor pair a caller continues with. One
// keyset vocabulary, so the inbox, a target-scoped read and a bundle read can
// never disagree about what "there is more" means.

package approvals

import (
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// keysetStart is where a scan resumes: the (created_at, id) of the last row the
// caller has already been shown. Nil starts at the newest row.
type keysetStart struct {
	createdAt time.Time
	id        ids.ApprovalID
}

// after is the resume point that follows one row.
func after(a row) *keysetStart { return &keysetStart{createdAt: a.CreatedAt, id: a.ID} }

// startOf is where this read begins: the caller's token, or the newest row
// when they sent none.
func startOf(token string) (*keysetStart, error) {
	if token == "" {
		return nil, nil //nolint:nilnil // no token is not a resume point: the scan starts at the newest row, which is what a nil start means throughout this file
	}
	start, err := decodeStart(token)
	if err != nil {
		return nil, err
	}
	return &start, nil
}

// decodeStart reads ONE page token, which the caller has already established
// is present — an absent token is not a resume point to decode, it is the
// newest row, and the caller expresses that by not calling this.
//
// The token is client input, so one that does not decode is a client fault: it
// travels as storekit's MalformedCursorError and the transport answers the same
// 422 every other list endpoint answers a bad cursor with.
//
// A token that decodes to a ZERO resume point is that same fault, and has to be
// caught HERE: storekit's decode is a JSON unmarshal, so an encoded `{}` parses
// happily into an empty Cursor. Carried into the query it reads as "everything
// before the beginning of time" — a successful, permanently empty page. A
// client paging on that loses every row it had not yet seen and is told
// nothing, which is the one outcome a page token must never produce.
func decodeStart(token string) (keysetStart, error) {
	c, err := storekit.DecodeCursor(token)
	if err != nil {
		return keysetStart{}, err
	}
	if c.CreatedAt.IsZero() || c.ID.IsZero() {
		return keysetStart{}, &storekit.MalformedCursorError{}
	}
	return keysetStart{createdAt: c.CreatedAt, id: ids.From[ids.ApprovalKind](c.ID)}, nil
}

// capPage cuts a filled-one-past result back to the display limit and derives
// the page from ONE resume point, so has_more and next_cursor can never
// disagree about whether there is a next page.
//
// scanned is the other reason there may be more: a read whose scan hit its own
// cap has not seen the whole backlog either. It carries the row that scan
// stopped on rather than a flag, because a page that returned nothing decidable
// still has to hand back a token — otherwise a caller is told there is more and
// given no way to reach it.
func capPage(out []row, limit int, scanned *row) ([]row, storekit.Page) {
	if len(out) > limit {
		out = out[:limit]
		return out, pageAfter(out[limit-1])
	}
	if scanned != nil {
		return out, pageAfter(*scanned)
	}
	return out, storekit.Page{}
}

// pageAfter is the page a caller continues with: has_more, and the keyset token
// the next request resumes from.
func pageAfter(last row) storekit.Page {
	return storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, last.ID.UUID)}
}
