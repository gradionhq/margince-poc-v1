// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// pgDuplicateColumn is SQLSTATE 42701: the ALTER TABLE's ADD COLUMN hit
// a column that already exists. Under the advisory lock the pre-check
// answers this case first; the SQLSTATE branch is the belt for a column
// added outside the engine (a fork migration racing a create).
const pgDuplicateColumn = "42701"

// pgLockNotAvailable is SQLSTATE 55P03: a lock wait exceeded the
// transaction's SET LOCAL lock_timeout — the retryable ErrTableBusy
// answer, never a 500.
const pgLockNotAvailable = "55P03"

// lockTimedOut reports whether err is the bounded lock wait firing
// (SQLSTATE 55P03).
func lockTimedOut(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgLockNotAvailable
}

// Create is the single chokepoint allowed to run a runtime ALTER TABLE
// (custom-fields.md "one chokepoint"): it validates spec, refuses a
// structural request, derives slug/column_name, and then commits the
// physical column + the catalog row + one audit entry in ONE transaction
// on the owner-privileged schema pool — Postgres transactional DDL makes
// the three land or roll back together (decisions/0024).
//
// Deliberately NOT database.WithWorkspaceTx: that helper runs on the app
// pool, whose margince_app role carries DML-only grants and cannot ALTER
// a core table. The transaction here opens as the schema pool's owner
// role, runs the DDL first, THEN downgrades itself to exactly the
// authority every other tenant write runs under (SET LOCAL ROLE
// margince_app + the app.workspace_id GUC) for the RLS-governed catalog
// insert and audit write.
func (s *Service) Create(ctx context.Context, spec FieldSpec) (crmcontracts.CustomField, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionCreate); err != nil {
		return crmcontracts.CustomField{}, err
	}
	if errs := Validate(spec); len(errs) > 0 {
		return crmcontracts.CustomField{}, &ValidationError{Errors: errs}
	}
	if IsStructural(spec.Label) {
		return crmcontracts.CustomField{}, ErrStructural
	}
	slug := DeriveSlug(spec.Label)
	column := ColumnName(slug)
	ddl, err := BuildDDL(spec.Object, column, spec)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	if s.schemaPool == nil {
		return crmcontracts.CustomField{}, ErrSchemaChangesUnavailable
	}
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return crmcontracts.CustomField{}, errors.New("customfields: no workspace bound to context")
	}
	creator, err := createdBy(ctx)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}

	tx, err := s.schemaPool.Begin(ctx)
	if err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: begin schema tx: %w", err)
	}
	// Error-path safety net only; after Commit this is pgx's ErrTxClosed
	// no-op, and on the error path the operation's own error is the one
	// the caller must see (the WithWorkspaceTx spelling).
	//craft:ignore swallowed-errors deferred rollback of a committed tx is a designed no-op; real failures already left through the operation error
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := s.createInTx(ctx, tx, spec, wsID, creator, slug, column, ddl)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: commit schema tx: %w", err)
	}
	return out, nil
}

// createInTx is Create's transaction body: lock → collision pre-check →
// ALTER (owner) → downgrade → catalog INSERT + audit.
func (s *Service) createInTx(ctx context.Context, tx pgx.Tx, spec FieldSpec, wsID ids.UUID, creator ids.UUID, slug, column, ddl string) (crmcontracts.CustomField, error) {
	if err := beginSchemaChange(ctx, tx, wsID, spec.Object); err != nil {
		return crmcontracts.CustomField{}, err
	}
	if err := refuseTakenColumn(ctx, tx, wsID, spec.Object, column); err != nil {
		return crmcontracts.CustomField{}, err
	}

	// The one privileged statement: the ALTER runs as the schema pool's
	// owner role. 42701 can still fire despite the pre-check when the
	// column arrived outside the engine's serialization (a fork
	// migration) — the same honest answer applies. 55P03 is the bounded
	// lock wait firing: the table is busy, the caller retries.
	if _, err := tx.Exec(ctx, ddl); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgDuplicateColumn {
			return crmcontracts.CustomField{}, &ColumnTakenError{Column: column}
		}
		if lockTimedOut(err) {
			return crmcontracts.CustomField{}, ErrTableBusy
		}
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: adding column: %w", err)
	}

	if err := downgradeToAppRole(ctx, tx); err != nil {
		return crmcontracts.CustomField{}, err
	}

	var currency *string
	if spec.Type == TypeCurrency {
		currency = spec.Currency
	}
	optionsArg, err := optionsJSON(spec)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	row := tx.QueryRow(ctx, `INSERT INTO custom_field (workspace_id, object, slug, label, type, column_name, currency, options, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+catalogColumns,
		wsID, spec.Object, slug, spec.Label, spec.Type, column, currency, optionsArg, creator)
	out, err := scanCustomField(row)
	if storekit.IsUniqueViolation(err) {
		// The per-workspace (object, slug/column_name) unique index fired:
		// a same-workspace duplicate that raced past the pre-check. The
		// failed INSERT rolls the whole transaction back, ALTER included.
		return crmcontracts.CustomField{}, fmt.Errorf(
			"a custom field named %q already exists on %s in this workspace: %w", slug, spec.Object, apperrors.ErrConflict)
	}
	if err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: inserting catalog row: %w", err)
	}

	after := map[string]any{
		fieldObject: string(out.Object), "slug": out.Slug, fieldLabel: out.Label,
		fieldType: string(out.Type), "column_name": column, fieldStatus: string(out.Status),
	}
	if out.Currency != nil {
		after[fieldCurrency] = *out.Currency
	}
	if out.Options != nil {
		after[fieldOptions] = *out.Options
	}
	// Audit-only by ratification: the closed event catalog (events.md §5)
	// defines no custom_field.* type — the spec's custom-fields.md §Events
	// pins the audit entry as the add/rename/retire trail, and a
	// cross-object catalog change has no single family stream to ride
	// (the attachments precedent in writeshape_test.go).
	if _, err := storekit.AuditWithEvidence(ctx, tx, "create", rbacObject, ids.UUID(out.Id),
		nil, after, map[string]any{fieldSource: spec.Source}); err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: audit write: %w", err)
	}
	return out, nil
}

// beginSchemaChange arms the transaction for a governed DDL step: it
// binds the workspace GUC (the WithWorkspaceTx spelling — parameterized
// set_config, never string-built SET LOCAL) so every catalog read and
// write in this transaction is workspace-scoped, then bounds the lock
// waits and serializes with the per-table advisory lock
// (serializeSchemaChange).
func beginSchemaChange(ctx context.Context, tx pgx.Tx, wsID ids.UUID, object string) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID.String()); err != nil {
		return fmt.Errorf("customfields: binding workspace GUC: %w", err)
	}
	return serializeSchemaChange(ctx, tx, object)
}

// serializeSchemaChange is the shared arming step of both DDL paths
// (Create's beginSchemaChange, SetOptions' setOptionsInTx):
//
// SET LOCAL lock_timeout bounds every lock wait in this transaction —
// the ALTER TABLE ahead needs ACCESS EXCLUSIVE, and unbounded, a single
// long-running reader queues that request while EVERY subsequent DML on
// the shared core table queues behind it: a platform-wide stall from one
// admin call. Timing out (SQLSTATE 55P03) answers the retryable
// ErrTableBusy instead; transaction-local, so the pooled connection
// reverts at COMMIT/ROLLBACK.
//
// The transaction-scoped advisory lock, keyed on the target table,
// removes the 42701 race window between concurrent ADD COLUMNs on the
// same shared table: whoever holds it sees the loser's committed column
// in the pre-check instead of colliding mid-ALTER; auto-released at
// COMMIT/ROLLBACK. lock_timeout bounds this wait too — a peer stuck
// mid-DDL answers the same busy signal.
func serializeSchemaChange(ctx context.Context, tx pgx.Tx, object string) error {
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
		return fmt.Errorf("customfields: bounding lock waits: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('customfields:' || $1, 0))`, object); err != nil {
		if lockTimedOut(err) {
			return ErrTableBusy
		}
		return fmt.Errorf("customfields: serializing schema change: %w", err)
	}
	return nil
}

// refuseTakenColumn is the decisions/0024 collision answer, decided
// under the advisory lock BEFORE the ALTER: the per-workspace unique
// indexes cannot see that the physical column namespace on the shared
// table is global. A live column with a catalog row in THIS workspace is
// the ordinary duplicate-slug conflict; a live column with none is
// another workspace's claim — an honest 409 naming the remedy, because
// existence of a bare column name discloses nothing about who holds it.
func refuseTakenColumn(ctx context.Context, tx pgx.Tx, wsID ids.UUID, object, column string) error {
	var columnExists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		  WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2)`,
		object, column).Scan(&columnExists); err != nil {
		return fmt.Errorf("customfields: probing column namespace: %w", err)
	}
	if !columnExists {
		return nil
	}
	// The explicit workspace predicate keeps this correct even where the
	// schema pool's role bypasses RLS (a superuser owner in dev).
	var ownedHere bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM custom_field
		  WHERE workspace_id = $1 AND object = $2 AND column_name = $3)`,
		wsID, object, column).Scan(&ownedHere); err != nil {
		return fmt.Errorf("customfields: probing catalog claim: %w", err)
	}
	if ownedHere {
		return fmt.Errorf("a custom field with column %q already exists on %s in this workspace: %w",
			column, object, apperrors.ErrConflict)
	}
	return &ColumnTakenError{Column: column}
}

// downgradeToAppRole drops the transaction to the DML-only app role for
// everything after the DDL, so the catalog and audit writes run under
// exactly the authority every other tenant write has — RLS forced, no
// owner privilege in reach. SET LOCAL is transaction-scoped: the pooled
// connection reverts to the owner role at COMMIT/ROLLBACK. The role name
// is the scripts/db-init.sql runtime role, the same one the app pool's
// DSN connects as.
//
// This is the arc's privilege boundary, and privilege_boundary_test.go
// pins both call sites (here and in options.go's setOptionsInTx):
// deleting either one leaves the transaction on the owner role for the
// catalog/audit write and every other test still passes, because the
// schema pool's role is superuser in dev — FORCE RLS doesn't bite
// behaviorally there.
func downgradeToAppRole(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE margince_app`); err != nil {
		return fmt.Errorf("customfields: downgrading to the app role: %w", err)
	}
	return nil
}

// optionsJSON renders a picklist's option set for the jsonb catalog
// column; non-picklist fields store NULL (a nil []byte binds as SQL
// NULL), not an empty array.
func optionsJSON(spec FieldSpec) ([]byte, error) {
	if spec.Type != TypePicklist {
		return nil, nil
	}
	return marshalOptions(spec.Options)
}

// marshalOptions is the one spelling of the options jsonb encoding.
func marshalOptions(options []string) ([]byte, error) {
	raw, err := json.Marshal(options)
	if err != nil {
		return nil, fmt.Errorf("customfields: encoding options: %w", err)
	}
	return raw, nil
}
