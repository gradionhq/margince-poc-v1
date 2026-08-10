// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The mirror search: one entity type's rows, or a SWEEP across several, and
// the cursor that makes a walk with no ranking resumable all the same.

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Search pages the mirror rows of one entity type — or SWEEPS several in a
// fixed order — visibility-joined via MirrorStore.List, applying q.Text as a
// naive case-insensitive substring filter over each row's string-valued
// fields (a branch-1 scope limit, not the FTS/RRF hybrid search's own
// retrieval path).
//
// The sweep exists because the tool surface advertises it: search_records
// says `record_type` may be omitted "to sweep all five", and search over an
// omitted type is exactly what the native provider answers. A mode that
// refused it made the same tool behave differently on an overlay workspace,
// which is the silent break AC-OV-2 forbids.
//
// It is walked type by type rather than ranked across them, because the
// mirror holds opaque incumbent rows and has no score to order them by. What
// makes that pageable anyway is sweepCursor: the position IS the type plus
// that type's own mirror cursor, so a caller resumes where the page stopped
// instead of being told there is more with no way to reach it.
func (p *Provider) Search(ctx context.Context, q datasource.SearchQuery) (datasource.SearchResult, error) {
	if p.ms == nil {
		return datasource.SearchResult{}, errNoMirrorStore()
	}
	types, err := searchableTypes(q.EntityTypes)
	if err != nil {
		return datasource.SearchResult{}, err
	}
	// Object RBAC before any mirror read — the MCP search_records path
	// reaches the provider directly, so the gate the REST search shadow
	// applies must also live here (see Read's rationale).
	//
	// One NAMED type answers the denial; a sweep omits the types the seat may
	// not read and answers the rest, which is the posture ListObjects already
	// takes and the only one that lets a partly-granted seat search at all.
	if len(types) == 1 {
		if err := auth.Require(ctx, string(types[0]), principal.ActionRead); err != nil {
			return datasource.SearchResult{}, err
		}
	}
	// A structured filter the mirror cannot evaluate is REFUSED, never dropped.
	// The mirror holds the incumbent's rows as opaque fields, so a narrowing by
	// owner or stage has nothing to bind to — and answering the unnarrowed page
	// instead would return a superset of what was asked for, in the shape of the
	// right answer. That is the silent break AC-OV-2 forbids: a tool either
	// behaves identically across modes or declares it cannot serve this one.
	//
	// It lands AFTER the object gate on purpose. A caller with no read grant
	// must hear the same thing whether or not they attached a filter; refusing
	// first would let an unauthorized caller learn this workspace's
	// system-of-record mode by adding one.
	if len(q.Filters) > 0 {
		return datasource.SearchResult{}, apperrors.ErrUnsupportedBySoR
	}
	return p.sweep(ctx, types, q)
}

// sweep walks types in order from the cursor's position, filling one page.
//
// The invariant it keeps: HasMore is true if and ONLY if NextCursor names
// somewhere to resume. A page that reports more and hands back no way to
// reach it is a page whose remainder does not exist as far as any caller can
// tell.
//
// HasMore means the WALK has not reached the end of the mirrored rows — not
// that another row will match. The mirror pages before the text filter runs,
// so a page whose rows are all filtered out still reports more and hands back
// the position to continue from, and a walk's last page can be empty. That is
// the reading every single-type overlay list already gives (MirrorStore.List
// answers a cursor whenever a batch filled); the alternative — scanning
// forward until a match turns up — is a request whose cost is decided by how
// rare the caller's word is.
func (p *Provider) sweep(ctx context.Context, types []datasource.EntityType, q datasource.SearchQuery) (datasource.SearchResult, error) {
	resumeAt, inner, err := resumeStream(q.Cursor)
	if err != nil {
		return datasource.SearchResult{}, err
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	// The page is one page whether it comes from one object class or five, so
	// it is bounded once here rather than per type.
	limit := clampListLimit(q.Limit)
	out := datasource.SearchResult{Records: []datasource.Record{}}

	for i := resumePosition(types, resumeAt); i < len(types); i++ {
		et := types[i]
		if len(types) > 1 {
			admitted, err := p.mayRead(ctx, et)
			if err != nil {
				return datasource.SearchResult{}, err
			}
			if !admitted {
				inner = ""
				continue
			}
		}
		if et != resumeAt {
			// The stream the cursor was minted in is not this one — it was
			// narrowed away, or the seat lost it. This type starts at ITS
			// beginning rather than inheriting a position from another.
			inner = ""
		}
		rows, next, err := p.ms.List(ctx, string(et), inner, limit-len(out.Records))
		if err != nil {
			return datasource.SearchResult{}, err
		}
		for _, row := range rows {
			if text != "" && !mirrorRowMatchesText(row, text) {
				continue
			}
			rec, err := recordFromRow(et, row)
			if err != nil {
				return datasource.SearchResult{}, err
			}
			out.Records = append(out.Records, rec)
		}
		// This type still has rows the page did not reach: resume INSIDE it.
		if next != "" {
			return withResumePosition(out, et, next)
		}
		// It is exhausted. If the page is full and any type is left, resume at
		// the start of the next one; a full page on the LAST type is simply a
		// complete answer, and claiming more would be the same lie inverted.
		inner = ""
		if len(out.Records) >= limit && i+1 < len(types) {
			return withResumePosition(out, types[i+1], "")
		}
	}
	return out, nil
}

// mayRead reports whether the seat may read one entity type, for the sweep's
// skip-the-denied posture.
//
// A DENIAL and a failure are different answers. Only the first is a fact about
// the caller's grants; the second — no principal bound, a malformed one — is
// this server not working, and reading it as "may not see it" would answer a
// broken request chain with five skipped types and a 200 saying the workspace
// holds nothing. ListObjects draws the same line for the same reason.
func (p *Provider) mayRead(ctx context.Context, et datasource.EntityType) (bool, error) {
	err := auth.Require(ctx, string(et), principal.ActionRead)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, apperrors.ErrPermissionDenied):
		return false, nil
	default:
		return false, err
	}
}

// MirroredEntityTypes is the order a sweep walks the mirror's object classes
// in — fixed, so a capped page is deterministic, and EXPORTED because compose
// answers "can this mode serve that type?" on the REST door and must answer it
// from the same list the provider walks. Two lists would drift the moment a
// sixth class is mirrored, and the shape of that drift is a type MCP serves
// and REST refuses.
func MirroredEntityTypes() []datasource.EntityType {
	return slices.Clone(knownEntityTypes)
}

// searchableTypes resolves the types one query walks: the ones it named, or
// every mirrored type when it named none — which is what "omit to sweep all"
// means on the tool surface. A type the mirror cannot hold is refused rather
// than silently walked past, so a caller who names `project` hears that the
// mirror has none instead of reading an empty page as an empty workspace.
//
// The answer is always in MIRROR order and always without repeats, whatever
// order the caller listed them in and however many times. The contract's
// `types` is a plain array with no uniqueness rule, and `types=person,person`
// walked literally would read that stream twice — serving every person again,
// since a cursor names a type rather than one of its two appearances.
func searchableTypes(named []datasource.EntityType) ([]datasource.EntityType, error) {
	if len(named) == 0 {
		return knownEntityTypes, nil
	}
	asked := make(map[datasource.EntityType]bool, len(named))
	for _, et := range named {
		if !slices.Contains(knownEntityTypes, et) {
			return nil, &datasource.UnsupportedEntityError{Type: string(et)}
		}
		asked[et] = true
	}
	walk := make([]datasource.EntityType, 0, len(asked))
	for _, et := range knownEntityTypes {
		if asked[et] {
			walk = append(walk, et)
		}
	}
	return walk, nil
}

// resumePosition is where in this query's walk a cursor's type resumes.
//
// The cursor names a position in the MIRROR's fixed type order rather than an
// index into one request's slice, so the same token still means the same place
// when the request's own type set is not the one that minted it — a caller who
// narrowed `types` mid-walk, or a seat that lost a grant between pages. A
// position this query no longer walks resumes at the next type PAST it: the
// rows in between belong to a stream this request is not reading, and the
// contract already says changing a filter mid-walk changes what the remaining
// pages see. Answering 422 there would call an authorization change a
// malformed input.
func resumePosition(walk []datasource.EntityType, resumeAt datasource.EntityType) int {
	if resumeAt == "" {
		return 0
	}
	at := slices.Index(knownEntityTypes, resumeAt)
	for i, et := range walk {
		if slices.Index(knownEntityTypes, et) >= at {
			return i
		}
	}
	return len(walk)
}

// withResumePosition finishes a page that stopped short of the walk's end:
// the position to continue from, and the flag that says one exists. They are
// set together and only together, which is the whole of the invariant.
func withResumePosition(out datasource.SearchResult, et datasource.EntityType, inner string) (datasource.SearchResult, error) {
	cursor, err := storekit.EncodeSweepCursor(storekit.SweepCursor{Stream: string(et), Inner: inner})
	if err != nil {
		return datasource.SearchResult{}, err
	}
	out.NextCursor, out.HasMore = cursor, true
	return out, nil
}

// resumeStream reads the position a cursor names, refusing one this package
// could not have minted: a token whose stream is an object class the mirror
// does not hold. Whether THIS request still walks that stream is
// resumePosition's question, not a fault in the token.
func resumeStream(cursor string) (datasource.EntityType, string, error) {
	position, err := storekit.DecodeSweepCursor(cursor)
	if err != nil {
		return "", "", err
	}
	if position.Stream == "" {
		return "", "", nil
	}
	at := datasource.EntityType(position.Stream)
	if !slices.Contains(knownEntityTypes, at) {
		return "", "", &storekit.MalformedCursorError{}
	}
	return at, position.Inner, nil
}

// mirrorRowMatchesText reports whether any string-valued field of row
// contains lowerText.
func mirrorRowMatchesText(row Row, lowerText string) bool {
	for _, v := range row.Fields {
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), lowerText) {
			return true
		}
	}
	return false
}
