// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Matching LinkedIn ghosts to CRM records (ADR-0078 §2.1b).
//
// A ghost is a name, maybe a company, and — on CSV rows where the connection
// allowed it — an address. Turning that into "this colleague knows THIS
// contact" is a dedupe problem, and it obeys the same rule the rest of this
// module obeys: **only an email address is an exact person key.**
//
// So there are exactly two outcomes and one of them needs a human:
//
//	EXACT EMAIL      → confirmed automatically. An address is identity here,
//	                   the same way it is on the capture path.
//	NAME + EMPLOYER  → suggested. It agrees often enough to be worth showing
//	                   and wrongly often enough that auto-confirming would
//	                   quietly attach a stranger to a customer record. There
//	                   are two Andreas Müllers at every large German firm.
//
// Nothing here ever CREATES a person. A ghost that matches nothing stays a
// ghost, and its only contribution is the org-level count — "someone here is
// connected to 3 people at this account" — which needs no identity at all.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LinkedInMatchResult reports what one matching pass decided.
type LinkedInMatchResult struct {
	Confirmed int
	Suggested int
}

// MatchLinkedInConnections runs the matcher over one OWNER's unmatched ghosts
// and reports what it decided.
//
// Scoped to the owner because the caller is an upload reporting its own
// result: a workspace-wide sweep would count a colleague's older unmatched
// ghosts as this upload's confirmations, so the number on the screen would
// describe work the person did not just do. A zero owner means every ghost —
// the shape a scheduled sweep wants, and the only caller allowed to say
// "workspace-wide".
//
// It is safe to re-run: a ghost a human has already confirmed or rejected is
// never revisited, so a nightly pass cannot overturn a person's decision, and
// a rejection is permanent rather than something the next import forgets.
func (s *Store) MatchLinkedInConnections(ctx context.Context, owner ids.UUID) (LinkedInMatchResult, error) {
	// READ, not update. The matcher writes only to the caller's own ghost rows;
	// it never touches a person, and the person grant it does need is the one
	// that says which contacts this member may be shown. Demanding update also
	// broke the per-owner sweep for any member whose role reads people without
	// editing them — their network would silently never be matched.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return LinkedInMatchResult{}, err
	}
	var out LinkedInMatchResult
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		confirmed, err := matchGhostsByEmail(ctx, tx, owner, ids.Nil)
		if err != nil {
			return err
		}
		out.Confirmed = confirmed
		// Accounts first: the name+employer suggestion below reads
		// matched_org_id, so resolving employers afterwards would leave every
		// first-pass suggestion unmade until the next run.
		if err := matchGhostOrganizations(ctx, tx); err != nil {
			return err
		}
		suggested, err := suggestGhostsByNameAndEmployer(ctx, tx, owner, ids.Nil)
		if err != nil {
			return err
		}
		out.Suggested = suggested
		return nil
	})
	return out, err
}

// MatchLinkedInConnectionsForPerson matches the unmatched ghosts against ONE
// contact, and it is the path that matters most in practice.
//
// A workspace does not learn its contacts all at once. An export is uploaded
// during onboarding, and the people it could match are created over the
// following hours and weeks — by mail capture, by a site read, by a rep typing
// a name in. Every one of those is a chance to attach a ghost that the upload
// could not have attached, and asking each writer to remember to call the
// matcher would guarantee that one of them forgets.
//
// So the trigger is the EVENT every writer already emits. person.created and
// person.updated flow through the outbox because the write shape puts them
// there, and the cg:graph-edge consumer turns them into this call. Manual
// entry, capture, site read, merge and import all reach it without any of them
// knowing this function exists.
//
// Scoped to the one person so the cost is proportional to the change: a
// workspace-wide pass per person event would re-scan every unmatched ghost
// thousands of times during a capture backfill.
//
// owner narrows the email/name-and-employer arms to that ONE member's
// ghosts — the same narrowing MatchLinkedInConnections applies via its own
// owner parameter. The caller (compose/linkedinmatchgen.go's matchPerson)
// already runs this once per ghost owner under that owner's real principal
// precisely so a match is decided by the ghost owner's authority (see
// ghostOwnerCapturePrivacy's doc above); passing ids.Nil here — SQL NULL,
// "every owner" — would match every member's ghosts under whichever
// member's row scope the loop currently binds, turning a guessed address
// uploaded as an owner-private ghost into a contact-existence oracle: wait
// for a broader-scoped colleague's pass to reach it, then read
// match_status to learn a contact you cannot see exists.
func (s *Store) MatchLinkedInConnectionsForPerson(ctx context.Context, owner, person ids.UUID) (LinkedInMatchResult, error) {
	// READ, not update. The matcher writes only to the caller's own ghost rows;
	// it never touches a person, and the person grant it does need is the one
	// that says which contacts this member may be shown. Demanding update also
	// broke the per-owner sweep for any member whose role reads people without
	// editing them — their network would silently never be matched.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return LinkedInMatchResult{}, err
	}
	var out LinkedInMatchResult
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		confirmed, err := matchGhostsByEmail(ctx, tx, owner, person)
		if err != nil {
			return err
		}
		out.Confirmed = confirmed
		if err := matchGhostOrganizations(ctx, tx); err != nil {
			return err
		}
		suggested, err := suggestGhostsByNameAndEmployer(ctx, tx, owner, person)
		out.Suggested = suggested
		return err
	})
	return out, err
}

// ghostOwnerCapturePrivacy is the capture-privacy arm of the boundary, carried
// on the match itself.
//
// It is a property of the ROW — visibility='owner' means the importing member
// alone, not even an admin — so it cannot come from the caller's scope clause
// and has to be rendered against the GHOST's owner. Row scope is the other arm
// and is a property of the READER; it arrives through auth.ScopeClauseFor,
// which is why the background sweep must run under each owner's real principal
// (compose/linkedinmatchgen.go, compose/linkedinrematch.go). A system principal
// is unbounded by design, so a sweep that ran as one would have neither arm:
// upload a guessed address, wait, and read match_status to learn whether a
// contact you cannot see exists.
const ghostOwnerCapturePrivacy = `(p.visibility <> 'owner' OR p.owner_id = g.owner_user_id)`

// matchGhostsByEmail confirms the ghosts whose address is already a known
// contact's address. This is the one automatic confirmation, and it is
// automatic for the same reason capture's dedupe is: an address identifies a
// person, and treating it as a suggestion would ask a human to re-confirm a
// fact the system is already certain of everywhere else.
func matchGhostsByEmail(ctx context.Context, tx pgx.Tx, owner, onlyPerson ids.UUID) (int, error) {
	// The person row scope, on the MATCH itself. Without it the matcher links
	// a ghost to a contact the uploader cannot see — and then reports a
	// confirmed count, which turns a one-row CSV into an oracle: upload a
	// guessed address, read the number, learn whether an owner-private
	// captured contact with that address exists.
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	ownerPos := arg(nullableOwner(owner))
	// The same nullable-parameter shape as the owner filter: a zero id means
	// "every candidate", so one query serves both the sweep and the per-person
	// call rather than two spellings of the same match drifting apart.
	personPos := arg(nullableOwner(onlyPerson))
	visible, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return 0, err
	}
	if visible == "" {
		visible = sqlAlwaysVisible
	}
	tag, err := tx.Exec(ctx, fmt.Sprintf(`
		UPDATE linkedin_connection g
		   SET matched_person_id = pe.person_id,
		       match_status = 'confirmed',
		       updated_at = now()
		  FROM person_email pe
		  JOIN person p ON p.id = pe.person_id AND p.archived_at IS NULL
		 WHERE g.email IS NOT NULL
		   AND lower(pe.email) = g.email
		   AND g.tombstoned_at IS NULL
		   -- Only an undecided ghost. A human's confirm or reject stands.
		   AND g.match_status = 'unmatched'
		   AND ($%[1]d::uuid IS NULL OR g.owner_user_id = $%[1]d)
		   AND ($%[3]d::uuid IS NULL OR p.id = $%[3]d)
		   AND `+ghostOwnerCapturePrivacy+`
		   AND (%[2]s)`, ownerPos, visible, personPos), args...)
	if err != nil {
		return 0, fmt.Errorf("people: matching LinkedIn connections by address: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// suggestNameEmployerMatchSQL renders the proposal in one statement, over three
// parameter positions: the owner filter, the person row scope, and the
// single-person narrowing.
const suggestNameEmployerMatchSQL = `
		WITH pair AS (
		    -- DISTINCT pairs FIRST. A contact with two live employment rows at
		    -- one account (a role change recorded as a second row) joins twice
		    -- and is still one candidate; counting the join rows would read
		    -- that as an ambiguity and refuse a correct suggestion.
		    SELECT DISTINCT g.id AS ghost_id, p.id AS person_id,
		           -- Whether the names agree EXACTLY, before folding. The fold
		           -- is what finds the candidate; this is what decides whether
		           -- a human still has to look at it.
		           g.full_name = p.full_name AS exact_name
		      FROM linkedin_connection g
		      JOIN person p
		        ON p.archived_at IS NULL
		       -- f_unaccent + lower is the DATABASE's approximation of the Go
		       -- normalizer that produced normalized_name. It narrows
		       -- candidates, and the outcome is a SUGGESTION a human confirms,
		       -- so a near-miss costs a proposal rather than a wrong link.
		       AND lower(f_unaccent(p.full_name)) = g.normalized_name
		      JOIN relationship r
		        ON r.person_id = p.id AND r.kind = 'employment'
		       -- Still employed TODAY, the same test the coverage and intro
		       -- reads take: a future end date is still employment.
		       AND r.archived_at IS NULL
		       AND (r.ended_at IS NULL OR r.ended_at > current_date)
		     WHERE g.match_status = 'unmatched'
		       AND g.tombstoned_at IS NULL
		       -- The employer is matched through matched_org_id, which the
		       -- Go-side resolver set using the ONE org-name normalizer. Doing
		       -- it here in SQL would mean a second spelling of the
		       -- legal-suffix strip, and two spellings of a normalizer drift.
		       AND ($%[1]d::uuid IS NULL OR g.owner_user_id = $%[1]d)
		       -- Narrowing to ONE contact must not narrow the ambiguity check:
		       -- the pair set below still sees every same-named candidate, so
		       -- a per-person call cannot suggest a link the sweep would have
		       -- refused as ambiguous. It filters the RESULT, not the pairs.
		       AND (%[2]s)
		       AND ` + ghostOwnerCapturePrivacy + `
		       AND g.matched_org_id IS NOT NULL
		       AND r.organization_id = g.matched_org_id
		       AND NOT EXISTS (
		           SELECT 1 FROM linkedin_connection other
		            WHERE other.matched_person_id = p.id
		              AND other.owner_user_id = g.owner_user_id
		              AND other.match_status = 'confirmed')
		),
		candidate AS (
		    -- Now the count is over distinct PEOPLE, which is what ambiguity
		    -- means. (count(DISTINCT …) is not available as a window function
		    -- in Postgres, hence the two steps rather than one.)
		    SELECT ghost_id, person_id, exact_name,
		           count(*) OVER (PARTITION BY ghost_id) AS matches
		      FROM pair
		)
		UPDATE linkedin_connection g
		   SET matched_person_id = c.person_id,
		       -- An EXACT name at a matched employer, with no other candidate,
		       -- is not a guess worth a human's attention: the two strings are
		       -- the same string, the employer agrees, and nobody else here is
		       -- called that. Asking about it trains people to click through
		       -- the queue without reading, which is what makes the genuinely
		       -- uncertain ones dangerous. A folded-only match — "André" vs
		       -- "Andre" — still goes to a human, because that is a judgement
		       -- about whether two spellings are one person.
		       match_status = CASE WHEN c.exact_name THEN 'confirmed' ELSE 'suggested' END,
		       updated_at = now()
		  FROM candidate c
		 WHERE g.id = c.ghost_id
		   -- Ambiguity is not a suggestion. Two contacts of the same name at
		   -- the same employer is exactly the case a human must resolve, and
		   -- picking one would be a guess wearing a confirmation's clothes.
		   AND c.matches = 1
		   -- The per-person narrowing, applied to the RESULT and not to the
		   -- pair set above: the ambiguity count must still see every
		   -- same-named candidate, or a per-person call would suggest a link
		   -- the workspace-wide sweep correctly refuses.
		   AND ($%[3]d::uuid IS NULL OR c.person_id = $%[3]d)`

// suggestGhostsByNameAndEmployer proposes the ghosts whose normalized name and
// employer agree with a contact's — and stops there.
//
// It requires BOTH, and it requires the employment to be live. Name alone is
// not a match in any market and least of all in this one; the employer is what
// turns a common name into a plausible identification, and it is still only
// plausible. A human confirms.
//
// It also refuses to propose a person some other ghost is already confirmed
// against: one contact cannot be two different LinkedIn connections of the
// same colleague, and offering that choice invites a wrong click.
func suggestGhostsByNameAndEmployer(ctx context.Context, tx pgx.Tx, owner, onlyPerson ids.UUID) (int, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	ownerPos := arg(nullableOwner(owner))
	personPos := arg(nullableOwner(onlyPerson))
	// Same reason as the email arm: a suggestion against an invisible contact
	// both creates a link the uploader may not make and reports its existence.
	visible, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return 0, err
	}
	if visible == "" {
		visible = sqlAlwaysVisible
	}
	tag, err := tx.Exec(ctx, fmt.Sprintf(suggestNameEmployerMatchSQL, ownerPos, visible, personPos), args...)
	if err != nil {
		return 0, fmt.Errorf("people: suggesting LinkedIn connection matches: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// matchGhostOrganizations attaches ghosts to an ACCOUNT by employer name even
// when the person never matches.
//
// This is where most of the value is, and it needs no identity at all. "Three
// people here are LinkedIn-connected to someone at Acme" is actionable on its
// own — it tells a rep the door is not cold — and it is true whether or not
// any of those three is a contact in the CRM.
// OrganizationLinkedInReach counts, per colleague, how many of their LinkedIn
// connections work at one account — the weaker, clearly-labelled evidence tier
// beside real interaction history.
//
// It is a COUNT and never a list of names, and that is a privacy decision
// rather than a payload-size one. The connections are third parties who never
// consented to appearing in this CRM; saying "Lars knows 3 people at Acme"
// discloses nothing about them, while naming them would publish a private
// address book to the colleague's whole team.
func OrganizationLinkedInReach(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (map[ids.UUID]int, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return nil, err
	}
	// The row gate, not just the object grant. A reach count is a statement
	// ABOUT an account — answering it for an account the caller cannot open
	// discloses that the account exists, and does so through a side door that
	// the account's own read path closes. 404-hiding, like every other
	// single-record read.
	if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT g.owner_user_id, count(*)
		  FROM linkedin_connection g
		  JOIN app_user u ON u.id = g.owner_user_id AND u.archived_at IS NULL
		 WHERE g.matched_org_id = $1
		   AND g.tombstoned_at IS NULL
		   AND g.match_status <> 'rejected'
		 GROUP BY g.owner_user_id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("people: counting LinkedIn reach into an account: %w", err)
	}
	defer rows.Close()
	out := map[ids.UUID]int{}
	for rows.Next() {
		var user ids.UUID
		var n int
		if err := rows.Scan(&user, &n); err != nil {
			return nil, err
		}
		out[user] = n
	}
	return out, rows.Err()
}

// nullableOwner renders the zero id as SQL NULL, which the scoping clauses
// read as "every owner". A zero uuid would otherwise match nobody and turn a
// workspace-wide sweep into a silent no-op.
func nullableOwner(owner ids.UUID) *ids.UUID {
	if owner == ids.Nil {
		return nil
	}
	return &owner
}
