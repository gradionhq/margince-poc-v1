// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Placing a ghost at an account (ADR-0078 §2.1b) — the employer half of the
// matcher, split from the person half so each file holds one concept.
//
// It answers the weaker of the two questions the import exists for: not "who
// is this person" but "does this connection work somewhere we sell to". A
// wrong answer here misattributes a reach count; a wrong answer on the person
// side attaches a stranger to a customer record. The two carry different
// evidence bars for that reason.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
// account.
//
// The capture-privacy arm, matching ghostOwnerCapturePrivacy on the person
// side. Row scope arrives through the caller's own clause, because the sweep
// runs under each owner's real principal. Placing a connection at an account the member cannot read would report its
// existence to them through a reach count — the arithmetic on that payload is
// differenceable, so an account in neither the visible list nor the unresolved
// total would itself be the disclosure.
func (c orgCandidate) reachableBy(member ids.UUID) bool {
	return !c.private || c.owner == member
}

// orgKeys is every live account by its normalized name. An ambiguous key —
// two accounts that normalize the same — is dropped rather than picked
// between: attaching a colleague's network to the wrong account is a worse
// answer than attaching it to none.
func orgKeys(ctx context.Context, tx pgx.Tx) (map[string]orgCandidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT o.id, o.display_name, o.visibility = 'owner',
		       coalesce(o.owner_id, '00000000-0000-0000-0000-000000000000'::uuid)
		  FROM organization o WHERE o.archived_at IS NULL`)
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
