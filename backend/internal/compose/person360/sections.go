// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// The per-section reads. Each one carries its own object grant so a
// caller missing it loses that section and keeps the page.

import (
	"context"
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

// employmentsSection lists this person's employment edges, current primary
// first — the header's "who they work for" and the career ribbon's history
// come from the same rows, so a former employer is never overwritten.
func (s *Service) employmentsSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "relationship"); err != nil {
		return err
	}
	limit := sectionCap
	kind := "employment"
	rows, _, err := s.people.ListRelationshipsTx(ctx, tx, people.ListRelationshipsInput{
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
		if name, err := s.organizationName(ctx, tx, *r.OrganizationID); err == nil && name != "" {
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
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: len(rows) >= sectionCap}}
	return nil
}

// organizationName resolves an employer's display name. A name the caller
// cannot read is simply absent — the edge still shows, without asserting a
// company the reader has no grant for.
func (s *Service) organizationName(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (string, error) {
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM organization WHERE id = $1 AND archived_at IS NULL`, orgID).Scan(&name)
	return name, err
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
