// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

// The engine-module service (Handlers→Service shape, decisions/0024):
// this file owns the Service seam, the typed refusals, and the one
// catalog-row scan every operation shares. Tables owned: custom_field.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// rbacObject is the custom_field RBAC object every service entry point
// gates on (decisions/0006 posture: admin/ops full CRUD, everyone else
// read — the pipeline-config precedent, because a field definition
// reshapes what the system stores for everyone's records).
const rbacObject = "custom_field"

// The two lifecycle states, spelled the way the status CHECK constraint
// spells them (migrations/core/0063).
const (
	statusActive  = "active"
	statusRetired = "retired"
)

// Service is the governed custom-fields engine over the catalog
// aggregate. It rides two pools with deliberately different authority:
// pool is the RLS-bound app pool (margince_app) every catalog-only
// operation uses, and schemaPool is the owner-privileged schema-change
// pool (decisions/0024) that ONLY the two DDL paths (Create, SetOptions)
// touch — nil when the operator has not mounted the second credential,
// in which case those two paths refuse with ErrSchemaChangesUnavailable.
type Service struct {
	pool       *pgxpool.Pool
	schemaPool *pgxpool.Pool
}

// NewService wires the engine. schemaPool MAY be nil: the schema-change
// seam is boot-optional (decisions/0024 "unwired by default"), exactly
// like the blobstore and keyvault seams.
func NewService(pool, schemaPool *pgxpool.Pool) *Service {
	return &Service{pool: pool, schemaPool: schemaPool}
}

// ErrStructural refuses a label judged structural — a new object,
// relationship, or logic, never a bounded scalar attribute
// (CUSTOM-FIELDS-AC-4); the transport maps it to the contract's
// structural_change_refused 422.
var ErrStructural = errors.New("customfields: structural change refused — a new object, relationship, or rule ships as a reviewed source change, not a runtime column")

// ErrSchemaChangesUnavailable reports that this deployment mounted no
// owner-privileged schema pool (--schema-dsn / MARGINCE_SCHEMA_DSN), so
// the two runtime-DDL operations are not available here — the handler
// maps it to 501, mirroring the unwired-blobstore posture
// (activities.ErrBlobstoreUnconfigured).
var ErrSchemaChangesUnavailable = errors.New("customfields: no schema-change pool configured")

// ErrNotPicklist refuses an options edit on a field whose type is not
// picklist — only a picklist has an options-derived CHECK to regenerate.
var ErrNotPicklist = errors.New("customfields: only a picklist field's options can be edited")

// ErrFieldRetired refuses rename and options edits on a retired field:
// retirement is the terminal lifecycle state — the row stays fetchable
// and re-retiring stays a no-op, but label and options are frozen. Wraps
// the conflict sentinel so both transports answer the ordinary 409; the
// message is the wire detail, so no package prefix.
var ErrFieldRetired = fmt.Errorf("the field is retired; a retired field cannot be renamed or have its options edited: %w", apperrors.ErrConflict)

// ErrTableBusy reports that a schema-change transaction hit its bounded
// lock wait (SQLSTATE 55P03 under the SET LOCAL lock_timeout) instead of
// queueing an ACCESS EXCLUSIVE request behind a long-running reader — a
// retryable condition, not a fault. Wraps the conflict sentinel (409);
// the message is the wire detail, so no package prefix. Deliberately
// does NOT wrap the pg error: httperr scrubs a sentinel whose chain
// carries an infrastructure cause, and this detail must stay actionable.
var ErrTableBusy = fmt.Errorf("the target table is busy with concurrent activity; retry the schema change: %w", apperrors.ErrConflict)

// ErrLastOption refuses an empty replacement option set — a picklist
// always keeps at least one allowed value.
var ErrLastOption = errors.New("customfields: a picklist needs at least one option")

// ValidationError carries the complete field-level violation list, so
// the transport can render every problem in one round trip.
type ValidationError struct{ Errors []FieldError }

func (e *ValidationError) Error() string { return "customfields: validation failed" }

// ColumnTakenError is the cross-workspace column-namespace collision
// (decisions/0024 "Resolution — global column-namespace collision"):
// the physical column namespace on a shared core table is global, so a
// slug another workspace already claimed cannot be added here. It reads
// as a 409 conflict; the message is the actionable remedy.
type ColumnTakenError struct{ Column string }

func (e *ColumnTakenError) Error() string {
	return fmt.Sprintf("custom-field column %q is taken platform-wide; choose another label", e.Column)
}

// Is maps the collision onto the generic conflict sentinel so transports
// without a dedicated branch still answer 409, never 500.
func (e *ColumnTakenError) Is(target error) bool { return target == apperrors.ErrConflict }

// catalogColumns is the ONE spelling of the custom_field row selection,
// in scanCustomField's scan order.
const catalogColumns = `id, workspace_id, object, slug, label, type, status, archived_at,
	column_name, currency, options, created_by, created_at, updated_at, version`

// scanCustomField scans one catalogColumns row into the contract shape
// (contract types as transport DTOs, decisions/0003).
func scanCustomField(row pgx.Row) (crmcontracts.CustomField, error) {
	var (
		out                          crmcontracts.CustomField
		id, wsID, createdBy          ids.UUID
		object, typ, status, colName string
		currency                     *string
		optionsRaw                   []byte
		version                      int64
	)
	err := row.Scan(&id, &wsID, &object, &out.Slug, &out.Label, &typ, &status, &out.ArchivedAt,
		&colName, &currency, &optionsRaw, &createdBy, &out.CreatedAt, &out.UpdatedAt, &version)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	out.Id = openapi_types.UUID(id)
	out.WorkspaceId = openapi_types.UUID(wsID)
	out.CreatedBy = openapi_types.UUID(createdBy)
	out.Object = crmcontracts.CustomFieldObject(object)
	out.Type = crmcontracts.CustomFieldType(typ)
	out.Status = crmcontracts.CustomFieldStatus(status)
	out.ColumnName = &colName
	out.Currency = currency
	out.Version = &version
	if len(optionsRaw) > 0 {
		var options []string
		if err := json.Unmarshal(optionsRaw, &options); err != nil {
			return crmcontracts.CustomField{}, fmt.Errorf("customfields: catalog options column is not a JSON string array: %w", err)
		}
		out.Options = &options
	}
	return out, nil
}

// createdBy resolves the app_user the catalog row's created_by FK names:
// the human caller, or the granting human behind an agent call ("agent ≤
// human" — the field is attributed to the authority that approved it).
func createdBy(ctx context.Context) (ids.UUID, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return ids.Nil, errors.New("customfields: no actor bound to context")
	}
	switch {
	case !p.UserID.IsZero():
		return p.UserID, nil
	case !p.OnBehalfOf.IsZero():
		return p.OnBehalfOf, nil
	default:
		return ids.Nil, errors.New("customfields: caller resolves to no app_user to attribute the field to")
	}
}
