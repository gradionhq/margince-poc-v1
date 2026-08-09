// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The two halves of a human-decided LinkedIn match (founder decision,
// 2026-08-02): the candidates that need deciding, and the write that happens
// when somebody says yes.
//
// The decision itself does NOT live here. It lives in the approvals engine,
// which is the product's one place where a proposal waits for a person — this
// module supplies the facts and performs the effect, and owns neither the
// queue nor the verdict.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The decided state a match lands in, and the person_social keys the handle
// write and its audit share. Named because SQL literals and Go comparisons of
// the same string are two places for one typo to orphan a link.
const (
	matchConfirmed = "confirmed"
	socialLinkedIn = "linkedin"
	auditKeySocial = "social"
)

// PendingLinkedInMatch is one candidate a human still has to judge, in the
// terms they judge it on: the export's own spelling of the connection, and the
// contact the matcher thinks it is.
type PendingLinkedInMatch struct {
	ConnectionID      ids.UUID
	ConnectionName    string
	ConnectionCompany string
	PersonID          ids.UUID
	PersonName        string
}

// PendingLinkedInMatches lists the caller's suggested matches.
//
// `suggested` is the MATCHER's output, not a queue: it means "this pair is
// plausible and no string comparison can settle it". Turning each one into a
// proposal is the caller's job, and skipping the ones already decided is too —
// the approval row is the record of that, and this module does not read it.
func (s *Store) PendingLinkedInMatches(ctx context.Context) ([]PendingLinkedInMatch, error) {
	return s.suggestedMatches(ctx, ids.Nil)
}

// PendingLinkedInMatchesForPerson is the same list narrowed to ONE contact —
// what a caller that matched a single arrival owes a proposal pass over.
//
// The narrow read exists because the whole-network one is O(the member's open
// questions) and the caller runs once per person event: proposing every
// outstanding match again on each capture write only ever joins rows that
// already exist. Matching against one contact can raise questions about that
// contact and no other, so this is the complete answer for that caller as well
// as the cheap one.
func (s *Store) PendingLinkedInMatchesForPerson(ctx context.Context, personID ids.UUID) ([]PendingLinkedInMatch, error) {
	if personID == ids.Nil {
		// The zero id is exactly what the unfiltered read passes, so accepting
		// it here would silently widen a caller that meant to name one contact.
		return nil, errors.New("people: a person-scoped pending-match read was given no contact")
	}
	return s.suggestedMatches(ctx, personID)
}

// suggestedMatches is the one gated read both entry points land on. forPerson
// is ids.Nil for every contact.
func (s *Store) suggestedMatches(ctx context.Context, forPerson ids.UUID) ([]PendingLinkedInMatch, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return nil, apperrors.ErrPermissionDenied
	}
	// The payload names a contact, so it takes the person read grant. Row scope
	// rides the join below.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []PendingLinkedInMatch
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		ownerPos := arg(actor.UserID)
		scope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
		if err != nil {
			return err
		}
		visible := sqlAlwaysVisible
		if scope != "" {
			visible = scope
		}
		// NULL is every contact, so one query serves both entry points without a
		// second copy of the row-scope join to keep in step with this one.
		var person *ids.UUID
		if forPerson != ids.Nil {
			person = &forPerson
		}
		personPos := arg(person)
		rows, err := tx.Query(ctx, storekit.SQLf(`
			SELECT c.id, c.full_name, coalesce(c.company_name, ''), p.id, p.full_name
			  FROM linkedin_connection c
			  JOIN person p ON p.id = c.matched_person_id AND p.archived_at IS NULL AND (%s)
			 WHERE c.owner_user_id = $%d
			   AND c.match_status = 'suggested'
			   AND c.tombstoned_at IS NULL
			   AND ($%d::uuid IS NULL OR c.matched_person_id = $%d::uuid)
			 ORDER BY c.full_name, c.id`, visible, ownerPos, personPos, personPos), args...)
		if err != nil {
			return fmt.Errorf("people: reading the LinkedIn matches awaiting a decision: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var m PendingLinkedInMatch
			if err := rows.Scan(&m.ConnectionID, &m.ConnectionName, &m.ConnectionCompany,
				&m.PersonID, &m.PersonName); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// ApplyLinkedInMatch links a connection to a contact and puts the connection's
// LinkedIn address on that contact — the effect an approved proposal releases.
//
// It is the same write the automatic exact-name path performs. The difference
// is only who released it: a string comparison there, a person here.
func (s *Store) ApplyLinkedInMatch(ctx context.Context, connectionID, personID ids.UUID) error {
	// Writing to a contact takes the person update grant. The approvals engine
	// checked the decider's authority before calling this; taking it again here
	// keeps the store's own entry point gated rather than trusting a caller.
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisibleLive(ctx, tx, entityPerson, personID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE linkedin_connection
			   SET matched_person_id = $2, match_status = 'confirmed', updated_at = now()
			 WHERE id = $1 AND tombstoned_at IS NULL`, connectionID, personID)
		if err != nil {
			return fmt.Errorf("people: applying an approved LinkedIn match: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// The connection went away between the proposal and the decision —
			// a re-import that tombstoned it, or an erasure. Not found is the
			// honest answer; silently succeeding would report a link that does
			// not exist.
			return apperrors.ErrNotFound
		}
		wrote, err := writeLinkedInHandle(ctx, tx, connectionID, personID)
		if err != nil {
			return err
		}
		return auditLinkedInMatch(ctx, tx, connectionID, personID, wrote)
	})
}

// auditLinkedInMatch commits the write shape. The connection's own audit row
// records the link; a handle that reached the contact is a second mutation of a
// second entity and takes its own audit and its own person.updated, so a trace
// consumer resolves each event to an audit of the entity it describes.
func auditLinkedInMatch(ctx context.Context, tx pgx.Tx, connectionID, personID ids.UUID, wroteURL bool) error {
	auditID, err := storekit.Audit(ctx, tx, "update", "linkedin_connection", connectionID, nil,
		map[string]any{
			"match_status":        matchConfirmed,
			"matched_person_id":   personID,
			"profile_url_written": wroteURL,
		})
	if err != nil {
		return err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, connectionID,
		crmcontracts.PublicEventLinkedinMatchDecided{ProfileUrlWritten: wroteURL}); err != nil {
		return err
	}
	if !wroteURL {
		return nil
	}
	personAudit, err := storekit.Audit(ctx, tx, "update", entityPerson, personID,
		nil, map[string]any{auditKeySocial: []string{socialLinkedIn}})
	if err != nil {
		return err
	}
	return storekit.EmitEvent(ctx, tx, personAudit, personID,
		crmcontracts.PublicEventPersonUpdated{
			ChangedFields: map[string]any{auditKeySocial: []string{socialLinkedIn}},
		})
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
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $3, $2)
		ON CONFLICT (workspace_id, person_id, platform) DO NOTHING`, personID, *handle, socialLinkedIn)
	if err != nil {
		return false, fmt.Errorf("people: writing a confirmed contact's LinkedIn handle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	return true, touchPerson(ctx, tx, personID)
}

// touchPerson bumps the person row so the aggregate's version moves with its
// children.
//
// person_social is part of the person aggregate, and the ordinary update path
// bumps the row for exactly this reason. Writing a child without it leaves a
// stale If-Match token valid: a browser holding version V would overwrite the
// social set it never saw, and replacePersonSocial replaces ALL rows, so the
// handle just written would vanish with no error anywhere.
//
// The row is LOCKED before the bump rather than updated blind. Two decisions
// landing on one contact at the same instant would otherwise both read the
// pre-bump version and one increment would be lost — the same TOCTOU shape
// every by-id update in this codebase is required to close.
func touchPerson(ctx context.Context, tx pgx.Tx, personID ids.UUID) error {
	if _, err := storekit.LockRow(ctx, tx, entityPerson, personID, storekit.LiveOnly); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE person SET updated_at = now() WHERE id = $1`, personID); err != nil {
		return fmt.Errorf("people: bumping the contact a LinkedIn handle changed: %w", err)
	}
	return nil
}
