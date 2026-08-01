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
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
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
func (s *Store) MatchLinkedInConnectionsForPerson(ctx context.Context, person ids.UUID) (LinkedInMatchResult, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return LinkedInMatchResult{}, err
	}
	var out LinkedInMatchResult
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		confirmed, err := matchGhostsByEmail(ctx, tx, ids.Nil, person)
		if err != nil {
			return err
		}
		out.Confirmed = confirmed
		if err := matchGhostOrganizations(ctx, tx); err != nil {
			return err
		}
		suggested, err := suggestGhostsByNameAndEmployer(ctx, tx, ids.Nil, person)
		out.Suggested = suggested
		return err
	})
	return out, err
}

// capturePrivacyForGhostOwner is the ONE boundary the workspace-wide matcher
// may not cross.
//
// The sweep and the event consumer run under a SYSTEM principal, and a system
// principal is exempt from capture privacy by design (auth.UnboundedFor) —
// provisioning, the relay and the privacy engines have to read every row. That
// exemption is correct for them and wrong here: it made auth.ScopeClauseFor
// return an empty clause, so the matcher happily linked Alice's ghost to a
// contact Bob had captured privately, and the review list then reported the
// match back to Alice. She could not read the record, and did not need to —
// the status alone proved it exists.
//
// So the match carries the boundary itself, keyed to the GHOST's owner rather
// than to whoever is executing: a private contact is a candidate only for the
// member who captured it. It composes with, and does not replace, the caller's
// own scope clause — an interactive call is still narrowed by both.
const capturePrivacyForGhostOwner = `(p.visibility <> 'owner' OR p.owner_id = g.owner_user_id)`

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
		   AND `+capturePrivacyForGhostOwner+`
		   AND (%[2]s)`, ownerPos, visible, personPos), args...)
	if err != nil {
		return 0, fmt.Errorf("people: matching LinkedIn connections by address: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

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
	tag, err := tx.Exec(ctx, fmt.Sprintf(`
		WITH pair AS (
		    -- DISTINCT pairs FIRST. A contact with two live employment rows at
		    -- one account (a role change recorded as a second row) joins twice
		    -- and is still one candidate; counting the join rows would read
		    -- that as an ambiguity and refuse a correct suggestion.
		    SELECT DISTINCT g.id AS ghost_id, p.id AS person_id
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
		       AND `+capturePrivacyForGhostOwner+`
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
		    SELECT ghost_id, person_id,
		           count(*) OVER (PARTITION BY ghost_id) AS matches
		      FROM pair
		)
		UPDATE linkedin_connection g
		   SET matched_person_id = c.person_id,
		       match_status = 'suggested',
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
		   AND ($%[3]d::uuid IS NULL OR c.person_id = $%[3]d)`, ownerPos, visible, personPos), args...)
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
func matchGhostOrganizations(ctx context.Context, tx pgx.Tx) error {
	// Resolved in Go rather than SQL because the account key is
	// NormalizeOrgName — case- and accent-folded AND stripped of its trailing
	// legal suffix, so a connection at "Acme GmbH" reaches the account stored
	// as "Acme". Reproducing that strip in SQL would be a second spelling of
	// the PO-PARAM-1 suffix list, and two spellings of a normalizer drift
	// until they disagree about a customer's name.
	orgs, err := orgKeys(ctx, tx)
	if err != nil {
		return err
	}
	if len(orgs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id, owner_user_id, company_name FROM linkedin_connection
		 WHERE company_name IS NOT NULL AND tombstoned_at IS NULL`)
	if err != nil {
		return fmt.Errorf("people: reading LinkedIn connections to place: %w", err)
	}
	var ghostIDs, orgIDs []ids.UUID
	for rows.Next() {
		var ghost, ghostOwner ids.UUID
		var company string
		if err := rows.Scan(&ghost, &ghostOwner, &company); err != nil {
			rows.Close()
			return err
		}
		// The SAME cleaner the import applies, then the narrow fallbacks. A
		// fallback is accepted only when it resolves to exactly one account —
		// orgKeys already drops every ambiguous key — so a looser lookup can
		// widen what is FOUND without ever widening what is GUESSED.
		for _, key := range orgMatchKeys(company) {
			org, known := orgs[key]
			if !known {
				continue
			}
			// A key that resolves ONLY to an account this member may not be
			// told about stops the search rather than falling through to a
			// looser key: the looser key is a weaker claim, and answering a
			// privacy refusal with a worse guess is not an improvement.
			if org.reachableBy(ghostOwner) {
				ghostIDs = append(ghostIDs, ghost)
				orgIDs = append(orgIDs, org.id)
			}
			break
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ghostIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE linkedin_connection g
		   SET matched_org_id = t.org_id, updated_at = now()
		  FROM unnest($1::uuid[], $2::uuid[]) AS t(ghost_id, org_id)
		 WHERE g.id = t.ghost_id AND g.matched_org_id IS DISTINCT FROM t.org_id`,
		ghostIDs, orgIDs); err != nil {
		return fmt.Errorf("people: attaching LinkedIn connections to accounts: %w", err)
	}
	return nil
}

// orgCandidate is one account a ghost could be placed at, with what capture
// privacy needs to decide whether THIS ghost's owner may be told about it.
type orgCandidate struct {
	id      ids.UUID
	private bool
	owner   ids.UUID
}

// reachableBy answers whether a member may have their connection placed at this
// account. An owner-private account is a captured, unpromoted record belonging
// to the member who captured it — placing somebody else's connection there
// would report its existence to them through a reach count.
func (c orgCandidate) reachableBy(member ids.UUID) bool {
	return !c.private || c.owner == member
}

// orgKeys is every live account by its normalized name. An ambiguous key —
// two accounts that normalize the same — is dropped rather than picked
// between: attaching a colleague's network to the wrong account is a worse
// answer than attaching it to none.
func orgKeys(ctx context.Context, tx pgx.Tx) (map[string]orgCandidate, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, display_name, visibility = 'owner', coalesce(owner_id, '00000000-0000-0000-0000-000000000000'::uuid)
		   FROM organization WHERE archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("people: reading accounts for LinkedIn placement: %w", err)
	}
	defer rows.Close()
	out := map[string]orgCandidate{}
	ambiguous := map[string]bool{}
	for rows.Next() {
		var c orgCandidate
		var name string
		if err := rows.Scan(&c.id, &name, &c.private, &c.owner); err != nil {
			return nil, err
		}
		key := NormalizeOrgName(name)
		if key == "" {
			continue
		}
		if _, seen := out[key]; seen {
			ambiguous[key] = true
			continue
		}
		out[key] = c
	}
	for key := range ambiguous {
		delete(out, key)
	}
	return out, rows.Err()
}

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
func nullableOwner(owner ids.UUID) any {
	if owner == ids.Nil {
		return nil
	}
	return owner
}
