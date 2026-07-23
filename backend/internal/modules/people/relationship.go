// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Relationship edges (data-model §5): employment (person↔org), deal
// stakeholders (deal↔person), and org↔org partner edges. An edge's
// visibility derives from its ENDPOINTS — every non-null endpoint must
// be visible to the caller, on read exactly as on write, so an edge can
// never leak a record its ends would hide. Mutations emit the anchor
// entity's .updated event (the catalog has no relationship.* family;
// an employment change IS a person-profile change).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// relationshipAnchor names the endpoint whose lifecycle a kind
// annotates — the entity whose .updated event a mutation emits and
// whose RBAC object gates it.
func relationshipAnchor(kind string) (object, column string) {
	switch kind {
	case "employment":
		return "person", "person_id"
	case "deal_stakeholder":
		return "deal", "deal_id"
	default: // partner_of, referred_by, co_sell_with
		return "organization", "organization_id"
	}
}

var relationshipKinds = map[string]bool{
	"employment": true, "deal_stakeholder": true,
	"partner_of": true, "referred_by": true, "co_sell_with": true,
}

const relationshipColumns = `id, workspace_id, kind, person_id, organization_id, counterparty_org_id, deal_id,
	role, is_current_primary, started_at, ended_at, source, captured_by, version, created_at, updated_at, archived_at`

type relationshipRow struct {
	ID                ids.UUID // no RelationshipKind in the kernel vocabulary: edges stay untyped
	WorkspaceID       ids.WorkspaceID
	Kind              string
	PersonID          *ids.PersonID
	OrganizationID    *ids.OrganizationID
	CounterpartyOrgID *ids.OrganizationID
	DealID            *ids.DealID
	Role              *string
	IsCurrentPrimary  bool
	StartedAt         *time.Time
	EndedAt           *time.Time
	Source            string
	CapturedBy        string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ArchivedAt        *time.Time
}

func scanRelationship(r pgx.Row) (relationshipRow, error) {
	var out relationshipRow
	err := r.Scan(&out.ID, &out.WorkspaceID, &out.Kind, &out.PersonID, &out.OrganizationID, &out.CounterpartyOrgID,
		&out.DealID, &out.Role, &out.IsCurrentPrimary, &out.StartedAt, &out.EndedAt,
		&out.Source, &out.CapturedBy, &out.Version, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt)
	return out, err
}

// relationshipEndpointScope renders "every non-null endpoint is
// visible": one EXISTS per endpoint table under the caller's own/team
// predicate. Unbounded actors carry no clause.
func relationshipEndpointScope(ctx context.Context, alias string, arg func(any) int) (string, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return "", errors.New("crmpeople: no actor bound to context")
	}
	if auth.Unbounded(actor) {
		return "", nil
	}
	var clauses []string
	for _, endpoint := range []struct{ column, table string }{
		{"person_id", "person"},
		{"organization_id", "organization"},
		{"counterparty_org_id", "organization"},
		{"deal_id", "deal"},
	} {
		predicate := auth.VisiblePredicate(actor, endpoint.table, arg)
		clauses = append(clauses, fmt.Sprintf(
			`(%[1]s.%[2]s IS NULL OR EXISTS (
			   SELECT 1 FROM %[3]s ep WHERE ep.id = %[1]s.%[2]s AND ep.archived_at IS NULL AND %[4]s))`,
			alias, endpoint.column, endpoint.table, predicate("ep")))
	}
	return "(" + strings.Join(clauses, " AND ") + ")", nil
}

type CreateRelationshipInput struct {
	Kind              string
	PersonID          *ids.PersonID
	OrganizationID    *ids.OrganizationID
	CounterpartyOrgID *ids.OrganizationID
	DealID            *ids.DealID
	Role              *string
	IsCurrentPrimary  bool
	StartedAt         *time.Time
	EndedAt           *time.Time
	Source            string
}

func (s *Store) CreateRelationship(ctx context.Context, in CreateRelationshipInput) (relationshipRow, error) {
	if !relationshipKinds[in.Kind] {
		return relationshipRow{}, &RequiredFieldError{Field: "kind"}
	}
	anchorObject, _ := relationshipAnchor(in.Kind)
	if err := auth.Require(ctx, "relationship", principal.ActionCreate); err != nil {
		return relationshipRow{}, err
	}
	if err := auth.Require(ctx, anchorObject, principal.ActionUpdate); err != nil {
		// The edge annotates its anchor: without the anchor's write
		// grant, an edge would be an RBAC side door onto it.
		return relationshipRow{}, err
	}
	capturedBy, err := storekit.CapturedBy(ctx)
	if err != nil {
		return relationshipRow{}, err
	}

	var out relationshipRow
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureRelationshipEndpoints(ctx, tx, in); err != nil {
			return err
		}
		// One current primary employer per person: demote the incumbent
		// inside the same transaction rather than failing the write.
		if in.Kind == "employment" && in.IsCurrentPrimary && in.PersonID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE relationship SET is_current_primary = false
				WHERE kind = 'employment' AND person_id = $1 AND is_current_primary AND archived_at IS NULL`,
				*in.PersonID); err != nil {
				return err
			}
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO relationship (workspace_id, kind, person_id, organization_id, counterparty_org_id,
			                          deal_id, role, is_current_primary, started_at, ended_at, source, captured_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING `+relationshipColumns,
			in.Kind, in.PersonID, in.OrganizationID, in.CounterpartyOrgID, in.DealID,
			in.Role, in.IsCurrentPrimary, in.StartedAt, in.EndedAt, in.Source, capturedBy)
		if out, err = scanRelationship(row); err != nil {
			return mapRelationshipConstraint(err, in.Kind)
		}
		return emitRelationshipChange(ctx, tx, "create", out)
	})
	return out, err
}

// ensureRelationshipEndpoints validates every supplied endpoint as a
// client-supplied FK argument (H1): each named target must be visible
// under the caller's row scope before the edge lands.
func ensureRelationshipEndpoints(ctx context.Context, tx pgx.Tx, in CreateRelationshipInput) error {
	for _, ref := range []struct {
		table string
		id    *ids.UUID
	}{
		{"person", untypedPtr(in.PersonID)}, {"organization", untypedPtr(in.OrganizationID)},
		{"organization", untypedPtr(in.CounterpartyOrgID)}, {"deal", untypedPtr(in.DealID)},
	} {
		if ref.id == nil {
			continue
		}
		if err := auth.EnsureLinkTarget(ctx, tx, ref.table, *ref.id); err != nil {
			return err
		}
	}
	return nil
}

// untypedPtr narrows an optional typed id back to the kernel UUID for
// the platform seams (auth, storekit) that speak untyped ids.
func untypedPtr[K ids.EntityKind](id *ids.ID[K]) *ids.UUID {
	if id == nil {
		return nil
	}
	return &id.UUID
}

// mapRelationshipConstraint turns the insert's constraint failures into
// typed input errors: the rel_* CHECKs are the kind→endpoint shape rules
// (migration 0007) — bad input, not a fault — and the partial unique
// indexes are the edge dedupe rules (a second identical edge conflicts
// with the existing one). Anything else surfaces unchanged.
func mapRelationshipConstraint(err error, kind string) error {
	if constraint, ok := storekit.CheckViolation(err); ok {
		switch constraint {
		case "rel_employment_shape", "rel_stakeholder_shape", "rel_partner_shape":
			return &RequiredFieldError{Field: "kind: " + kind + " endpoint shape"}
		case "rel_dates":
			return &RequiredFieldError{Field: "ended_at: must not precede started_at"}
		}
	}
	if constraint, ok := storekit.UniqueViolation(err); ok {
		switch constraint {
		case "uq_rel_current_primary_employer", "uq_rel_deal_person_role":
			return fmt.Errorf("relationship %s: %w", constraint, apperrors.ErrConflict)
		}
	}
	return err
}

type UpdateRelationshipInput struct {
	Role             *string
	IsCurrentPrimary *bool
	StartedAt        *time.Time
	EndedAt          *time.Time
	IfVersion        *int64
}

func (s *Store) UpdateRelationship(ctx context.Context, id ids.UUID, in UpdateRelationshipInput) (relationshipRow, error) {
	if err := auth.Require(ctx, "relationship", principal.ActionUpdate); err != nil {
		return relationshipRow{}, err
	}
	var out relationshipRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The row lock makes the state read and the update below one
		// race-free unit.
		if _, err := storekit.LockRow(ctx, tx, "relationship", id, storekit.LiveOnly); err != nil {
			return err
		}
		current, err := s.visibleRelationship(ctx, tx, id)
		if err != nil {
			return err
		}
		// Same rule as create: editing an edge is editing its anchor.
		anchorObject, _ := relationshipAnchor(current.Kind)
		if err := auth.Require(ctx, anchorObject, principal.ActionUpdate); err != nil {
			return err
		}
		if in.IfVersion != nil && *in.IfVersion != current.Version {
			return apperrors.ErrVersionSkew
		}
		if in.IsCurrentPrimary != nil && *in.IsCurrentPrimary &&
			current.Kind == "employment" && current.PersonID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE relationship SET is_current_primary = false
				WHERE kind = 'employment' AND person_id = $1 AND is_current_primary AND archived_at IS NULL AND id <> $2`,
				*current.PersonID, id); err != nil {
				return err
			}
		}
		row := tx.QueryRow(ctx, `
			UPDATE relationship SET
			  role = coalesce($2, role),
			  is_current_primary = coalesce($3, is_current_primary),
			  started_at = coalesce($4, started_at),
			  ended_at = coalesce($5, ended_at)
			WHERE id = $1
			RETURNING `+relationshipColumns,
			id, in.Role, in.IsCurrentPrimary, in.StartedAt, in.EndedAt)
		if out, err = scanRelationship(row); err != nil {
			return err
		}
		return emitRelationshipChange(ctx, tx, "update", out)
	})
	return out, err
}

func (s *Store) ArchiveRelationship(ctx context.Context, id ids.UUID) (relationshipRow, error) {
	if err := auth.Require(ctx, "relationship", principal.ActionDelete); err != nil {
		return relationshipRow{}, err
	}
	var out relationshipRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := s.visibleRelationship(ctx, tx, id)
		if err != nil {
			return err
		}
		// Same rule as create: removing an edge is editing its anchor.
		anchorObject, _ := relationshipAnchor(current.Kind)
		if err := auth.Require(ctx, anchorObject, principal.ActionUpdate); err != nil {
			return err
		}
		row := tx.QueryRow(ctx,
			`UPDATE relationship SET archived_at = now() WHERE id = $1 AND archived_at IS NULL RETURNING `+relationshipColumns, id)
		if out, err = scanRelationship(row); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		} else if err != nil {
			return err
		}
		return emitRelationshipChange(ctx, tx, "archive", out)
	})
	return out, err
}

// visibleRelationship loads one edge under the endpoint-visibility rule
// — absence and out-of-scope read identically (existence-hiding).
func (s *Store) visibleRelationship(ctx context.Context, tx pgx.Tx, id ids.UUID) (relationshipRow, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)
	scope, err := relationshipEndpointScope(ctx, "r", arg)
	if err != nil {
		return relationshipRow{}, err
	}
	sql := storekit.SQLf(`SELECT %s FROM relationship r WHERE r.id = $%d`, aliased(relationshipColumns, "r"), idPos)
	if scope != "" {
		sql += " AND " + scope
	}
	out, err := scanRelationship(tx.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return relationshipRow{}, apperrors.ErrNotFound
	}
	return out, err
}

// emitRelationshipChange lands the write shape on the edge's anchor:
// audit on the relationship row, event on the anchor entity (an
// employment change IS a person change to every consumer).
func emitRelationshipChange(ctx context.Context, tx pgx.Tx, action string, rel relationshipRow) error {
	anchorObject, _ := relationshipAnchor(rel.Kind)
	var anchorID ids.UUID
	switch anchorObject {
	case "person":
		anchorID = rel.PersonID.UUID
	case "deal":
		anchorID = rel.DealID.UUID
	default:
		anchorID = rel.OrganizationID.UUID
	}
	auditID, err := storekit.Audit(ctx, tx, action, "relationship", rel.ID, nil, map[string]any{
		"kind": rel.Kind, "role": rel.Role,
	})
	if err != nil {
		return err
	}
	changedFields := map[string]any{
		"delta": map[string]any{"relationship": map[string]any{"id": rel.ID, "kind": rel.Kind, "action": action}},
	}
	return storekit.EmitEvent(ctx, tx, auditID, anchorID, relationshipUpdatedPayload(anchorObject, changedFields))
}

// relationshipUpdatedPayload builds the anchor's .updated event for a
// relationship mutation — the same changed_fields delta wrapped in
// whichever of the three anchors' published OPEN envelopes this edge
// points at. All three (deal.updated, person.updated,
// organization.updated) are OPEN envelopes with an identical
// changed_fields shape, so the only real work here is picking the right
// generated struct for the anchor.
//
//nolint:ireturn // dispatches to one of WebhookPayloadDealUpdated/PersonUpdated/OrganizationUpdated by anchorObject; tested directly via the interface in person_organization_payload_test.go
func relationshipUpdatedPayload(anchorObject string, changedFields map[string]any) events.Payload {
	switch anchorObject {
	case "deal":
		return crmcontracts.WebhookPayloadDealUpdated{ChangedFields: changedFields}
	case "person":
		return crmcontracts.WebhookPayloadPersonUpdated{ChangedFields: changedFields}
	default: // organization
		return crmcontracts.WebhookPayloadOrganizationUpdated{ChangedFields: changedFields}
	}
}

// EnsureDealVisible probes a deal id under the caller's row scope —
// the deal-scoped stakeholder view needs the anchor's own answer when
// the edge list is empty (owned SQL on the deal row).
func (s *Store) EnsureDealVisible(ctx context.Context, dealID ids.DealID) error {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// EnsureLinkTarget, not EnsureVisible: the anchor must EXIST for
		// everyone — unbounded actors skip only the scope half.
		return auth.EnsureLinkTarget(ctx, tx, "deal", dealID.UUID)
	})
}

// aliased qualifies a comma-separated column list with a table alias.
func aliased(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

func wireRelationship(rel relationshipRow) crmcontracts.Relationship {
	out := crmcontracts.Relationship{
		Id:          openapi_types.UUID(rel.ID),
		WorkspaceId: openapi_types.UUID(rel.WorkspaceID.UUID),
		Kind:        crmcontracts.RelationshipKind(rel.Kind),
		Source:      rel.Source,
		CapturedBy:  &rel.CapturedBy,
		CreatedAt:   rel.CreatedAt,
		UpdatedAt:   rel.UpdatedAt,
		ArchivedAt:  rel.ArchivedAt,
		Role:        rel.Role,
	}
	version := crmcontracts.RowVersion(rel.Version)
	out.Version = &version
	out.IsCurrentPrimary = &rel.IsCurrentPrimary
	out.PersonId = uuidPtr(untypedPtr(rel.PersonID))
	out.OrganizationId = uuidPtr(untypedPtr(rel.OrganizationID))
	out.CounterpartyOrgId = uuidPtr(untypedPtr(rel.CounterpartyOrgID))
	out.DealId = uuidPtr(untypedPtr(rel.DealID))
	if rel.StartedAt != nil {
		out.StartedAt = &openapi_types.Date{Time: *rel.StartedAt}
	}
	if rel.EndedAt != nil {
		out.EndedAt = &openapi_types.Date{Time: *rel.EndedAt}
	}
	return out
}
