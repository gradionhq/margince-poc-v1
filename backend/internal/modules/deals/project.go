// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The project write paths: create, archive, and the typed errors the
// transport maps onto contract codes. A project is the body of work a
// client relationship is made of — the deals in this module hang off it,
// which is why it lives in this bounded context rather than one of its own.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// projectObject is this record type's RBAC object and catalog object name,
// spelled once.
const projectObject = "project"

// dealProjectSameOrgConstraint is the constraint trigger that enforces
// "a deal and its project name the same company" — a rule spanning two
// rows, so it cannot be a CHECK and its name is what the deal write paths
// match on to answer 422 rather than 500.
const dealProjectSameOrgConstraint = "deal_project_same_org"

// PhaseInitiative is where every project is born: the ladder's head. A
// project only ever leaves it through AdvanceProjectPhase, which is what
// keeps the phase and its history in one transaction.
const PhaseInitiative = "initiative"

// PhaseClosed is the one phase that demands a reason — closing is a claim
// about the work having ended, and an unexplained claim is not answerable
// later.
const PhaseClosed = "closed"

// CreateProjectInput is one new body of work. Phase and captured_by are
// absent by design: both are the server's to decide.
type CreateProjectInput struct {
	Name           string
	Key            *string
	OrganizationID ids.OrganizationID
	OwnerID        *ids.UserID
	Description    *string
	StartedAt      *time.Time
	TargetEndDate  *time.Time
	Source         string
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (storekit customcolumns).
	CustomFields map[string]any
}

// CreateProject opens a project on a company, with its birth phase-history
// row written in the same transaction.
func (s *Store) CreateProject(ctx context.Context, in CreateProjectInput) (crmcontracts.Project, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionCreate); err != nil {
		return crmcontracts.Project{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Project{}, err
	}
	active, err := s.activeColumnsFor(ctx, projectObject)
	if err != nil {
		return crmcontracts.Project{}, err
	}

	var out crmcontracts.Project
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = createProjectTx(ctx, tx, in, by, active)
		return err
	})
	return out, err
}

// createProjectTx inserts the project with its birth phase-history row and
// runs the write shape, all inside the caller's transaction.
func createProjectTx(ctx context.Context, tx pgx.Tx, in CreateProjectInput, by string, active []fieldcatalog.Column) (crmcontracts.Project, error) {
	wsID := storekit.MustWorkspace(ctx)

	// The anchor company is a client-supplied reference to a row-scoped
	// record, so naming it is a read of it: the caller must be able to see
	// the company before a project can be hung off it. The composite FK
	// only proves same-workspace, which is a weaker claim.
	if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.OrganizationID.UUID); err != nil {
		return crmcontracts.Project{}, err
	}

	if err := ensureProjectKeyFree(ctx, tx, in.Key); err != nil {
		return crmcontracts.Project{}, err
	}

	id := ids.New[ids.ProjectKind]()
	cfCols, cfHolders, cfArgs := storekit.InsertFragments(active, in.CustomFields, 11)
	args := []any{
		id, wsID, in.Name, in.Key, in.OrganizationID, in.OwnerID,
		in.Description, in.StartedAt, in.TargetEndDate, in.Source, by,
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO project (id, workspace_id, name, key, organization_id, owner_id,
		                      description, started_at, target_end_date, source, captured_by`+cfCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11`+cfHolders+`)`,
		append(args, cfArgs...)...)
	if err != nil {
		if conflict := projectKeyConflict(err, in.Key); conflict != nil {
			return crmcontracts.Project{}, conflict
		}
		// Covers the owner FK; the organization target was pre-checked.
		if storekit.IsForeignKeyViolation(err) {
			return crmcontracts.Project{}, apperrors.ErrNotFound
		}
		if constraint, ok := storekit.CheckViolation(err); ok {
			return crmcontracts.Project{}, projectCheckError(constraint)
		}
		return crmcontracts.Project{}, fmt.Errorf("insert project: %w", err)
	}

	// The birth row: from_phase NULL, exactly as deal_stage_history records
	// a deal's first placement. A project's history is complete from row one.
	if _, err := tx.Exec(ctx,
		`INSERT INTO project_phase_history (workspace_id, project_id, from_phase, to_phase, changed_by)
		 VALUES ($1, $2, NULL, $3, $4)`,
		wsID, id, PhaseInitiative, by); err != nil {
		return crmcontracts.Project{}, fmt.Errorf("record project phase history: %w", err)
	}

	auditID, err := storekit.Audit(ctx, tx, "create", projectObject, id.UUID, nil, map[string]any{dealNameColumn: in.Name})
	if err != nil {
		return crmcontracts.Project{}, fmt.Errorf("audit project create: %w", err)
	}
	created := crmcontracts.PublicEventProjectCreated{
		Name:           in.Name,
		OrganizationId: openapi_types.UUID(in.OrganizationID.UUID),
		Phase:          PhaseInitiative,
	}
	if in.Key != nil {
		created.Key = in.Key
	}
	if in.OwnerID != nil {
		owner := openapi_types.UUID(in.OwnerID.UUID)
		created.OwnerId = &owner
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, created); err != nil {
		return crmcontracts.Project{}, fmt.Errorf("emit project.created: %w", err)
	}
	out, err := readProject(ctx, tx, id, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Project{}, fmt.Errorf("read created project: %w", err)
	}
	return out, nil
}

// ArchiveProject soft-deletes a project and the grouping it provided. It
// deliberately does NOT touch the activities or deals it grouped: the
// grouping dies, the history does not. The deal's project_id is cleared by
// the FK's ON DELETE SET NULL only on a hard delete, so an archived
// project keeps its rollup readable — which is what "the history does not
// die" means in practice.
func (s *Store) ArchiveProject(ctx context.Context, id ids.ProjectID, ifVersion *int64) (crmcontracts.Project, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionDelete); err != nil {
		return crmcontracts.Project{}, err
	}
	active, err := s.activeColumnsFor(ctx, projectObject)
	if err != nil {
		return crmcontracts.Project{}, err
	}
	var out crmcontracts.Project
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, projectObject, id.UUID); err != nil {
			return err
		}
		// A liveness probe, not a wire read — no custom columns needed.
		current, err := readProject(ctx, tx, id, storekit.LiveOnly, nil)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		// The archive rides the same guarded patch every other by-id write
		// uses, so the If-Match the contract accepts is actually honored:
		// archiving a project someone else just re-phased is version skew,
		// not a silent overwrite.
		p := storekit.NewPatch()
		p.Set("archived_at", current.ArchivedAt, now)
		if err := p.ApplyGuarded(ctx, tx, projectObject, id.UUID, ifVersion); err != nil {
			return fmt.Errorf("archive project: %w", err)
		}
		// The stakeholder edges are attributes of the grouping, so they go
		// with it — the people themselves are untouched.
		if _, err := tx.Exec(ctx,
			`UPDATE relationship SET archived_at = $2
			   WHERE project_id = $1 AND kind = 'project_stakeholder' AND archived_at IS NULL`,
			id, now); err != nil {
			return fmt.Errorf("archive project stakeholder edges: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM list_member WHERE entity_type = 'project' AND entity_id = $1`, id); err != nil {
			return fmt.Errorf("detach list memberships: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM taggable WHERE entity_type = 'project' AND entity_id = $1`, id); err != nil {
			return fmt.Errorf("detach tags: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "archive", projectObject, id.UUID, p.Before(), p.After())
		if err != nil {
			return fmt.Errorf("audit project archive: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventProjectArchived{}); err != nil {
			return fmt.Errorf("emit project.archived: %w", err)
		}
		if out, err = readProject(ctx, tx, id, storekit.IncludeArchived, active); err != nil {
			return fmt.Errorf("read archived project: %w", err)
		}
		return nil
	})
	return out, err
}

// ensureProjectKeyFree resolves a VISIBLE key collision BEFORE the write, so
// the 409 can carry the id of the project already holding the key — a caller
// that collided wants to open that project, not to be told "taken" and left
// hunting for it. When the holder is outside the caller's scope the probe
// finds nothing and the unique index refuses the write instead, naming no id.
// It has to run first: once a unique violation has aborted the transaction,
// no further query in it can answer anything.
func ensureProjectKeyFree(ctx context.Context, tx pgx.Tx, key *string) error {
	if key == nil || *key == "" {
		return nil
	}
	// Naming the colliding id is a READ of that project, so the probe carries
	// the row-scope clause like any other read. Without it the conflict path
	// hands a caller the exact id of a row it may not see — the key is
	// workspace-unique, so an invisible owner's project would still answer.
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	keyPos := arg(*key)
	scope, err := auth.ScopeClauseFor(ctx, projectObject, "", arg)
	if err != nil {
		return err
	}
	where := storekit.SQLf("lower(key) = lower($%d) AND archived_at IS NULL", keyPos)
	if scope != "" {
		where += " AND " + scope
	}
	var existing ids.UUID
	err = tx.QueryRow(ctx, storekit.SQLf(`SELECT id FROM project WHERE %s`, where), args...).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the key is free, or it is held by a project this caller
		// cannot see. Both answer the same way here: the unique index is the
		// authority, and projectKeyConflict reports the refusal without an id.
		return nil
	}
	if err != nil {
		return fmt.Errorf("check project key availability: %w", err)
	}
	return &ProjectKeyTakenError{Key: *key, ExistingID: &existing}
}

// projectKeyConflict maps the index's own refusal. The pre-check above
// answers the ordinary case with an id; this covers the narrow race where
// a concurrent write took the key in between, where there is no id to
// name — a conflict without the pointer beats turning a 409 into a 500.
func projectKeyConflict(err error, key *string) error {
	constraint, ok := storekit.UniqueViolation(err)
	if !ok || constraint != "uq_project_key" || key == nil {
		return nil
	}
	return &ProjectKeyTakenError{Key: *key}
}

// projectCheckError names the schema-side business rules that can still
// fire after the per-path validations, so a breach reads as a 422 about a
// rule rather than an opaque server fault.
func projectCheckError(constraint string) error {
	switch constraint {
	case "project_key_shape":
		return &ProjectKeyShapeError{}
	case "project_closed_reason":
		return &ClosedReasonRequiredError{}
	case "project_dates":
		return &ProjectDateRangeError{}
	default:
		return &ProjectConstraintError{Constraint: constraint}
	}
}

// ProjectKeyTakenError maps to 409 project_key_taken.
type ProjectKeyTakenError struct {
	Key        string
	ExistingID *ids.UUID
}

func (e *ProjectKeyTakenError) Error() string {
	return "a live project already uses the key " + e.Key
}

// ProjectKeyShapeError maps to 422: the key must be letter-led so it can
// never be a bare number, which would match dates, amounts and order
// numbers in an inbound subject line.
type ProjectKeyShapeError struct{}

func (e *ProjectKeyShapeError) Error() string {
	return "a project key must start with a letter and use only letters, digits, hyphen or underscore (2-24 characters)"
}

// ClosedReasonRequiredError maps to 422 closed_reason_required.
type ClosedReasonRequiredError struct{}

func (e *ClosedReasonRequiredError) Error() string {
	return "closing a project requires a reason"
}

// ProjectDateRangeError maps to 422: a project cannot end before it started.
type ProjectDateRangeError struct{}

func (e *ProjectDateRangeError) Error() string {
	return "a project's end date cannot precede its start date"
}

// ProjectConstraintError is the honest fallback for a project CHECK this
// module has not given its own message: still a 422 (a business rule was
// broken, not a server fault), and it names the rule so the gap is
// visible rather than silent.
type ProjectConstraintError struct{ Constraint string }

func (e *ProjectConstraintError) Error() string {
	return "the project violates the " + e.Constraint + " rule"
}

// DealProjectOrgMismatchError maps to 422: a deal and the project it
// belongs to must name the same company. Raised by the
// deal_project_same_org constraint trigger, which is the only place the
// cross-row rule can be enforced.
type DealProjectOrgMismatchError struct{}

func (e *DealProjectOrgMismatchError) Error() string {
	return "a deal and its project must belong to the same company"
}
