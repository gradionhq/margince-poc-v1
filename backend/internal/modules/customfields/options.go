// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SetOptions replaces a picklist field's allowed option set and
// regenerates the physical column's CHECK constraint from it
// (CUSTOM-FIELDS-PARAM-5) — the one lifecycle mutation besides Create
// that runs DDL, so it follows the same one-transaction schema-pool
// shape (decisions/0024): owner ALTER first, then the downgrade to the
// app role for the RLS-governed catalog UPDATE + one audit row.
func (s *Service) SetOptions(ctx context.Context, id ids.UUID, options []string) (crmcontracts.CustomField, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionUpdate); err != nil {
		return crmcontracts.CustomField{}, err
	}
	if len(options) == 0 {
		return crmcontracts.CustomField{}, ErrLastOption
	}
	for _, o := range options {
		if !validOptionText(o) {
			return crmcontracts.CustomField{}, &ValidationError{Errors: []FieldError{{Field: fieldOptions, Code: codeInvalidCharacters}}}
		}
	}
	if s.schemaPool == nil {
		return crmcontracts.CustomField{}, ErrSchemaChangesUnavailable
	}
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return crmcontracts.CustomField{}, errors.New("customfields: no workspace bound to context")
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

	out, err := s.setOptionsInTx(ctx, tx, wsID, id, options)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: commit schema tx: %w", err)
	}
	return out, nil
}

// lockPicklistField binds the workspace GUC, takes FOR UPDATE on the
// catalog row, and refuses a non-picklist target. The GUC binds first so
// the read is workspace-scoped even where the owner role is subject to
// the catalog's FORCE RLS; the explicit workspace predicate keeps it
// correct where it is not (a superuser owner in dev).
func lockPicklistField(ctx context.Context, tx pgx.Tx, wsID ids.UUID, id ids.UUID) (lockedField, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID.String()); err != nil {
		return lockedField{}, fmt.Errorf("customfields: binding workspace GUC: %w", err)
	}
	var f lockedField
	err := tx.QueryRow(ctx,
		`SELECT object, column_name, type, status, label, options, version
		   FROM custom_field WHERE id = $1 AND workspace_id = $2 FOR UPDATE`,
		id, wsID).Scan(&f.Object, &f.ColumnName, &f.Type, &f.Status, &f.Label, &f.OptionsRaw, &f.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedField{}, apperrors.ErrNotFound
	}
	if err != nil {
		return lockedField{}, fmt.Errorf("customfields: locking catalog row: %w", err)
	}
	if f.Type != TypePicklist {
		return lockedField{}, ErrNotPicklist
	}
	return f, nil
}

// setOptionsInTx is SetOptions' transaction body: GUC + row lock →
// picklist check → advisory lock → CHECK regeneration (owner) →
// downgrade → catalog UPDATE + audit.
func (s *Service) setOptionsInTx(ctx context.Context, tx pgx.Tx, wsID ids.UUID, id ids.UUID, options []string) (crmcontracts.CustomField, error) {
	f, err := lockPicklistField(ctx, tx, wsID, id)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	ddl, err := BuildOptionsDDL(f.Object, f.ColumnName, options)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}

	// Same serialization as Create: one ALTER per core table at a time.
	// Lock order is row-then-advisory in every flow that holds both, so
	// the two DDL paths cannot deadlock each other.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('customfields:' || $1, 0))`, f.Object); err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: serializing schema change: %w", err)
	}
	if _, err := tx.Exec(ctx, ddl); err != nil {
		if _, ok := storekit.CheckViolation(err); ok {
			// ADD CONSTRAINT validates existing rows: a removed option that
			// records still carry refuses the edit rather than stranding
			// data outside its own CHECK.
			return crmcontracts.CustomField{}, fmt.Errorf(
				"existing %s values still use a removed option; migrate them first: %w", f.Object, apperrors.ErrConflict)
		}
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: regenerating options CHECK: %w", err)
	}

	if err := downgradeToAppRole(ctx, tx); err != nil {
		return crmcontracts.CustomField{}, err
	}
	optionsArg, err := marshalOptions(options)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE custom_field SET options = $1 WHERE id = $2`, optionsArg, id); err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: updating options: %w", err)
	}
	out, err := readField(ctx, tx, id)
	if err != nil {
		return crmcontracts.CustomField{}, err
	}

	var oldOptions []string
	if len(f.OptionsRaw) > 0 {
		if err := json.Unmarshal(f.OptionsRaw, &oldOptions); err != nil {
			return crmcontracts.CustomField{}, fmt.Errorf("customfields: catalog options column is not a JSON string array: %w", err)
		}
	}
	if _, err := storekit.Audit(ctx, tx, "update", rbacObject, id,
		map[string]any{fieldOptions: oldOptions}, map[string]any{fieldOptions: options}); err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: audit write: %w", err)
	}
	return out, nil
}
