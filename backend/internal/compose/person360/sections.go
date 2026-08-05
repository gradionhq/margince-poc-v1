// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// The per-section reads. Each one carries its own object grant so a
// caller missing it loses that section and keeps the page.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (s *Service) strengthSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, now time.Time, out *crmcontracts.Person360) error {
	rs, err := s.people.PersonStrengthTx(ctx, tx, personID, now)
	if err != nil {
		return err
	}
	wire := people.StrengthToWire(rs, now)
	out.Strength = &wire
	return nil
}

// relationshipChangesSection reports what happened to the relationship, as
// opposed to what it currently is.
//
// It sits beside the strength section rather than inside it because the two
// answer different questions and a caller may hold the grant for one reading
// and still lose the other to a section fault. Both fold the same §4 curve, so
// they cannot disagree about the same window.
func (s *Service) relationshipChangesSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, now time.Time, out *crmcontracts.Person360) error {
	changes, err := s.people.PersonRelationshipChangesTx(ctx, tx, personID, now)
	if err != nil {
		return err
	}
	wire := make([]crmcontracts.PersonRelationshipChange, 0, len(changes))
	for _, c := range changes {
		item := crmcontracts.PersonRelationshipChange{
			Kind: crmcontracts.PersonRelationshipChangeKind(c.Kind),
			At:   c.At,
		}
		// Days and the two bands are per-kind, so each is set only where it
		// means something. A zero "days" on a band move would read as "this
		// happened today".
		if c.Days > 0 {
			days := c.Days
			item.Days = &days
		}
		if c.FromBucket != "" {
			from := crmcontracts.PersonRelationshipChangeFromBucket(c.FromBucket)
			to := crmcontracts.PersonRelationshipChangeToBucket(c.ToBucket)
			item.FromBucket, item.ToBucket = &from, &to
		}
		wire = append(wire, item)
	}
	out.RelationshipChanges = &wire
	return nil
}

// employmentsSection lists this person's employment edges, current primary
// first — the header's "who they work for" and the career ribbon's history
// come from the same rows, so a former employer is never overwritten.
func (s *Service) employmentsSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "relationship"); err != nil {
		return err
	}
	limit := sectionCap
	kind := "employment"
	rows, page, err := s.people.ListRelationshipsTx(ctx, tx, people.ListRelationshipsInput{
		Kind: &kind, PersonID: &personID, Limit: &limit,
	})
	if err != nil {
		return err
	}
	data := make([]crmcontracts.Person360Employment, 0, len(rows))
	for _, r := range rows {
		if r.OrganizationID == nil {
			continue // an employment edge with no employer names nothing
		}
		e := crmcontracts.Person360Employment{
			RelationshipId:   openapi_types.UUID(r.ID),
			OrganizationId:   openapi_types.UUID(r.OrganizationID.UUID),
			IsCurrentPrimary: r.IsCurrentPrimary,
			Role:             r.Role,
			StartedAt:        r.StartedAt,
			EndedAt:          r.EndedAt,
		}
		name, err := s.organizationName(ctx, tx, *r.OrganizationID)
		if err != nil {
			return err
		}
		if name != "" {
			e.OrganizationName = &name
		}
		data = append(data, e)
	}
	// Current primary first, then the rest as the store ordered them: the
	// header reads the first row, so the employer they hold today must not
	// depend on insertion order.
	for i := range data {
		if data[i].IsCurrentPrimary && i != 0 {
			data[0], data[i] = data[i], data[0]
			break
		}
	}
	out.Employments = &struct {
		Data []crmcontracts.Person360Employment `json:"data"`
		Page crmcontracts.PageInfo              `json:"page"`
		// The store's own answer, not len(rows) >= cap: ListRelationshipsTx
		// over-fetches by one to know this exactly, and guessing it would
		// report has_more on a person holding exactly 25 employments and send
		// the client to a page that does not exist.
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: page.HasMore}}
	return nil
}

// organizationName resolves an employer's display name. A name the caller
// cannot read is simply absent — the edge still shows, without asserting a
// company the reader has no grant for.
func (s *Service) organizationName(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (string, error) {
	var name string
	err := tx.QueryRow(ctx, `SELECT display_name FROM organization WHERE id = $1 AND archived_at IS NULL`, orgID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		// Archived, or outside this caller's row scope. The edge still shows;
		// it just does not assert a company name the reader has no grant for.
		// This is the ONLY tolerated outcome — any other error has already
		// aborted the transaction, and continuing past it makes the NEXT
		// section fail with an error that names the wrong cause.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read employer name: %w", err)
	}
	return name, nil
}

// dealRolesSection lists the stakeholder seats this person holds. The role
// is what the edge records; it is never inferred from a job title.
func (s *Service) dealRolesSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "relationship"); err != nil {
		return err
	}
	if err := requireRead(ctx, "deal"); err != nil {
		return err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	personPos := arg(personID)
	dealScope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return err
	}
	if dealScope == "" {
		dealScope = "true"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT r.id, r.deal_id, r.role, d.name, s.name
		FROM relationship r
		JOIN deal d ON d.id = r.deal_id AND d.archived_at IS NULL
		LEFT JOIN stage s ON s.id = d.stage_id
		WHERE r.kind = 'deal_stakeholder' AND r.person_id = $%d
		  AND r.archived_at IS NULL AND (%s)
		ORDER BY r.id
		LIMIT %d`, personPos, dealScope, sectionCap+1), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	data := make([]crmcontracts.Person360DealRole, 0, sectionCap)
	for rows.Next() {
		var dr crmcontracts.Person360DealRole
		var relID, dealID ids.UUID
		var role *string
		if err := rows.Scan(&relID, &dealID, &role, &dr.DealTitle, &dr.DealStage); err != nil {
			return err
		}
		dr.RelationshipId = openapi_types.UUID(relID)
		dr.DealId = openapi_types.UUID(dealID)
		if role != nil {
			dr.Role = *role
		}
		data = append(data, dr)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	hasMore := len(data) > sectionCap
	if hasMore {
		data = data[:sectionCap]
	}
	out.DealRoles = &struct {
		Data []crmcontracts.Person360DealRole `json:"data"`
		Page crmcontracts.PageInfo            `json:"page"`
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: hasMore}}
	return nil
}
