// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Deciding one suggestion (ADR-0078 §2.1b) — the write half of the review
// surface, split from the read half so each file holds one concept.
//
// A decision is a HUMAN's, and it is the only route by which a name-and-employer
// guess becomes a link the product relies on. It is also the only place a ghost
// contributes anything to a real record: the connection's own LinkedIn address,
// and nothing else.

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
		// Re-confirming onto a DIFFERENT contact must take the handle off the
		// one it was on. Without this a corrected mistake leaves the wrong
		// person carrying a LinkedIn address that is not theirs, and nothing
		// ever removes it.
		if ghost.MatchedPerson != nil && *ghost.MatchedPerson != target &&
			ghost.MatchStatus == matchConfirmed {
			if err := clearLinkedInHandle(ctx, tx, id, *ghost.MatchedPerson); err != nil {
				return err
			}
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
		return auditMatchDecision(ctx, tx, id, target, matchConfirmed, out.ProfileURLWritten)
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
	// A rejection changes stored state, so it takes the same write grant the
	// confirmation does. Without it a read_only role could permanently alter
	// the database through one of the two sibling verbs, which is the kind of
	// disagreement between paths that makes a role's contract meaningless.
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return LinkedInMatchDecision{}, err
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		ghost, err := lockOwnConnection(ctx, tx, actor.UserID, id)
		if err != nil {
			return err
		}
		// A rejection of a CONFIRMED match takes the handle back off the
		// contact. The member is saying the link was wrong, and leaving the
		// address behind would keep the wrong claim on the record.
		if ghost.MatchedPerson != nil && ghost.MatchStatus == matchConfirmed {
			if err := clearLinkedInHandle(ctx, tx, id, *ghost.MatchedPerson); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE linkedin_connection
			   SET matched_person_id = NULL, match_status = 'rejected', updated_at = now()
			 WHERE id = $1`, id); err != nil {
			return fmt.Errorf("people: rejecting a LinkedIn match: %w", err)
		}
		return auditMatchDecision(ctx, tx, id, ids.Nil, matchRejected, false)
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
	rows, _, err := s.ListMyLinkedInConnections(ctx, ListMyLinkedInConnectionsInput{ID: &id})
	if err != nil {
		return LinkedInMatchDecision{}, err
	}
	if len(rows) == 0 {
		// The row was decided a moment ago inside a committed transaction and
		// the list is owner-scoped to the same caller, so an empty result means
		// it has since been tombstoned or erased. Not found is the honest
		// answer; inventing a payload would report a decision on a row that is
		// no longer there.
		return LinkedInMatchDecision{}, apperrors.ErrNotFound
	}
	return LinkedInMatchDecision{Connection: rows[0], ProfileURLWritten: wroteURL}, nil
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
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	return true, touchPerson(ctx, tx, personID)
}

// clearLinkedInHandle removes the handle THIS connection put on a contact, when
// the member takes the link back.
//
// Matched on the handle value, not merely on the platform: a contact may carry
// a LinkedIn address somebody typed in by hand, and a rejection here is a
// statement about this connection, not licence to delete a field the member
// never touched.
func clearLinkedInHandle(ctx context.Context, tx pgx.Tx, connectionID, personID ids.UUID) error {
	var handle *string
	if err := tx.QueryRow(ctx,
		`SELECT profile_url FROM linkedin_connection WHERE id = $1`, connectionID).Scan(&handle); err != nil {
		return fmt.Errorf("people: reading a connection's profile URL: %w", err)
	}
	if handle == nil || *handle == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM person_social
		 WHERE person_id = $1 AND platform = 'linkedin' AND handle = $2`, personID, *handle)
	if err != nil {
		return fmt.Errorf("people: removing a withdrawn LinkedIn handle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return touchPerson(ctx, tx, personID)
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

// auditMatchDecision commits the write shape for one decision.
//
// EVERY decision emits, including a rejection. A rejection changes stored state
// — the link is cleared and the refusal is durable — and a mutation with an
// audit row but no outbox row is a mutation the bus never sees, which is how a
// projection silently stops tracking reality.
//
// The event names NEITHER the connection nor the contact. A ghost is a third
// party who never consented to being in this CRM, and publishing the pair
// through the outbox would defeat the invisibility every other surface
// maintains. A subscriber that needs the records reads the audit row the
// envelope links to, under its own authority.
//
// When a handle reached the contact, that is a SECOND mutation of a second
// entity, so it takes its own audit and its own person.updated. Linking a
// person event to an audit of the LinkedIn connection — which is what this used
// to do — hands every trace consumer an audit row with no person image in it.
func auditMatchDecision(ctx context.Context, tx pgx.Tx, id, personID ids.UUID, verdict string, wroteURL bool) error {
	auditID, err := storekit.Audit(ctx, tx, "update", "linkedin_connection", id, nil, map[string]any{
		"match_status":        verdict,
		"matched_person_id":   personID,
		"profile_url_written": wroteURL,
	})
	if err != nil {
		return err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id,
		crmcontracts.PublicEventLinkedinMatchDecided{
			Verdict:           crmcontracts.PublicEventLinkedinMatchDecidedVerdict(verdict),
			ProfileUrlWritten: wroteURL,
		}); err != nil {
		return err
	}
	if !wroteURL {
		return nil
	}
	personAudit, err := storekit.Audit(ctx, tx, "update", entityPerson, personID,
		nil, map[string]any{"social": []string{"linkedin"}})
	if err != nil {
		return err
	}
	return storekit.EmitEvent(ctx, tx, personAudit, personID,
		crmcontracts.PublicEventPersonUpdated{
			ChangedFields: map[string]any{"social": []string{"linkedin"}},
		})
}
