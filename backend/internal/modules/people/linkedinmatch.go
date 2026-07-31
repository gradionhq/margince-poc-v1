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

// MatchLinkedInConnections runs the matcher over every unmatched ghost in the
// workspace and reports what it decided.
//
// It is safe to re-run: a ghost a human has already confirmed or rejected is
// never revisited, so a nightly pass cannot overturn a person's decision, and
// a rejection is permanent rather than something the next import forgets.
func (s *Store) MatchLinkedInConnections(ctx context.Context) (LinkedInMatchResult, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return LinkedInMatchResult{}, err
	}
	var out LinkedInMatchResult
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		confirmed, err := matchGhostsByEmail(ctx, tx)
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
		suggested, err := suggestGhostsByNameAndEmployer(ctx, tx)
		if err != nil {
			return err
		}
		out.Suggested = suggested
		return nil
	})
	return out, err
}

// matchGhostsByEmail confirms the ghosts whose address is already a known
// contact's address. This is the one automatic confirmation, and it is
// automatic for the same reason capture's dedupe is: an address identifies a
// person, and treating it as a suggestion would ask a human to re-confirm a
// fact the system is already certain of everywhere else.
func matchGhostsByEmail(ctx context.Context, tx pgx.Tx) (int, error) {
	tag, err := tx.Exec(ctx, `
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
		   AND g.match_status = 'unmatched'`)
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
func suggestGhostsByNameAndEmployer(ctx context.Context, tx pgx.Tx) (int, error) {
	tag, err := tx.Exec(ctx, `
		WITH candidate AS (
		    SELECT g.id AS ghost_id, p.id AS person_id,
		           count(*) OVER (PARTITION BY g.id) AS matches
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
		       AND r.ended_at IS NULL AND r.archived_at IS NULL
		     WHERE g.match_status = 'unmatched'
		       AND g.tombstoned_at IS NULL
		       -- The employer is matched through matched_org_id, which the
		       -- Go-side resolver set using the ONE org-name normalizer. Doing
		       -- it here in SQL would mean a second spelling of the
		       -- legal-suffix strip, and two spellings of a normalizer drift.
		       AND g.matched_org_id IS NOT NULL
		       AND r.organization_id = g.matched_org_id
		       AND NOT EXISTS (
		           SELECT 1 FROM linkedin_connection other
		            WHERE other.matched_person_id = p.id
		              AND other.owner_user_id = g.owner_user_id
		              AND other.match_status = 'confirmed')
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
		   AND c.matches = 1`)
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
		SELECT id, company_name FROM linkedin_connection
		 WHERE company_name IS NOT NULL AND tombstoned_at IS NULL`)
	if err != nil {
		return fmt.Errorf("people: reading LinkedIn connections to place: %w", err)
	}
	var ghostIDs, orgIDs []ids.UUID
	for rows.Next() {
		var ghost ids.UUID
		var company string
		if err := rows.Scan(&ghost, &company); err != nil {
			rows.Close()
			return err
		}
		if org, known := orgs[NormalizeOrgName(company)]; known {
			ghostIDs = append(ghostIDs, ghost)
			orgIDs = append(orgIDs, org)
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

// orgKeys is every live account by its normalized name. An ambiguous key —
// two accounts that normalize the same — is dropped rather than picked
// between: attaching a colleague's network to the wrong account is a worse
// answer than attaching it to none.
func orgKeys(ctx context.Context, tx pgx.Tx) (map[string]ids.UUID, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, display_name FROM organization WHERE archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("people: reading accounts for LinkedIn placement: %w", err)
	}
	defer rows.Close()
	out := map[string]ids.UUID{}
	ambiguous := map[string]bool{}
	for rows.Next() {
		var id ids.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
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
		out[key] = id
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
