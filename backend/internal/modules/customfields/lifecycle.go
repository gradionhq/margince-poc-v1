// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

// Rename and Retire — the two catalog-only lifecycle mutations. Neither
// runs DDL, so both stay on the RLS-bound app pool inside
// database.WithWorkspaceTx; only Create and SetOptions need the schema
// pool's owner-ALTER-then-downgrade shape (create.go, options.go).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// lockedField is the FOR UPDATE snapshot Rename/Retire/SetOptions decide
// from: the lock is taken BEFORE any decision read, so the snapshot
// cannot go stale under a concurrent writer.
type lockedField struct {
	Object     string
	ColumnName string
	Type       string
	Status     string
	Label      string
	OptionsRaw []byte
	Version    int64
}

// mutable is the status gate on the two catalog mutations a retired
// field no longer accepts — rename (Rename, after lockField) and the
// options edit (lockPicklistField): retirement is terminal, so label and
// options are frozen while the row stays fetchable and a repeat retire
// stays a no-op (which is why the gate is not inside lockField itself —
// Retire needs the retired row to answer it unchanged).
func (f lockedField) mutable() error {
	if f.Status == statusRetired {
		return ErrFieldRetired
	}
	return nil
}

// lockField takes FOR UPDATE on one catalog row. The custom_field
// catalog is workspace-shared admin config with no owner_id — the object
// grant is the whole authority question (the pipeline precedent), so
// there is no row-scope probe to add; RLS pins the
// workspace. A missing row answers ErrNotFound.
func lockField(ctx context.Context, tx pgx.Tx, id ids.UUID) (lockedField, error) {
	var f lockedField
	err := tx.QueryRow(ctx,
		`SELECT object, column_name, type, status, label, options, version
		   FROM custom_field WHERE id = $1 FOR UPDATE`,
		id).Scan(&f.Object, &f.ColumnName, &f.Type, &f.Status, &f.Label, &f.OptionsRaw, &f.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedField{}, apperrors.ErrNotFound
	}
	if err != nil {
		return lockedField{}, fmt.Errorf("customfields: locking catalog row: %w", err)
	}
	return f, nil
}

// readField re-reads the full row after a mutation, inside the same
// transaction, so the response carries the trigger-bumped version.
func readField(ctx context.Context, tx pgx.Tx, id ids.UUID) (crmcontracts.CustomField, error) {
	out, err := scanCustomField(tx.QueryRow(ctx,
		`SELECT `+catalogColumns+` FROM custom_field WHERE id = $1`, id))
	if err != nil {
		return crmcontracts.CustomField{}, fmt.Errorf("customfields: reading catalog row: %w", err)
	}
	return out, nil
}

// Rename updates the catalog label ONLY (CUSTOM-FIELDS-WIRE-3): slug,
// column_name, object and type never move — the physical column identity
// is stable across rename. A retired field refuses rename (ErrFieldRetired,
// 409): retirement is terminal. If-Match is the contract's optional CAS: a
// stale version answers ErrVersionSkew; without one the held row lock
// still serializes concurrent writers.
func (s *Service) Rename(ctx context.Context, id ids.UUID, label string, ifVersion *int64) (crmcontracts.CustomField, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionUpdate); err != nil {
		return crmcontracts.CustomField{}, err
	}
	if strings.TrimSpace(label) == "" {
		return crmcontracts.CustomField{}, &ValidationError{Errors: []FieldError{{Field: fieldLabel, Code: codeRequired}}}
	}

	var out crmcontracts.CustomField
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := lockField(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := f.mutable(); err != nil {
			return err
		}
		if ifVersion != nil && *ifVersion != f.Version {
			return apperrors.ErrVersionSkew
		}
		if _, err := tx.Exec(ctx,
			`UPDATE custom_field SET label = $1 WHERE id = $2`, label, id); err != nil {
			return fmt.Errorf("customfields: updating label: %w", err)
		}
		if out, err = readField(ctx, tx, id); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", rbacObject, id,
			map[string]any{fieldLabel: f.Label}, map[string]any{fieldLabel: label}); err != nil {
			return fmt.Errorf("customfields: audit write: %w", err)
		}
		return nil
	})
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	return out, nil
}

// Retire flips status to retired (CUSTOM-FIELDS-WIRE-4/AC-13): the field
// disappears from record payloads and the sort/filter vocabulary while
// the physical column and every value in it are preserved — never a
// DROP, and archived_at stays null (retire is a status flip on a
// still-fetchable row, not an archive). Retiring an already-retired
// field is a no-op that returns the row unchanged: nothing changed, so
// nothing is written to the audit trail.
func (s *Service) Retire(ctx context.Context, id ids.UUID) (crmcontracts.CustomField, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionUpdate); err != nil {
		return crmcontracts.CustomField{}, err
	}

	var out crmcontracts.CustomField
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := lockField(ctx, tx, id)
		if err != nil {
			return err
		}
		if f.Status == statusRetired {
			out, err = readField(ctx, tx, id)
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE custom_field SET status = $1 WHERE id = $2`, statusRetired, id); err != nil {
			return fmt.Errorf("customfields: updating status: %w", err)
		}
		if out, err = readField(ctx, tx, id); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", rbacObject, id,
			map[string]any{fieldStatus: f.Status}, map[string]any{fieldStatus: statusRetired}); err != nil {
			return fmt.Errorf("customfields: audit write: %w", err)
		}
		return nil
	})
	if err != nil {
		return crmcontracts.CustomField{}, err
	}
	return out, nil
}
