// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The mirror search: one entity type's rows, or a SWEEP across several, and
// the cursor that makes a walk with no ranking resumable all the same.

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
// The invariant it keeps is the one #586 was filed for: HasMore is true if
// and ONLY if NextCursor names somewhere to resume. A page that reports more
// and hands back no way to reach it is a page whose remainder does not exist
// as far as any caller can tell.
func (p *Provider) sweep(ctx context.Context, types []datasource.EntityType, q datasource.SearchQuery) (datasource.SearchResult, error) {
	from, inner, err := decodeSweepCursor(q.Cursor, types)
	if err != nil {
		return datasource.SearchResult{}, err
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	limit := sweepLimit(q.Limit)
	out := datasource.SearchResult{Records: []datasource.Record{}}

	for i := from; i < len(types); i++ {
		et := types[i]
		if len(types) > 1 && !p.mayRead(ctx, et) {
			inner = ""
			continue
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
			out.NextCursor, out.HasMore = encodeSweepCursor(et, next), true
			return out, nil
		}
		// It is exhausted. If the page is full and any type is left, resume at
		// the start of the next one; a full page on the LAST type is simply a
		// complete answer, and claiming more would be the same lie inverted.
		inner = ""
		if len(out.Records) >= limit && i+1 < len(types) {
			out.NextCursor, out.HasMore = encodeSweepCursor(types[i+1], ""), true
			return out, nil
		}
	}
	return out, nil
}

// mayRead reports whether the seat may read one entity type, for the sweep's
// skip-the-denied posture. A failure that is NOT a denial (a malformed
// principal) also answers false: the sweep omits what it cannot prove the
// caller may see, and never widens on an error.
func (p *Provider) mayRead(ctx context.Context, et datasource.EntityType) bool {
	return auth.Require(ctx, string(et), principal.ActionRead) == nil
}

// searchableTypes resolves the types one query walks: the ones it named, or
// every mirrored type when it named none — which is what "omit to sweep all"
// means on the tool surface. A type the mirror cannot hold is refused rather
// than silently walked past, so a caller who names `project` hears that the
// mirror has none instead of reading an empty page as an empty workspace.
func searchableTypes(named []datasource.EntityType) ([]datasource.EntityType, error) {
	if len(named) == 0 {
		return knownEntityTypes, nil
	}
	for _, et := range named {
		if !slices.Contains(knownEntityTypes, et) {
			return nil, &datasource.UnsupportedEntityError{Type: string(et)}
		}
	}
	return named, nil
}

// sweepLimit resolves the page size a sweep fills, through the same bounds
// MirrorStore.List applies per type — the page is one page whether it comes
// from one object class or five.
func sweepLimit(requested int) int {
	switch {
	case requested <= 0:
		return defaultListLimit
	case requested > maxListLimit:
		return maxListLimit
	default:
		return requested
	}
}

// sweepCursor is where a sweep stopped: the entity type being walked and that
// type's own mirror cursor within it. Both halves are needed — the type alone
// would restart it, and the mirror cursor alone would not say which object
// class it indexes into.
type sweepCursor struct {
	Type  string `json:"t"`
	Inner string `json:"c"`
}

// encodeSweepCursor renders a resume position opaquely: a caller must never
// build or edit one, and the shape inside is this package's business.
func encodeSweepCursor(et datasource.EntityType, inner string) string {
	raw, err := json.Marshal(sweepCursor{Type: string(et), Inner: inner})
	if err != nil {
		// A two-string struct cannot fail to marshal; encoding it as an empty
		// cursor rather than panicking keeps the HasMore invariant honest —
		// see sweep, which reads "" as "nowhere to resume".
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeSweepCursor resolves a cursor to the index in types it resumes at and
// the mirror cursor within that type. An empty cursor starts at the
// beginning.
//
// A cursor naming a type this query does not walk is MALFORMED, not
// ignorable: it was minted for a different sweep, and resuming a narrower
// query from a wider one's position would silently skip whatever lies between
// them.
func decodeSweepCursor(cursor string, types []datasource.EntityType) (from int, inner string, err error) {
	if cursor == "" {
		return 0, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "", &storekit.MalformedCursorError{}
	}
	var position sweepCursor
	if err := json.Unmarshal(raw, &position); err != nil {
		return 0, "", &storekit.MalformedCursorError{}
	}
	at := slices.Index(types, datasource.EntityType(position.Type))
	if at < 0 {
		return 0, "", &storekit.MalformedCursorError{}
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
