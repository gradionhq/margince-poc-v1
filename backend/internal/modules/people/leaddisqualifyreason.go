// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Disqualification reasons are the closing vocabulary (lead_disqualify_reason):
// what a rep picks when a lead is dropped, so the "why did we drop this"
// question is later answered from a list rather than from free text.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// CreateLeadDisqualifyReasonInput is one new reason.
type CreateLeadDisqualifyReasonInput struct {
	Label     string
	SortOrder int
}

// UpdateLeadDisqualifyReasonInput is a sparse patch; nil leaves the column alone.
type UpdateLeadDisqualifyReasonInput struct {
	Label     *string
	SortOrder *int
	Active    *bool
}

// leadDisqualifyReasonColumns renders the select list; lead_count carries
// the caller's row scope, like every other lead read.
func leadDisqualifyReasonColumns(ctx context.Context, args *[]any) (string, error) {
	arg := func(v any) int { *args = append(*args, v); return len(*args) }
	scope, err := visibleLeadScope(ctx, arg)
	if err != nil {
		return "", err
	}
	return `id, label, sort_order, active, system, version, created_at, updated_at,
	(SELECT count(*) FROM lead WHERE disqualify_reason_id = lead_disqualify_reason.id AND ` + scope + `)`, nil
}

func scanLeadDisqualifyReason(row pgx.Row) (crmcontracts.LeadDisqualifyReason, error) {
	var out crmcontracts.LeadDisqualifyReason
	var id ids.UUID
	var system bool
	var version int64
	var count int
	if err := row.Scan(&id, &out.Label, &out.SortOrder, &out.Active, &system, &version,
		&out.CreatedAt, &out.UpdatedAt, &count); err != nil {
		return out, err
	}
	out.Id = openapi_types.UUID(id)
	out.System = &system
	out.Version = &version
	out.LeadCount = &count
	return out, nil
}

// readLeadDisqualifyReason reads one row; lock takes it FOR UPDATE first so
// a patch built from the read cannot be overtaken by a concurrent writer.
func readLeadDisqualifyReason(ctx context.Context, tx pgx.Tx, id ids.UUID, lock bool) (crmcontracts.LeadDisqualifyReason, error) {
	if lock {
		if _, err := storekit.LockRow(ctx, tx, "lead_disqualify_reason", id, storekit.NoArchiveColumn); err != nil {
			return crmcontracts.LeadDisqualifyReason{}, err
		}
	}
	args := []any{id}
	cols, err := leadDisqualifyReasonColumns(ctx, &args)
	if err != nil {
		return crmcontracts.LeadDisqualifyReason{}, err
	}
	out, err := scanLeadDisqualifyReason(tx.QueryRow(ctx,
		`SELECT `+cols+` FROM lead_disqualify_reason WHERE id = $1`, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, apperrors.ErrNotFound
	}
	return out, err
}

// UnknownDisqualifyReasonError refuses a reason that is not an active row:
// the dialog lists active reasons, and the API agrees with the dialog.
type UnknownDisqualifyReasonError struct{}

func (e *UnknownDisqualifyReasonError) Error() string {
	return "reason_id is not an active disqualification reason; pick one from the list"
}

// FieldFault names reason_id as the invalid input.
func (e *UnknownDisqualifyReasonError) FieldFault() (field, code, message string) {
	return "reason_id", "unknown_reason", e.Error()
}

// ensureActiveDisqualifyReason is the disqualify path's check that the
// reason it is about to record is one a rep could have picked.
func ensureActiveDisqualifyReason(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT active FROM lead_disqualify_reason WHERE id = $1`, id).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return &UnknownDisqualifyReasonError{}
	}
	return err
}

// ListLeadDisqualifyReasons answers the whole list in display order.
func (s *Store) ListLeadDisqualifyReasons(ctx context.Context) ([]crmcontracts.LeadDisqualifyReason, error) {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionRead); err != nil {
		return nil, err
	}
	out := []crmcontracts.LeadDisqualifyReason{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var args []any
		cols, err := leadDisqualifyReasonColumns(ctx, &args)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+cols+` FROM lead_disqualify_reason ORDER BY sort_order, label, id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			reason, err := scanLeadDisqualifyReason(rows)
			if err != nil {
				return err
			}
			out = append(out, reason)
		}
		return rows.Err()
	})
	return out, err
}

// CreateLeadDisqualifyReason adds a reason; a blank label is a 422.
func (s *Store) CreateLeadDisqualifyReason(ctx context.Context, in CreateLeadDisqualifyReasonInput) (crmcontracts.LeadDisqualifyReason, error) {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionCreate); err != nil {
		return crmcontracts.LeadDisqualifyReason{}, err
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		return crmcontracts.LeadDisqualifyReason{}, &values.ParseError{Field: "label", Code: codeRequired, Message: "label is required"}
	}
	var out crmcontracts.LeadDisqualifyReason
	err := s.tx(ctx, func(tx pgx.Tx) error {
		id := ids.NewV7()
		if _, err := tx.Exec(ctx,
			`INSERT INTO lead_disqualify_reason (id, label, sort_order) VALUES ($1, $2, $3)`,
			id, label, in.SortOrder); err != nil {
			return fmt.Errorf("insert lead disqualify reason: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "create", "lead_disqualify_reason", id, nil, map[string]any{"label": label}); err != nil {
			return err
		}
		var err error
		out, err = readLeadDisqualifyReason(ctx, tx, id, false)
		return err
	})
	return out, err
}

// UpdateLeadDisqualifyReason renames, reorders or (de)activates a reason.
func (s *Store) UpdateLeadDisqualifyReason(ctx context.Context, id ids.UUID, in UpdateLeadDisqualifyReasonInput) (crmcontracts.LeadDisqualifyReason, error) {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionUpdate); err != nil {
		return crmcontracts.LeadDisqualifyReason{}, err
	}
	if in.Label != nil && strings.TrimSpace(*in.Label) == "" {
		return crmcontracts.LeadDisqualifyReason{}, &values.ParseError{Field: "label", Code: codeRequired, Message: "label must not be empty"}
	}
	var out crmcontracts.LeadDisqualifyReason
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := readLeadDisqualifyReason(ctx, tx, id, true)
		if err != nil {
			return err
		}
		p := storekit.NewPatch()
		if in.Label != nil {
			p.Set("label", before.Label, strings.TrimSpace(*in.Label))
		}
		if in.SortOrder != nil {
			p.Set("sort_order", before.SortOrder, *in.SortOrder)
		}
		if in.Active != nil {
			p.Set("active", before.Active, *in.Active)
		}
		if p.Empty() {
			out = before
			return nil
		}
		if err := p.ApplyGuardedIn(ctx, tx, "lead_disqualify_reason", id, nil, storekit.NoArchiveColumn); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "lead_disqualify_reason", id, p.Before(), p.After()); err != nil {
			return err
		}
		out, err = readLeadDisqualifyReason(ctx, tx, id, false)
		return err
	})
	return out, err
}

// DeleteLeadDisqualifyReason removes a reason no lead points at. A built-in
// or an in-use reason is a 409 (the FK is RESTRICT as well): deactivate it.
func (s *Store) DeleteLeadDisqualifyReason(ctx context.Context, id ids.UUID) error {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionDelete); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readLeadDisqualifyReason(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if (current.System != nil && *current.System) || (current.LeadCount != nil && *current.LeadCount > 0) {
			return apperrors.ErrConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM lead_disqualify_reason WHERE id = $1`, id); err != nil {
			if storekit.IsForeignKeyViolation(err) {
				return apperrors.ErrConflict
			}
			return fmt.Errorf("delete lead disqualify reason: %w", err)
		}
		_, err = storekit.Audit(ctx, tx, "erase", "lead_disqualify_reason", id, map[string]any{"label": current.Label}, nil)
		return err
	})
}
