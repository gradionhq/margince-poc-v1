// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Pre-capture exclusions: the addresses and domains whose mail the CRM must
// not store at all. A workspace exclusion is the installation's rule and takes
// admin/ops to change; a user exclusion is one person's boundary for the
// mailbox they connected, theirs alone to set and to lift, and binds only the
// connections they granted. Both are read by the sink before any write
// (excludedTx), so a matching message leaves a breadcrumb and a trace that
// names the rule kind — never the address — and nothing else.

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The exclusion vocabulary, the Go spelling of the table's CHECKs.
const (
	ExclusionScopeWorkspace = "workspace"
	ExclusionScopeUser      = "user"
	ExclusionKindAddress    = "address"
	ExclusionKindDomain     = "domain"
)

// auditKeyExclusion is the audit-image key naming which rule a change was
// about; the value is the folded address or domain, which is what the trail
// is searched by when somebody asks why a message never appeared.
const auditKeyExclusion = "capture_exclusion"

// Exclusion is one rule as stored.
type Exclusion struct {
	ID        ids.UUID
	Scope     string
	UserID    *ids.UUID
	Kind      string
	Value     string
	CreatedAt time.Time
}

// ExclusionStore reads and writes the exclusion lists.
type ExclusionStore struct {
	db *database.DB
}

// NewExclusionStore builds the store over the app pool.
func NewExclusionStore(db *database.DB) *ExclusionStore {
	return &ExclusionStore{db: db}
}

// List answers the workspace's rules and the caller's own: a reader learns
// every rule that binds THEIR connections, and no colleague's personal ones.
func (s *ExclusionStore) List(ctx context.Context) ([]Exclusion, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return nil, err
	}
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return nil, err
	}
	var out []Exclusion
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, scope, user_id, kind, value, created_at
			  FROM capture_exclusion
			 WHERE scope = 'workspace' OR user_id = $1
			 ORDER BY scope, kind, value`, actor.UserID)
		if err != nil {
			return fmt.Errorf("capture: listing exclusions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e Exclusion
			if err := rows.Scan(&e.ID, &e.Scope, &e.UserID, &e.Kind, &e.Value, &e.CreatedAt); err != nil {
				return fmt.Errorf("capture: listing exclusions: %w", err)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// Add records one rule. A workspace rule takes the capture-settings update
// grant (admin/ops); a user rule takes only a human seat, and names the
// caller. Idempotent on the folded value: re-adding answers the existing row.
func (s *ExclusionStore) Add(ctx context.Context, scope, kind, raw string) (Exclusion, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return Exclusion{}, err
	}
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return Exclusion{}, err
	}
	var userID *ids.UUID
	switch scope {
	case ExclusionScopeWorkspace:
		if err := auth.Require(ctx, captureSettingsObject, principal.ActionUpdate); err != nil {
			return Exclusion{}, err
		}
	case ExclusionScopeUser:
		me := actor.UserID
		userID = &me
	default:
		return Exclusion{}, &InvalidExclusionError{Field: "scope", Reason: "scope is workspace or user"}
	}
	value, err := ValidExclusionValue(kind, raw)
	if err != nil {
		return Exclusion{}, err
	}
	var out Exclusion
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO capture_exclusion (scope, user_id, kind, value, created_by)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (scope, coalesce(user_id, '00000000-0000-0000-0000-000000000000'::uuid), kind, value)
			  DO UPDATE SET value = EXCLUDED.value
			RETURNING id, scope, user_id, kind, value, created_at`,
			scope, userID, kind, value, actor.ID).
			Scan(&out.ID, &out.Scope, &out.UserID, &out.Kind, &out.Value, &out.CreatedAt); err != nil {
			return fmt.Errorf("capture: adding an exclusion: %w", err)
		}
		// Audit-only, like the own-domain list beside it: this is capture
		// configuration, and the closed event catalog carries no type for it.
		_, err := storekit.Audit(ctx, tx, "update", captureSettingsObject, storekit.MustWorkspace(ctx),
			nil, map[string]any{auditKeyExclusion: value, "scope": scope, "kind": kind})
		return err
	})
	return out, err
}

// Remove lifts one rule: a workspace rule by admin/ops, a user rule by the
// user it names. Somebody else's user rule is not theirs to see, so it
// answers not-found like a row that is not there.
func (s *ExclusionStore) Remove(ctx context.Context, id ids.UUID) error {
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var e Exclusion
		err := tx.QueryRow(ctx, `SELECT id, scope, user_id, kind, value FROM capture_exclusion WHERE id = $1`, id).
			Scan(&e.ID, &e.Scope, &e.UserID, &e.Kind, &e.Value)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("capture: reading an exclusion: %w", err)
		}
		if e.Scope == ExclusionScopeWorkspace {
			if err := auth.Require(ctx, captureSettingsObject, principal.ActionUpdate); err != nil {
				return err
			}
		} else if e.UserID == nil || *e.UserID != actor.UserID {
			return apperrors.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM capture_exclusion WHERE id = $1`, id); err != nil {
			return fmt.Errorf("capture: removing an exclusion: %w", err)
		}
		// "archive", not a delete verb: the audit vocabulary is closed and
		// lifting a rule IS retiring it, as the own-domain list reads it.
		_, err = storekit.Audit(ctx, tx, "archive", captureSettingsObject, storekit.MustWorkspace(ctx),
			map[string]any{auditKeyExclusion: e.Value, "scope": e.Scope, "kind": e.Kind}, nil)
		return err
	})
}

// ValidExclusionValue vets one rule's value and returns its stored form: a
// lowercased address, or an IDNA ASCII domain through the own-domain vetting.
func ValidExclusionValue(kind, raw string) (string, error) {
	switch kind {
	case ExclusionKindAddress:
		addr, err := mail.ParseAddress(strings.TrimSpace(raw))
		if err != nil || addr.Address == "" || !strings.Contains(addr.Address, "@") {
			return "", &InvalidExclusionError{Field: "value", Reason: "give one email address, for example name@example.com"}
		}
		return strings.ToLower(addr.Address), nil
	case ExclusionKindDomain:
		domain, err := ValidOwnDomain(raw)
		if err != nil {
			return "", &InvalidExclusionError{Field: "value", Reason: err.Error()}
		}
		return domain, nil
	}
	return "", &InvalidExclusionError{Field: "kind", Reason: "kind is address or domain"}
}

// InvalidExclusionError is a malformed rule; it answers 422 naming the field.
type InvalidExclusionError struct{ Field, Reason string }

func (e *InvalidExclusionError) Error() string { return "capture exclusion: " + e.Reason }

// FieldFault maps the refusal onto the wire.
func (e *InvalidExclusionError) FieldFault() (field, code, message string) {
	return e.Field, "invalid_exclusion", e.Reason
}
