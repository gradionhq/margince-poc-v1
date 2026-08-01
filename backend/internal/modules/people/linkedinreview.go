// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Reviewing the matcher's suggestions (ADR-0078 §2.1b).
//
// The matcher has three tiers and only two of them decide anything on their
// own: an exact email match confirms itself, an ambiguous name matches nothing,
// and everything in between — the same name at the same employer — can only be
// a SUGGESTION, because a name and a company are not an identity.
//
// That middle tier is where nearly all the volume is. LinkedIn exports an
// address only for the connections who allowed it, so on a real 5,064-row
// export exactly one row carried an address and thirty-three arrived as
// suggestions. Without a way to decide them the whole tier was inert, and the
// import card said "awaiting your confirmation" about a queue that existed
// nowhere.
//
// Every read and write here is the CALLER's own. An export is a list of third
// parties who never agreed to be in anyone's CRM; a colleague working through
// somebody else's address book is precisely the disclosure §8 exists to
// prevent, and no seat — admin included — crosses that line.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fieldPersonID names the request field a validation error points at, spelled
// once so a message and the schema cannot drift apart.
const fieldPersonID = "person_id"

// The two decided states, named so the SQL literals and the Go comparisons
// cannot drift.
const (
	matchConfirmed = "confirmed"
	// socialLinkedIn is the person_social platform key. Named because the
	// write, the removal and both audit payloads say it, and four literals are
	// four places for a typo to orphan a handle.
	socialLinkedIn = "linkedin"
	// auditKeySocial names the changed-field group both the write and the
	// removal report, so a consumer reading field history sees one key.
	auditKeySocial = "social"
	matchRejected  = "rejected"
)

// LinkedInConnectionRow is one ghost as a human reviewing it needs to see it.
//
// The ORIGINAL name and company travel, never the folded forms the matcher
// compares on: somebody deciding whether "Andreas Müller · SIMIO GmbH & Co. KG"
// is their contact cannot judge "andreas muller · simio".
type LinkedInConnectionRow struct {
	ID            ids.UUID
	FullName      string
	Position      *string
	CompanyName   *string
	Email         *string
	ConnectedOn   *time.Time
	MatchStatus   string
	MatchedPerson *ids.UUID
	// MatchedPersonName is nil when the caller cannot read that contact. The
	// suggestion is then not actionable, and naming the record anyway would
	// disclose through this list exactly what the person read closes.
	MatchedPersonName *string
	MatchedOrg        *ids.UUID
	MatchedOrgName    *string
	// CreatedAt and rowID carry the keyset cursor.
	CreatedAt time.Time
}

// ListMyLinkedInConnectionsInput selects within the caller's own network.
type ListMyLinkedInConnectionsInput struct {
	// ID selects exactly one connection. It exists so a decision's response is
	// assembled by the SAME read, under the SAME scope joins, as every other
	// view of that row — a second single-row query is a second place for the
	// disclosure rules to be wrong.
	ID *ids.UUID
	// MatchStatus nil means NO filter — the house rule for a filter's one
	// no-filter input. Any value is a selection, never a default.
	MatchStatus *string
	Cursor      *string
	Limit       *int
}

// ListMyLinkedInConnections pages the caller's own connections.
//
// Tombstoned rows are excluded. They are kept in the table so a re-import
// cannot resurrect a link a human rejected — not so a human is asked about a
// connection that is no longer in their network.
func (s *Store) ListMyLinkedInConnections(ctx context.Context, in ListMyLinkedInConnectionsInput) ([]LinkedInConnectionRow, storekit.Page, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return nil, storekit.Page{}, apperrors.ErrPermissionDenied
	}
	// The payload names the contacts a suggestion points at, so it takes the
	// person read grant as well as the row scope. Scope decides WHICH records a
	// reader reaches; the object grant decides whether they read people at all,
	// and a reader denied the second must not reach the first through here.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	if in.MatchStatus != nil && !validMatchStatus(*in.MatchStatus) {
		return nil, storekit.Page{}, &DedupeInputError{
			Field: "match_status",
			Msg:   "match_status is one of unmatched, suggested, confirmed, rejected — omit it for all of them",
		}
	}
	limit := storekit.ClampLimit(in.Limit)

	var rows []LinkedInConnectionRow
	var page storekit.Page
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		rows, page, err = s.readMyConnections(ctx, tx, actor.UserID, in, limit)
		return err
	})
	if err != nil {
		return nil, storekit.Page{}, err
	}
	return rows, page, nil
}

func validMatchStatus(status string) bool {
	for _, valid := range matchRankOrder {
		if status == valid {
			return true
		}
	}
	return false
}

func (s *Store) readMyConnections(ctx context.Context, tx pgx.Tx, owner ids.UUID, in ListMyLinkedInConnectionsInput, limit int) ([]LinkedInConnectionRow, storekit.Page, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	ownerPos := arg(owner)

	where := storekit.SQLf(`c.owner_user_id = $%d AND c.tombstoned_at IS NULL`, ownerPos)
	if in.ID != nil {
		where += storekit.SQLf(` AND c.id = $%d`, arg(*in.ID))
	}
	if in.MatchStatus != nil {
		where += storekit.SQLf(` AND c.match_status = $%d`, arg(*in.MatchStatus))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		cursor, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where += storekit.SQLf(` AND (c.created_at, c.id) < ($%d, $%d)`,
			arg(cursor.CreatedAt), arg(cursor.ID))
	}

	// The matched person and organization are LEFT-joined through the caller's
	// own row-scope clauses, so a suggestion pointing at a record this caller
	// cannot read comes back with a null name rather than being hidden. Hiding
	// the row would leave a suggestion the member can neither see nor clear;
	// naming the record would disclose it. Showing the ghost without the name
	// is the only honest third answer.
	personScope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	orgScope, err := auth.ScopeClauseFor(ctx, "organization", "o", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}

	sql := storekit.SQLf(`
		SELECT c.id, c.full_name, c.position, c.company_name, c.email, c.connected_on,
		       c.match_status, c.matched_person_id, p.full_name,
		       c.matched_org_id, o.display_name, c.created_at
		  FROM linkedin_connection c
		  LEFT JOIN person p
		    ON p.id = c.matched_person_id AND p.archived_at IS NULL AND (%s)
		  LEFT JOIN organization o
		    ON o.id = c.matched_org_id AND o.archived_at IS NULL AND (%s)
		 WHERE %s
		 ORDER BY c.created_at DESC, c.id DESC
		 LIMIT $%d`,
		orClauseTrue(personScope), orClauseTrue(orgScope), where, arg(limit+1))

	queried, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, storekit.Page{}, fmt.Errorf("people: listing the caller's LinkedIn connections: %w", err)
	}
	defer queried.Close()

	var out []LinkedInConnectionRow
	for queried.Next() {
		var r LinkedInConnectionRow
		if err := queried.Scan(&r.ID, &r.FullName, &r.Position, &r.CompanyName, &r.Email,
			&r.ConnectedOn, &r.MatchStatus, &r.MatchedPerson, &r.MatchedPersonName,
			&r.MatchedOrg, &r.MatchedOrgName, &r.CreatedAt); err != nil {
			return nil, storekit.Page{}, err
		}
		// The ID goes with the name. Withholding the name while returning the
		// uuid withholds nothing that matters: the id alone proves the record
		// exists, which is exactly what the person read refuses to confirm, and
		// a client can put it in a URL. So an unresolvable name drops BOTH, and
		// the row degrades to an undecidable suggestion the member can still
		// reject.
		if r.MatchedPersonName == nil {
			r.MatchedPerson = nil
		}
		if r.MatchedOrgName == nil {
			r.MatchedOrg = nil
		}
		out = append(out, r)
	}
	if err := queried.Err(); err != nil {
		return nil, storekit.Page{}, err
	}

	var page storekit.Page
	if len(out) > limit {
		out = out[:limit]
		page.HasMore = true
		last := out[len(out)-1]
		page.NextCursor = storekit.EncodeCursor(last.CreatedAt, last.ID)
	}
	return out, page, nil
}

// orClauseTrue turns an empty scope clause into an always-true predicate. An
// unbounded caller's clause is the empty string, and interpolating that into a
// join condition would be a syntax error rather than the "sees everything" it
// means.
func orClauseTrue(clause string) string {
	if clause == "" {
		return sqlAlwaysVisible
	}
	return clause
}
