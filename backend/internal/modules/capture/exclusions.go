// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The per-user personal-mail exclusion rule set (RC-2, capture.md
// CAP-DDL-3 / CAP-WIRE-2): the CRUD a human runs over their own bounded
// rules, plus RulesFor — the loader the ONE Sink calls to gate ingestion
// before any write. The pure match logic lives in capture/exclusion; this
// file is only the store (reads/writes), never the matching.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture/exclusion"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ExclusionRule is one of the caller's stored rules, for the CRUD surface.
type ExclusionRule struct {
	ID        ids.UUID
	Kind      string
	Value     string
	CreatedAt time.Time
}

// Exclusions is the store over the RC-2 rule set. It holds only the pool;
// tenancy is RLS (the workspace GUC) and the per-user scope is granted_by
// the acting human — capture is per-user (RC-2, like RC-8's connection).
type Exclusions struct {
	pool *pgxpool.Pool
}

// NewExclusions builds the exclusion-rule store over the pool.
func NewExclusions(pool *pgxpool.Pool) *Exclusions { return &Exclusions{pool: pool} }

// List returns the calling human's own exclusion rules in the current
// workspace (RLS scopes the workspace; user_id scopes the human).
func (e *Exclusions) List(ctx context.Context) ([]ExclusionRule, error) {
	actor, err := requireHuman(ctx)
	if err != nil {
		return nil, err
	}
	var out []ExclusionRule
	err = database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, kind, value, created_at FROM capture_exclusion_rule
			WHERE user_id = $1 AND archived_at IS NULL
			ORDER BY created_at`, actor.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ExclusionRule
			if err := rows.Scan(&r.ID, &r.Kind, &r.Value, &r.CreatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: listing exclusion rules: %w", err)
	}
	return out, nil
}

// Create adds one bounded rule for the calling human. Idempotent on
// (workspace, user, kind, value): re-adding an existing rule returns the
// existing row and writes no duplicate. The value is normalized — domains
// lowercased, labels trimmed — so the idempotency key is stable.
func (e *Exclusions) Create(ctx context.Context, kind, value string) (ExclusionRule, error) {
	actor, err := requireHuman(ctx)
	if err != nil {
		return ExclusionRule{}, err
	}
	// The transport validates kind/value for a clean 422; the DB CHECK on
	// `kind` is the backstop. Here we only normalize so the idempotency key
	// is stable.
	kind = strings.TrimSpace(kind)
	value = normalizeValue(kind, value)
	var r ExclusionRule
	err = database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		// DO UPDATE (a no-op re-set of archived_at) so the RETURNING clause
		// yields the existing row on an idempotent re-add — DO NOTHING would
		// return nothing.
		return tx.QueryRow(ctx, `
			INSERT INTO capture_exclusion_rule (workspace_id, user_id, kind, value)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
			ON CONFLICT (workspace_id, user_id, kind, value)
			DO UPDATE SET archived_at = NULL
			RETURNING id, kind, value, created_at`,
			actor.UserID, kind, value).Scan(&r.ID, &r.Kind, &r.Value, &r.CreatedAt)
	})
	if err != nil {
		return ExclusionRule{}, fmt.Errorf("capture: creating exclusion rule: %w", err)
	}
	return r, nil
}

// Delete removes one of the calling human's own rules by id. Idempotent —
// a missing or already-removed rule is a no-op, not an error (204).
func (e *Exclusions) Delete(ctx context.Context, id ids.UUID) error {
	actor, err := requireHuman(ctx)
	if err != nil {
		return err
	}
	err = database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			DELETE FROM capture_exclusion_rule WHERE id = $1 AND user_id = $2`,
			id, actor.UserID)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: deleting exclusion rule: %w", err)
	}
	return nil
}

// RulesFor loads the exclusion rules for one user in the current workspace,
// for the pre-ingestion gate. The Sink calls it under the connector
// principal, so the user is the granting human (OnBehalfOf); the read is
// RLS-scoped to the workspace and filtered to that user's live rules.
func (e *Exclusions) RulesFor(ctx context.Context, userID ids.UUID) ([]exclusion.Rule, error) {
	var out []exclusion.Rule
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT kind, value FROM capture_exclusion_rule
			WHERE user_id = $1 AND archived_at IS NULL`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r exclusion.Rule
			if err := rows.Scan(&r.Kind, &r.Value); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: loading exclusion rules for the gate: %w", err)
	}
	return out, nil
}

// requireHuman resolves the acting human — managing one's own personal-mail
// boundary is a human-only act (an agent must not widen or narrow it).
func requireHuman(ctx context.Context) (principal.Principal, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return principal.Principal{}, fmt.Errorf("capture: managing exclusion rules is a human-only action: %w", apperrors.ErrPermissionDenied)
	}
	return actor, nil
}

// normalizeValue lowercases domains (case-insensitive by nature) and trims
// labels, so the idempotency key is stable regardless of caller casing.
func normalizeValue(kind, value string) string {
	value = strings.TrimSpace(value)
	switch kind {
	case exclusion.KindSenderDomain, exclusion.KindRecipientDomain:
		return strings.ToLower(value)
	default:
		return value
	}
}
