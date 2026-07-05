// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The inbox read side: the store row shape and the List/Get queries.
// Every read here runs through decidable (authority.go), so triage
// visibility and the decision gate can never drift apart.

package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// row is the store shape of one approval.
type row struct {
	ID             ids.UUID
	Kind           string
	Status         string
	ProposedBy     string
	OnBehalfOf     *ids.UUID
	PassportID     *ids.UUID
	TargetType     *string
	TargetID       *ids.UUID
	TargetVersion  *int64
	Summary        *string
	ProposedChange json.RawMessage
	DiffHash       string
	ExpiresAt      time.Time
	DecidedBy      *ids.UUID
	DecidedAt      *time.Time
	ConsumedAt     *time.Time
	CreatedAt      time.Time
}

const columns = `id, kind, status, proposed_by, on_behalf_of, passport_id,
	target_entity_type, target_entity_id, target_version, summary,
	proposed_change, diff_hash, expires_at, decided_by, decided_at, consumed_at, created_at`

func scan(r pgx.Row) (row, error) {
	var a row
	err := r.Scan(&a.ID, &a.Kind, &a.Status, &a.ProposedBy, &a.OnBehalfOf, &a.PassportID,
		&a.TargetType, &a.TargetID, &a.TargetVersion, &a.Summary,
		&a.ProposedChange, &a.DiffHash, &a.ExpiresAt, &a.DecidedBy, &a.DecidedAt, &a.ConsumedAt, &a.CreatedAt)
	return a, err
}

// effectiveStatus folds lazy expiry in: a pending row past its expiry
// reads as expired everywhere without a sweeper process.
func (a row) effectiveStatus(now time.Time) string {
	if a.Status == "pending" && now.After(a.ExpiresAt) {
		return "expired"
	}
	return a.Status
}

// inboxBatch is the scan window List filters per round trip; List keeps
// paging until the display limit is met or the table is exhausted, so a
// burst of undecidable stagings can never starve older visible rows out
// of a caller's inbox.
const inboxBatch = 200

// List returns the inbox, newest first — but only the approvals the caller
// could themselves decide. Deciding is human work, and so is triage: an
// agent cannot browse the queue of withheld authority, and neither can a
// human who lacks the grant the staged effect needs or cannot see the
// target row under their own/team scope. Without this filter the inbox is
// a workspace-wide side channel that leaks proposed_change, target ids,
// and diffs to any low-privilege user (C3/ADR-0036).
func (s *Service) List(ctx context.Context, status *string, limit int) ([]row, error) {
	if err := humanOnly(ctx); err != nil {
		return nil, err
	}
	p, _ := principal.Actor(ctx)
	if limit <= 0 || limit > inboxBatch {
		limit = 50
	}
	var out []row
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Decidability is role/target/row-scope-shaped, not expressible as
		// one WHERE without joining every object grant — so scan keyset
		// batches and filter in memory until the display limit fills or the
		// table runs out.
		var afterCreated *time.Time
		var afterID *ids.UUID
		for {
			q := `SELECT ` + columns + ` FROM approval`
			args := []any{}
			arg := func(v any) int { args = append(args, v); return len(args) }
			where := []string{}
			if status != nil {
				where = append(where, fmt.Sprintf("status = $%d", arg(*status)))
			}
			if afterCreated != nil {
				where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", arg(*afterCreated), arg(*afterID)))
			}
			for i, w := range where {
				if i == 0 {
					q += " WHERE " + w
				} else {
					q += " AND " + w
				}
			}
			q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, inboxBatch)

			batch, err := collect(ctx, tx, q, args)
			if err != nil {
				return err
			}
			for i := range batch {
				a := batch[i]
				visible, err := decidable(ctx, tx, p, a)
				if err != nil {
					return err
				}
				if !visible {
					continue
				}
				out = append(out, a)
				if len(out) >= limit {
					return nil
				}
			}
			if len(batch) < inboxBatch {
				return nil // table exhausted
			}
			last := batch[len(batch)-1]
			afterCreated, afterID = &last.CreatedAt, &last.ID
		}
	})
	return out, err
}

// collect materializes one query's rows (the row-scope probes inside the
// filter loop need the connection, so the cursor cannot stay open).
func collect(ctx context.Context, tx pgx.Tx, q string, args []any) ([]row, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id ids.UUID) (row, error) {
	if err := humanOnly(ctx); err != nil {
		return row{}, err
	}
	p, _ := principal.Actor(ctx)
	var a row
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		a, err = get(ctx, tx, id)
		if err != nil {
			return err
		}
		// An approval the caller could not decide reads as absent — the
		// same existence-hiding the row-scope convention uses, so Get never
		// becomes a lookup oracle for out-of-scope proposed changes (C3),
		// whether the gap is a missing grant or a target row outside the
		// caller's row scope.
		visible, err := decidable(ctx, tx, p, a)
		if err != nil {
			return err
		}
		if !visible {
			return apperrors.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return row{}, err
	}
	return a, nil
}

func get(ctx context.Context, tx pgx.Tx, id ids.UUID) (row, error) {
	a, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM approval WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, apperrors.ErrNotFound
	}
	return a, err
}
