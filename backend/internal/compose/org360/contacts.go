// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The people section: who works at this account, how warm each
// relationship is, what each one does on the account's deals, and whether
// they may be contacted for each purpose.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// contactsSection lists the account's current employees with their §4
// strength, their stakeholder roles on this account's deals, and their
// per-purpose consent state. Contacts outside the caller's row scope are
// absent — the batch strength read that produced `all` applies that
// predicate itself, which is also why the scores arrive rather than being
// recomputed here: the account roll-up is folded from the same slice.
func contactsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time,
	all []people.ContactStrength,
) ([]crmcontracts.Organization360Contact, crmcontracts.PageInfo, error) {
	strengths, page := truncate(all)
	if len(strengths) == 0 {
		return []crmcontracts.Organization360Contact{}, page, nil
	}

	personIDs := make([]ids.PersonID, len(strengths))
	for i, s := range strengths {
		personIDs[i] = s.PersonID
	}
	identity, err := contactIdentity(ctx, tx, personIDs)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	roles, err := contactDealRoles(ctx, tx, orgID, personIDs)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	consent, err := contactConsent(ctx, tx, personIDs)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}

	out := make([]crmcontracts.Organization360Contact, 0, len(strengths))
	for _, s := range strengths {
		id := s.PersonID
		card := crmcontracts.Organization360Contact{
			PersonId:  openapi_types.UUID(id.UUID),
			Strength:  people.StrengthToWire(s.Strength, now),
			DealRoles: roles[id],
			Consent:   consent[id],
		}
		if card.DealRoles == nil {
			card.DealRoles = []crmcontracts.Organization360DealRole{}
		}
		if card.Consent == nil {
			card.Consent = map[string]crmcontracts.Organization360ContactConsent{}
		}
		if who, ok := identity[id]; ok {
			card.FullName = who.fullName
			card.Title = who.title
			card.PrimaryEmail = who.primaryEmail
		}
		out = append(out, card)
	}
	return out, page, nil
}

// contactCard is the display identity of one contact.
type contactCard struct {
	fullName     string
	title        *string
	primaryEmail *string
}

// contactIdentity reads name, title and the primary email address for a
// contact set. The address arrives through a correlated subquery so a
// contact with none on file still appears: the strength read already
// decided who is on this list, and a join could only shorten it.
func contactIdentity(ctx context.Context, tx pgx.Tx, personIDs []ids.PersonID) (map[ids.PersonID]contactCard, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.full_name, p.title,
		       (SELECT e.email FROM person_email e
		         WHERE e.person_id = p.id AND e.archived_at IS NULL
		         ORDER BY e.is_primary DESC, e.position, e.id
		         LIMIT 1)
		FROM person p WHERE p.id = ANY($1)`, personIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[ids.PersonID]contactCard, len(personIDs))
	for rows.Next() {
		var id ids.PersonID
		var card contactCard
		if err := rows.Scan(&id, &card.fullName, &card.title, &card.primaryEmail); err != nil {
			return nil, err
		}
		out[id] = card
	}
	return out, rows.Err()
}

// contactDealRoles reads each contact's stakeholder roles on THIS
// account's deals, pruned to the deals the caller can see: a rep who
// cannot read a colleague's deal must not learn who its champion is.
func contactDealRoles(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, personIDs []ids.PersonID) (map[ids.PersonID][]crmcontracts.Organization360DealRole, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	peoplePos, orgPos := arg(personIDs), arg(orgID)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT r.person_id, r.deal_id, r.role
		FROM relationship r
		JOIN deal d ON d.id = r.deal_id
		WHERE r.kind = 'deal_stakeholder' AND r.person_id = ANY($%d)
		  AND r.archived_at IS NULL AND r.ended_at IS NULL
		  AND d.organization_id = $%d AND d.archived_at IS NULL AND (%s)
		ORDER BY r.person_id, r.deal_id`, peoplePos, orgPos, dealScope), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ids.PersonID][]crmcontracts.Organization360DealRole{}
	for rows.Next() {
		var personID ids.PersonID
		var dealID ids.UUID
		var role *string
		if err := rows.Scan(&personID, &dealID, &role); err != nil {
			return nil, err
		}
		named := ""
		if role != nil {
			named = *role
		}
		out[personID] = append(out[personID], crmcontracts.Organization360DealRole{DealId: openapi_types.UUID(dealID), Role: named})
	}
	return out, rows.Err()
}

// contactConsent reads each contact's state per consent purpose. Every
// live purpose appears for every contact: a purpose with no stored row is
// "unknown", which is default-deny for outbound, and leaving the key out
// would let a caller read absence as permission.
func contactConsent(ctx context.Context, tx pgx.Tx, personIDs []ids.PersonID) (map[ids.PersonID]map[string]crmcontracts.Organization360ContactConsent, error) {
	purposes, err := livePurposeKeys(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make(map[ids.PersonID]map[string]crmcontracts.Organization360ContactConsent, len(personIDs))
	for _, id := range personIDs {
		states := make(map[string]crmcontracts.Organization360ContactConsent, len(purposes))
		for _, key := range purposes {
			states[key] = crmcontracts.Organization360ContactConsentUnknown
		}
		out[id] = states
	}
	rows, err := tx.Query(ctx, `
		SELECT pc.person_id, cp.key, pc.state
		FROM person_consent pc
		JOIN consent_purpose cp ON cp.id = pc.purpose_id AND cp.archived_at IS NULL
		WHERE pc.person_id = ANY($1)`, personIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var personID ids.PersonID
		var key, state string
		if err := rows.Scan(&personID, &key, &state); err != nil {
			return nil, err
		}
		if states, ok := out[personID]; ok {
			states[key] = crmcontracts.Organization360ContactConsent(state)
		}
	}
	return out, rows.Err()
}

func livePurposeKeys(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT key FROM consent_purpose WHERE archived_at IS NULL ORDER BY key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var key string
		err := row.Scan(&key)
		return key, err
	})
}
