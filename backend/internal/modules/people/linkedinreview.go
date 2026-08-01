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
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

// LinkedInMatchDecision is one review decision's outcome.
type LinkedInMatchDecision struct {
	Connection LinkedInConnectionRow
	// ProfileURLWritten says whether the contact gained a LinkedIn handle. It
	// is reported rather than assumed because the two false cases are ones a
	// member would otherwise wonder about: the contact already carried a handle
	// (never overwritten), or this connection has no profile URL to write.
	ProfileURLWritten bool
}

// ConfirmLinkedInMatch links a connection to a contact, on a human's word.
//
// personID zero accepts the matcher's own suggestion; a value overrides it,
// which is how a wrong guess is corrected rather than rejected outright and the
// connection lost.
func (s *Store) ConfirmLinkedInMatch(ctx context.Context, id ids.UUID, personID ids.UUID) (LinkedInMatchDecision, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return LinkedInMatchDecision{}, apperrors.ErrPermissionDenied
	}
	// Confirming WRITES to a contact, so it takes the person update grant —
	// not merely the read grant the list takes. A member who may not edit
	// people must not be able to stamp a LinkedIn handle onto one.
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return LinkedInMatchDecision{}, err
	}

	var out LinkedInMatchDecision
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		ghost, err := lockOwnConnection(ctx, tx, actor.UserID, id)
		if err != nil {
			return err
		}
		target := personID
		if target == ids.Nil {
			if ghost.MatchedPerson == nil {
				return &DedupeInputError{
					Field: fieldPersonID,
					Msg:   "this connection has no suggested contact — name the one it is, or leave it unmatched",
				}
			}
			target = *ghost.MatchedPerson
		}
		// The contact must be one this caller can actually reach. Without this
		// probe a member could confirm against any id they guessed and learn
		// from the outcome whether it exists.
		if err := auth.EnsureVisibleLive(ctx, tx, "person", target); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE linkedin_connection
			   SET matched_person_id = $2, match_status = 'confirmed', updated_at = now()
			 WHERE id = $1`, id, target); err != nil {
			return fmt.Errorf("people: confirming a LinkedIn match: %w", err)
		}
		out.ProfileURLWritten, err = writeLinkedInHandle(ctx, tx, id, target)
		if err != nil {
			return err
		}
		return auditMatchDecision(ctx, tx, id, target, "confirmed", out.ProfileURLWritten)
	})
	if err != nil {
		return LinkedInMatchDecision{}, err
	}
	return s.decisionOutcome(ctx, id, out.ProfileURLWritten)
}

// RejectLinkedInMatch records that the suggested contact is the wrong person.
//
// It clears the person link and leaves matched_org_id alone: the account claim
// is separate and weaker — this connection works at this company — and a wrong
// person does not make the employer wrong.
func (s *Store) RejectLinkedInMatch(ctx context.Context, id ids.UUID) (LinkedInMatchDecision, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return LinkedInMatchDecision{}, apperrors.ErrPermissionDenied
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockOwnConnection(ctx, tx, actor.UserID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE linkedin_connection
			   SET matched_person_id = NULL, match_status = 'rejected', updated_at = now()
			 WHERE id = $1`, id); err != nil {
			return fmt.Errorf("people: rejecting a LinkedIn match: %w", err)
		}
		return auditMatchDecision(ctx, tx, id, ids.Nil, "rejected", false)
	})
	if err != nil {
		return LinkedInMatchDecision{}, err
	}
	return s.decisionOutcome(ctx, id, false)
}

// decisionOutcome re-reads the decided row through the ordinary list path, so
// the response a caller gets is assembled by the same code and under the same
// scopes as every other view of it.
func (s *Store) decisionOutcome(ctx context.Context, id ids.UUID, wroteURL bool) (LinkedInMatchDecision, error) {
	rows, _, err := s.ListMyLinkedInConnections(ctx, ListMyLinkedInConnectionsInput{})
	if err != nil {
		return LinkedInMatchDecision{}, err
	}
	for _, r := range rows {
		if r.ID == id {
			return LinkedInMatchDecision{Connection: r, ProfileURLWritten: wroteURL}, nil
		}
	}
	// The row was decided a moment ago inside a committed transaction, so it
	// exists; it simply fell past the first page. Re-read it directly rather
	// than paging, which would be a second, slower spelling of "find one row".
	return s.oneConnection(ctx, id, wroteURL)
}

func (s *Store) oneConnection(ctx context.Context, id ids.UUID, wroteURL bool) (LinkedInMatchDecision, error) {
	actor, _ := principal.Actor(ctx)
	var r LinkedInConnectionRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT c.id, c.full_name, c.position, c.company_name, c.email, c.connected_on,
			       c.match_status, c.matched_person_id, c.matched_org_id, c.created_at
			  FROM linkedin_connection c
			 WHERE c.id = $1 AND c.owner_user_id = $2`, id, actor.UserID).
			Scan(&r.ID, &r.FullName, &r.Position, &r.CompanyName, &r.Email, &r.ConnectedOn,
				&r.MatchStatus, &r.MatchedPerson, &r.MatchedOrg, &r.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return LinkedInMatchDecision{}, apperrors.ErrNotFound
	}
	if err != nil {
		return LinkedInMatchDecision{}, fmt.Errorf("people: reading a decided LinkedIn connection: %w", err)
	}
	return LinkedInMatchDecision{Connection: r, ProfileURLWritten: wroteURL}, nil
}

// lockOwnConnection reads and locks one of the CALLER's live connections.
//
// Owner-scoped in the WHERE clause, so another member's connection is not
// merely refused — it is indistinguishable from one that does not exist, which
// is the answer that discloses nothing about whose network holds whom.
func lockOwnConnection(ctx context.Context, tx pgx.Tx, owner, id ids.UUID) (LinkedInConnectionRow, error) {
	var r LinkedInConnectionRow
	err := tx.QueryRow(ctx, `
		SELECT id, full_name, match_status, matched_person_id
		  FROM linkedin_connection
		 WHERE id = $1 AND owner_user_id = $2 AND tombstoned_at IS NULL
		 FOR UPDATE`, id, owner).
		Scan(&r.ID, &r.FullName, &r.MatchStatus, &r.MatchedPerson)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperrors.ErrNotFound
	}
	if err != nil {
		return r, fmt.Errorf("people: reading the connection being decided: %w", err)
	}
	return r, nil
}

// writeLinkedInHandle stamps the member's LinkedIn profile URL onto the contact
// they just confirmed a connection to.
//
// This is the ONE thing a ghost contributes to a real record, and it is
// deliberately narrow: the URL and nothing else. The ghost's name, employer,
// position and connection date stay where they are — the export is a third
// party's data, and copying it onto a contact would be the consent problem the
// whole ghost design exists to avoid.
//
// The handle written is the CONNECTION's own profile URL — the `URL` column
// Connections.csv has always carried. NOT the member's own profile URL: that
// one belongs to the member, and stamping it on every contact they confirm
// would put the wrong person's address on the record.
//
// A connection imported before migration 0161 has no URL and writes nothing.
// The confirmation still stands; only the copy is unavailable, and the caller
// is told so rather than left to wonder.
//
// ON CONFLICT DO NOTHING: a handle already on the record is somebody's
// statement, and confirming a match is not grounds to replace it. The caller is
// told which happened rather than left to guess.
func writeLinkedInHandle(ctx context.Context, tx pgx.Tx, connectionID, personID ids.UUID) (bool, error) {
	var handle *string
	err := tx.QueryRow(ctx,
		`SELECT profile_url FROM linkedin_connection WHERE id = $1`, connectionID).Scan(&handle)
	if err != nil {
		return false, fmt.Errorf("people: reading a connection's profile URL: %w", err)
	}
	if handle == nil || *handle == "" {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO person_social (workspace_id, person_id, platform, handle)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'linkedin', $2)
		ON CONFLICT (workspace_id, person_id, platform) DO NOTHING`, personID, *handle)
	if err != nil {
		return false, fmt.Errorf("people: writing a confirmed contact's LinkedIn handle: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// auditMatchDecision commits the write shape for one decision.
//
// The audit row names the CONNECTION and the person, because "who decided that
// this ghost is that contact, and when" is the question somebody will ask when
// a link looks wrong. The event is a person.updated: from a subscriber's point
// of view a contact gained a social handle, and no ghost detail crosses the
// outbox — publishing a third party's name through the bus would defeat the
// invisibility the ghosts exist to keep.
func auditMatchDecision(ctx context.Context, tx pgx.Tx, id, personID ids.UUID, verdict string, wroteURL bool) error {
	auditID, err := storekit.Audit(ctx, tx, "update", "linkedin_connection", id, nil, map[string]any{
		"match_status":        verdict,
		"matched_person_id":   personID,
		"profile_url_written": wroteURL,
	})
	if err != nil {
		return err
	}
	if personID == ids.Nil || !wroteURL {
		// A rejection changes no contact, and a confirmation that wrote nothing
		// left the contact byte-identical. Emitting person.updated for either
		// would tell every subscriber to re-read a record that did not change.
		return nil
	}
	return storekit.EmitEvent(ctx, tx, auditID, personID, crmcontracts.PublicEventPersonUpdated{
		ChangedFields: map[string]any{"social": []string{"linkedin"}},
	})
}
