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
	case ProjectStakeholderKind:
		return projectObjectName, "project_id"
	default: // partner_of, referred_by, co_sell_with
		return "organization", "organization_id"
	}
}

var relationshipKinds = map[string]bool{
	"employment": true, "deal_stakeholder": true, "project_stakeholder": true,
	"partner_of": true, "referred_by": true, "co_sell_with": true,
}

const relationshipColumns = `id, kind, person_id, organization_id, counterparty_org_id, deal_id, project_id,
	role, is_current_primary, started_at, ended_at, source, captured_by, version, created_at, updated_at, archived_at`

type relationshipRow struct {
	ID                ids.UUID // no RelationshipKind in the kernel vocabulary: edges stay untyped
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
	err := r.Scan(&out.ID, &out.Kind, &out.PersonID, &out.OrganizationID, &out.CounterpartyOrgID,
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
	// IsCurrentPrimary is TRI-STATE, and the third state is what makes the rule
	// in the insert safe: nil means the caller expressed no opinion and the
	// store decides, false means they said this is NOT the person's current
	// primary employment. Collapsing the two would silently invert a choice the
	// caller can see themselves making — the person rail's "current employer"
	// checkbox sends exactly that false.
	IsCurrentPrimary *bool
	StartedAt        *time.Time
	EndedAt          *time.Time
	Source           string
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
		// Both rules below read the person's OTHER employments and then write
		// against what they read, so they are one unit per person or they are a
		// race: two concurrent unmarked employments at different companies would
		// each see no primary, each claim it, and one would come back 409 naming
		// a flag its caller never sent.
		if in.Kind == "employment" && in.PersonID != nil {
			if err := storekit.LockWriteIdentity(ctx, tx, "employment", in.PersonID.String()); err != nil {
				return err
			}
		}
		// One current primary employer per person: demote the incumbent
		// inside the same transaction rather than failing the write. An
		// employment that arrives already OVER claims nothing, so it displaces
		// nobody — see the insert below, which refuses it the flag. A future
		// end date is a notice period and DOES displace: they work there.
		//
		// That last test is in the statement, not in Go, so it reads the same
		// clock the insert below reads. A Go-side comparison would answer a
		// different question on a server in a different timezone from the
		// database, and the two would disagree about exactly one day.
		if in.Kind == "employment" && in.IsCurrentPrimary != nil && *in.IsCurrentPrimary &&
			in.PersonID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE relationship SET is_current_primary = false
				WHERE kind = 'employment' AND person_id = $1 AND is_current_primary AND archived_at IS NULL
				  AND `+EmploymentIsCurrentSQL("$2::date"),
				*in.PersonID, in.EndedAt); err != nil {
				return err
			}
		}
		// Two rules about is_current_primary, spelled in the insert so the
		// returned row is the row that landed — a follow-up UPDATE would bump
		// the version under the caller about to read it back.
		//
		// A person's ONLY current employment is their current primary one, WHEN
		// THE CALLER SAID NOTHING ($8 IS NULL). The column defaults to false and
		// nothing else ever promotes, so without this a person with exactly one
		// employer has none marked: a state no reader of the column expects and
		// none of them can repair. A caller who sent the field keeps their
		// answer, including an explicit false — deriving over it would invert a
		// choice they can see themselves making. The subquery excludes an
		// ended-but-still-primary row as well as a current one, because
		// promoting past either would violate uq_rel_current_primary_employer.
		//
		// And an employment that arrives already ended never holds the flag,
		// however it was asked for — history being backfilled is not where
		// somebody works today. That is the same rule the UPDATE below applies,
		// and both read it off the row rather than off the request.
		row := tx.QueryRow(ctx, `
			INSERT INTO relationship (kind, person_id, organization_id, counterparty_org_id,
			                          deal_id, project_id, role, is_current_primary, started_at, ended_at, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7,
			        coalesce($8, $1 = 'employment' AND NOT EXISTS (
			          SELECT 1 FROM relationship
			           WHERE kind = 'employment' AND person_id = $2 AND archived_at IS NULL
			             AND (`+EmploymentIsCurrentSQL("ended_at")+` OR is_current_primary)))
			          AND ($1 <> 'employment' OR `+EmploymentIsCurrentSQL("$10::date")+`),
			        $9, $10, $11, $12)
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
		// Not for an employment this patch leaves ended, and not for one that
		// already was: the UPDATE below refuses such a row the flag, so
		// demoting the incumbent for it would clear the person's primary
		// employer and put nothing in its place.
		if in.IsCurrentPrimary != nil && *in.IsCurrentPrimary &&
			in.EndedAt == nil && current.EndedAt == nil &&
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
			  -- An employment somebody has LEFT is not their CURRENT primary
			  -- one, whichever half of the patch makes it so: ending the job
			  -- clears the flag, and setting the flag on a job already over
			  -- does not take. Written against the row rather than as a Go
			  -- condition, so the two halves cannot drift apart. LEFT, not
			  -- "has a date" — see EmploymentIsCurrentSQL.
			  is_current_primary = coalesce($3, is_current_primary)
			    AND (kind <> 'employment' OR `+EmploymentIsCurrentSQL("coalesce($5, ended_at)")+`),
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
