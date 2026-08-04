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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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
	case projectStakeholderKind:
		return projectObjectName, "project_id"
	default: // partner_of, referred_by, co_sell_with
		return "organization", "organization_id"
	}
}

var relationshipKinds = map[string]bool{
	"employment": true, "deal_stakeholder": true, "project_stakeholder": true,
	"partner_of": true, "referred_by": true, "co_sell_with": true,
}

const relationshipColumns = `id, workspace_id, kind, person_id, organization_id, counterparty_org_id, deal_id, project_id,
	role, is_current_primary, started_at, ended_at, source, captured_by, version, created_at, updated_at, archived_at`

type relationshipRow struct {
	ID                ids.UUID // no RelationshipKind in the kernel vocabulary: edges stay untyped
	WorkspaceID       ids.WorkspaceID
	Kind              string
	PersonID          *ids.PersonID
	OrganizationID    *ids.OrganizationID
	CounterpartyOrgID *ids.OrganizationID
	DealID            *ids.DealID
	ProjectID         *ids.ProjectID
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
		&out.DealID, &out.ProjectID, &out.Role, &out.IsCurrentPrimary, &out.StartedAt, &out.EndedAt,
		&out.Source, &out.CapturedBy, &out.Version, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt)
	return out, err
}

type CreateRelationshipInput struct {
	Kind              string
	PersonID          *ids.PersonID
	OrganizationID    *ids.OrganizationID
	CounterpartyOrgID *ids.OrganizationID
	DealID            *ids.DealID
	ProjectID         *ids.ProjectID
	Role              *string
	IsCurrentPrimary  bool
	StartedAt         *time.Time
	EndedAt           *time.Time
	Source            string
}

func (s *Store) CreateRelationship(ctx context.Context, in CreateRelationshipInput) (relationshipRow, error) {
	// A SUPPLIED kind outside the vocabulary is a different fault from an omitted
	// one, and they used to answer the same sentence: a caller who sent
	// kind="EMPLOYMENT" was told `kind` is required, which is factually wrong about
	// a field they can see in their own request. The case-sensitivity trap makes
	// that land in practice, so the refusal names the allowed set.
	if in.Kind == "" {
		return relationshipRow{}, &RequiredFieldError{Field: relationshipKindField}
	}
	if !relationshipKinds[in.Kind] {
		return relationshipRow{}, &RelationshipKindError{Kind: in.Kind}
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
			                          deal_id, project_id, role, is_current_primary, started_at, ended_at, source, captured_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING `+relationshipColumns,
			in.Kind, in.PersonID, in.OrganizationID, in.CounterpartyOrgID, in.DealID, in.ProjectID,
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
		{"person", untypedPtr(in.PersonID)},
		{"organization", untypedPtr(in.OrganizationID)},
		{"organization", untypedPtr(in.CounterpartyOrgID)},
		{"deal", untypedPtr(in.DealID)},
		{projectObjectName, untypedPtr(in.ProjectID)},
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
			// Through the SAME constraint mapping the insert uses. A patch can
			// violate rel_dates exactly as a create can — moving ended_at behind
			// started_at — and without this the two verbs answered one rule two
			// ways: a named refusal on create, the generic constraint net on
			// update. current.Kind, because a patch cannot change the kind.
			return mapRelationshipConstraint(err, current.Kind)
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
	scope, err := auth.RelationshipEndpointScope(ctx, "r", arg)
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
	case projectObjectName:
		anchorID = rel.ProjectID.UUID
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
//nolint:ireturn // dispatches to one of PublicEventDeal/Project/Person/OrganizationUpdated by anchorObject; tested directly via the interface in person_organization_payload_test.go
func relationshipUpdatedPayload(anchorObject string, changedFields map[string]any) events.Payload {
	switch anchorObject {
	case "deal":
		return crmcontracts.PublicEventDealUpdated{ChangedFields: changedFields}
	case projectObjectName:
		return crmcontracts.PublicEventProjectUpdated{ChangedFields: changedFields}
	case "person":
		return crmcontracts.PublicEventPersonUpdated{ChangedFields: changedFields}
	default: // organization
		return crmcontracts.PublicEventOrganizationUpdated{ChangedFields: changedFields}
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

// GetRelationship reads one edge under the endpoint-visibility rule, in its own
// transaction — the entry point the datasource seam needs, since every other
// caller of visibleRelationship already holds a transaction of its own.
//
// The seam needs it for three verbs and not only for a read tool: create_record
// reads the edge back after writing it, and archive_record reads the target
// BEFORE staging, to summarize for the human who will approve. Without this the
// edge would commit and the tool would report a read-back failure — a false
// failure with a real side effect — and the 🟡 archive could not even stage.
//
// The RBAC gate is the read one, not the anchor's update one that the mutating
// verbs also demand: reading an edge discloses its endpoints, which is what
// `relationship` read governs. Absence and out-of-scope answer identically.
func (s *Store) GetRelationship(ctx context.Context, id ids.UUID) (relationshipRow, error) {
	if err := auth.Require(ctx, "relationship", principal.ActionRead); err != nil {
		return relationshipRow{}, err
	}
	var out relationshipRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row, err := s.visibleRelationship(ctx, tx, id)
		if err != nil {
			return err
		}
		// Live only, matching every other record type's Read (which passes
		// storekit.LiveOnly). visibleRelationship deliberately does NOT filter —
		// Update locks live-only itself and Archive's own WHERE clause does the
		// work — so the filter belongs here, or an archived edge would go on
		// being served by the one verb whose whole job is to say what the record
		// currently is. Post-filtering is safe: the row already passed this
		// caller's endpoint scope, so nothing about it is disclosed by the check.
		if row.ArchivedAt != nil {
			return apperrors.ErrNotFound
		}
		out = row
		return nil
	})
	return out, err
}

// EnsureProjectVisible probes a project id under the caller's row scope —
// the project-scoped stakeholder view needs the anchor's own answer when
// the edge list is empty, so "no stakeholders yet" and "no such project"
// stay distinguishable without disclosing either.
func (s *Store) EnsureProjectVisible(ctx context.Context, projectID ids.ProjectID) error {
	if err := auth.Require(ctx, projectObjectName, principal.ActionRead); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		return auth.EnsureLinkTarget(ctx, tx, projectObjectName, projectID.UUID)
	})
}
