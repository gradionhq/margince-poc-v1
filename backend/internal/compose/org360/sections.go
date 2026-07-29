// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The per-section reads. Each one runs inside the assembling transaction,
// carries its own object grant (resolved once in assemble.go), and prunes
// to the caller's row scope with the same auth.ScopeClauseFor predicate
// the module lists use — so a section can never out-see the dedicated
// endpoint it summarizes.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sectionLimit is how many rows of a nested collection one 360 carries.
// The section is a summary with a "there is more" flag, not a paging
// surface: follow-up pages come from the dedicated endpoint for that
// collection, which owns the cursor vocabulary.
const sectionLimit = 25

// truncate cuts a section to sectionLimit and reports whether it had to.
func truncate[T any](rows []T) ([]T, crmcontracts.PageInfo) {
	if len(rows) > sectionLimit {
		return rows[:sectionLimit], crmcontracts.PageInfo{HasMore: true}
	}
	return rows, crmcontracts.PageInfo{HasMore: false}
}

// scopeAll is the predicate an unbounded (admin) caller gets: the SQL
// that embeds a row-scope clause then needs only one spelling.
const scopeAll = "TRUE"

// scopeClause resolves one object's row-scope predicate for the caller,
// answering scopeAll for an unbounded caller.
func scopeClause(ctx context.Context, object, alias string, arg func(any) int) (string, error) {
	clause, err := auth.ScopeClauseFor(ctx, object, alias, arg)
	if err != nil {
		return "", err
	}
	if clause == "" {
		return scopeAll, nil
	}
	return clause, nil
}

// contactsSection lists the account's current employees with their §4
// strength, their stakeholder roles on this account's deals, and their
// per-purpose consent state. Contacts outside the caller's row scope are
// absent — the batch strength read applies that predicate itself.
func contactsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time) ([]crmcontracts.Organization360Contact, crmcontracts.PageInfo, error) {
	strengths, err := people.StrengthForOrgContacts(ctx, tx, orgID, now)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	strengths, page := truncate(strengths)
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
			Strength:  strengthToWire(s.Strength, now),
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
// contact set. The email is a LEFT JOIN: a contact with no address on file
// is still a contact, and dropping them here would silently shorten the
// list the strength read already decided.
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
		  AND d.organization_id = $%d AND d.archived_at IS NULL AND %s
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

// dealsSection reads the account's open deals plus the two lifetime
// figures the header shows. won_lifetime sums amount_minor_base — each
// deal's amount at its FROZEN close-time rate — so the figure never moves
// when today's FX does.
func dealsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time) (crmcontracts.Organization360Deals, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return crmcontracts.Organization360Deals{}, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT d.id, d.name, d.status, d.stage_id, s.name, d.amount_minor, d.currency,
		       d.expected_close_date, d.created_at, d.last_activity_at, d.wait_until
		FROM deal d
		LEFT JOIN stage s ON s.id = d.stage_id AND s.workspace_id = d.workspace_id
		WHERE d.organization_id = $%d AND d.status = 'open' AND d.archived_at IS NULL AND %s
		ORDER BY d.created_at DESC, d.id DESC`, orgPos, dealScope), args...)
	if err != nil {
		return crmcontracts.Organization360Deals{}, err
	}
	open, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.Organization360Deal, error) {
		var d crmcontracts.Organization360Deal
		var id, stageID ids.UUID
		var stageIDPtr *ids.UUID
		var status string
		var amountMinor *int64
		var currency *string
		var createdAt time.Time
		var lastActivityAt, waitUntil *time.Time
		if err := row.Scan(&id, &d.Name, &status, &stageIDPtr, &d.StageName, &amountMinor, &currency,
			&d.ExpectedCloseDate, &createdAt, &lastActivityAt, &waitUntil); err != nil {
			return d, err
		}
		d.DealId = openapi_types.UUID(id)
		d.Status = crmcontracts.Organization360DealStatus(status)
		if stageIDPtr != nil {
			stageID = *stageIDPtr
			v := openapi_types.UUID(stageID)
			d.StageId = &v
		}
		if amountMinor != nil {
			d.Amount = &crmcontracts.Money{AmountMinor: amountMinor, Currency: currency}
		}
		d.Stalled = deals.IsStalled(status, createdAt, lastActivityAt, waitUntil, now)
		return d, nil
	})
	if err != nil {
		return crmcontracts.Organization360Deals{}, err
	}
	open, page := truncate(open)

	lifetime, lost, err := closedTotals(ctx, tx, orgID)
	if err != nil {
		return crmcontracts.Organization360Deals{}, err
	}
	return crmcontracts.Organization360Deals{
		Data:        open,
		Page:        page,
		WonLifetime: lifetime,
		LostCount:   lost,
	}, nil
}

// closedTotals sums won money and counts lost deals over the same row
// scope the open list uses — a total that included deals the caller cannot
// open would disclose their existence through arithmetic.
func closedTotals(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (crmcontracts.Money, int, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return crmcontracts.Money{}, 0, err
	}
	var wonMinor int64
	var lost int
	var baseCurrency string
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT coalesce(sum(d.amount_minor_base) FILTER (WHERE d.status = 'won'), 0)::bigint,
		       count(*) FILTER (WHERE d.status = 'lost'),
		       (SELECT base_currency FROM workspace WHERE id = d.workspace_id)
		FROM deal d
		WHERE d.organization_id = $%d AND d.archived_at IS NULL
		  AND d.status IN ('won','lost') AND %s
		GROUP BY d.workspace_id`, orgPos, dealScope), args...).Scan(&wonMinor, &lost, &baseCurrency)
	if err == pgx.ErrNoRows {
		// No closed deal on this account yet: an honest zero in the
		// workspace's own currency, not a missing figure.
		return workspaceZero(ctx, tx)
	}
	if err != nil {
		return crmcontracts.Money{}, 0, err
	}
	return crmcontracts.Money{AmountMinor: &wonMinor, Currency: &baseCurrency}, lost, nil
}

func workspaceZero(ctx context.Context, tx pgx.Tx) (crmcontracts.Money, int, error) {
	var baseCurrency string
	if err := tx.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE id = current_setting('app.workspace_id')::uuid`).
		Scan(&baseCurrency); err != nil {
		return crmcontracts.Money{}, 0, fmt.Errorf("read workspace base currency: %w", err)
	}
	zero := int64(0)
	return crmcontracts.Money{AmountMinor: &zero, Currency: &baseCurrency}, 0, nil
}

// nextStepsSection reads the account's open tasks in the order a rep works
// them: overdue first, then dated, then undated. A task reaches the
// account through any of its links — the task itself, its deal, or the
// contact it is about — which is why the link walk is a UNION rather than
// one join.
func nextStepsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time) ([]crmcontracts.Organization360NextStep, crmcontracts.PageInfo, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	activityScope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	if activityScope == "" {
		activityScope = scopeAll
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT a.id, coalesce(a.subject, ''), a.due_at, a.assignee_id,
		       (SELECT dl.deal_id FROM activity_link dl
		         WHERE dl.activity_id = a.id AND dl.entity_type = 'deal' ORDER BY dl.id LIMIT 1),
		       (SELECT pl.person_id FROM activity_link pl
		         WHERE pl.activity_id = a.id AND pl.entity_type = 'person' ORDER BY pl.id LIMIT 1)
		FROM activity a
		WHERE a.kind = 'task' AND NOT a.is_done AND a.archived_at IS NULL AND %s
		  AND EXISTS (
		    SELECT 1 FROM activity_link l
		    LEFT JOIN deal d ON d.id = l.deal_id
		    LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
		      AND r.ended_at IS NULL AND r.archived_at IS NULL
		    WHERE l.activity_id = a.id
		      AND (l.organization_id = $%d OR d.organization_id = $%d OR r.organization_id = $%d))
		ORDER BY (a.due_at IS NULL), a.due_at, a.id`,
		activityScope, orgPos, orgPos, orgPos), args...)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	steps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.Organization360NextStep, error) {
		var step crmcontracts.Organization360NextStep
		var id ids.UUID
		var assignee, dealID, personID *ids.UUID
		if err := row.Scan(&id, &step.Subject, &step.DueAt, &assignee, &dealID, &personID); err != nil {
			return step, err
		}
		step.ActivityId = openapi_types.UUID(id)
		step.AssigneeId = uuidPtr(assignee)
		step.LinkedDealId = uuidPtr(dealID)
		step.LinkedPersonId = uuidPtr(personID)
		step.Overdue = step.DueAt != nil && step.DueAt.Before(now)
		return step, nil
	})
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	// Overdue leads by construction: the SQL orders dated before undated and
	// earliest first, and overdue is exactly "dated before now".
	steps, page := truncate(steps)
	if steps == nil {
		steps = []crmcontracts.Organization360NextStep{}
	}
	return steps, page, nil
}

// tagsSection reads the tags applied to the account.
func tagsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) ([]crmcontracts.Tag, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.workspace_id, t.name, t.color, t.created_at, t.updated_at, t.archived_at
		FROM tag t
		JOIN taggable g ON g.tag_id = t.id AND g.entity_type = 'organization' AND g.entity_id = $1
		WHERE t.archived_at IS NULL
		ORDER BY t.name, t.id`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.Tag, error) {
		var t crmcontracts.Tag
		var id, wsID ids.UUID
		if err := row.Scan(&id, &wsID, &t.Name, &t.Color, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt); err != nil {
			return t, err
		}
		t.Id = openapi_types.UUID(id)
		t.WorkspaceId = openapi_types.UUID(wsID)
		return t, nil
	})
}

// listMembershipsSection reads the lists the account belongs to, pruned to
// the ones the caller can read.
func listMembershipsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) ([]crmcontracts.List, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	listScope, err := scopeClause(ctx, "list", "l", arg)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT l.id, l.workspace_id, l.name, l.entity_type, l.list_type, l.definition,
		       l.owner_id, l.team_id, l.created_at, l.updated_at, l.archived_at
		FROM list l
		JOIN list_member m ON m.list_id = l.id AND m.entity_type = 'organization' AND m.entity_id = $%d
		WHERE l.archived_at IS NULL AND %s
		ORDER BY l.name, l.id`, orgPos, listScope), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.List, error) {
		var l crmcontracts.List
		var id, wsID ids.UUID
		var ownerID, teamID *ids.UUID
		var entityType, listType string
		if err := row.Scan(&id, &wsID, &l.Name, &entityType, &listType, &l.Definition,
			&ownerID, &teamID, &l.CreatedAt, &l.UpdatedAt, &l.ArchivedAt); err != nil {
			return l, err
		}
		l.Id = openapi_types.UUID(id)
		l.WorkspaceId = openapi_types.UUID(wsID)
		l.EntityType = crmcontracts.ListEntityType(entityType)
		l.ListType = crmcontracts.ListListType(listType)
		l.OwnerId = uuidPtr(ownerID)
		l.TeamId = uuidPtr(teamID)
		return l, nil
	})
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	v := openapi_types.UUID(*id)
	return &v
}
