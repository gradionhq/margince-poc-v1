// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// Automation instance CRUD (B-E15.4): per-workspace rows that
// parameterize the workflow engine's closed catalog. Every mutation is
// RBAC-gated on the `automation` config object and audited in its own
// transaction; the closed catalog (events.md §5) defines no
// automation.* event type, so these writes are ratified audit-only
// (waived with rationale in writeshape_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AutomationStore owns the automation table (tableownership: this
// module) and the catalog lookups the transport needs.
type AutomationStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewAutomationStore(pool *pgxpool.Pool) *AutomationStore {
	return &AutomationStore{pool: pool, now: time.Now}
}

// Automation is one configured instance.
type Automation struct {
	ID        ids.AutomationID
	Key       string
	Name      string
	Enabled   bool
	Params    json.RawMessage
	Version   int64
	CreatedAt time.Time
	UpdatedAt *time.Time
}

// CreateAutomationInput instantiates a catalog key. Created PAUSED per
// the contract — enabling is a deliberate second PATCH.
type CreateAutomationInput struct {
	Key    string
	Name   string
	Params map[string]any
}

// UpdateAutomationInput carries the PATCH subset; nil means unchanged.
type UpdateAutomationInput struct {
	Name      *string
	Params    map[string]any
	Enabled   *bool
	IfVersion *int64
}

// AutomationPage is one keyset page, newest first.
type AutomationPage struct {
	Items      []Automation
	NextCursor string
	HasMore    bool
}

const automationColumns = `id, key, name, enabled, params, version, created_at, updated_at`

func scanAutomation(row pgx.Row) (Automation, error) {
	var a Automation
	err := row.Scan(&a.ID, &a.Key, &a.Name, &a.Enabled, &a.Params, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// List pages the workspace's live instances.
func (s *AutomationStore) List(ctx context.Context, cursor *string, limit *int) (AutomationPage, error) {
	if err := auth.Require(ctx, "automation", principal.ActionRead); err != nil {
		return AutomationPage{}, err
	}
	n := storekit.ClampLimit(limit)
	where := "archived_at IS NULL"
	args := []any{}
	if cursor != nil && *cursor != "" {
		c, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return AutomationPage{}, err
		}
		where += " AND (created_at, id) < ($1, $2)"
		args = append(args, c.CreatedAt, c.ID)
	}
	var page AutomationPage
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, storekit.SQLf(
			`SELECT %s FROM automation WHERE %s ORDER BY created_at DESC, id DESC LIMIT %d`,
			automationColumns, where, n+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAutomation(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, a)
		}
		return rows.Err()
	})
	if err != nil {
		return AutomationPage{}, err
	}
	if len(page.Items) > n {
		page.Items = page.Items[:n]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = storekit.EncodeCursor(last.CreatedAt, last.ID.UUID)
		page.HasMore = true
	}
	return page, nil
}

// Get reads one live instance; an archived or foreign row reads as
// absent.
func (s *AutomationStore) Get(ctx context.Context, id ids.AutomationID) (Automation, error) {
	if err := auth.Require(ctx, "automation", principal.ActionRead); err != nil {
		return Automation{}, err
	}
	var a Automation
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		a, err = scanAutomation(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM automation WHERE id = $1 AND archived_at IS NULL`, automationColumns), id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	if err != nil {
		return Automation{}, err
	}
	return a, nil
}

// Create instantiates a catalog key, always paused. The catalog entry —
// not the request — supplies trigger, action, and tier snapshots.
func (s *AutomationStore) Create(ctx context.Context, in CreateAutomationInput) (Automation, error) {
	if err := auth.Require(ctx, "automation", principal.ActionCreate); err != nil {
		return Automation{}, err
	}
	entry, ok := CatalogEntryByKey(in.Key)
	if !ok {
		return Automation{}, &ParamError{Field: "key", Reason: "not a catalog automation type"}
	}
	if in.Name == "" {
		return Automation{}, &ParamError{Field: "name", Reason: "must not be empty"}
	}
	if err := entry.Validate(in.Params); err != nil {
		return Automation{}, err
	}
	paramsJSON, err := json.Marshal(nonNilParams(in.Params))
	if err != nil {
		return Automation{}, err
	}
	triggerJSON, actionJSON, err := entrySnapshots(entry)
	if err != nil {
		return Automation{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return Automation{}, apperrors.ErrPermissionDenied
	}

	var a Automation
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		a, err = scanAutomation(tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO automation (workspace_id, key, name, origin, trigger, action, params, owner_id, enabled, tier)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, 'catalog', $3, $4, $5, $6, false, $7)
			RETURNING %s`, automationColumns),
			in.Key, in.Name, triggerJSON, actionJSON, paramsJSON, storekit.UUIDOrNil(actor.UserID), entry.Tier))
		if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "automation", a.ID.UUID, nil, map[string]any{
			"key": a.Key, "name": a.Name, "params": in.Params, "status": "paused",
		})
		return err
	})
	if err != nil {
		return Automation{}, err
	}
	return a, nil
}

// Update re-parameterizes, renames, or flips enabled/paused, honoring
// If-Match version skew.
func (s *AutomationStore) Update(ctx context.Context, id ids.AutomationID, in UpdateAutomationInput) (Automation, error) {
	if err := auth.Require(ctx, "automation", principal.ActionUpdate); err != nil {
		return Automation{}, err
	}
	var a Automation
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The row lock makes the state read and the update below one
		// race-free unit.
		if _, err := storekit.LockRow(ctx, tx, "automation", id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		before, err := scanAutomation(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM automation WHERE id = $1 AND archived_at IS NULL`, automationColumns), id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if in.IfVersion != nil && *in.IfVersion != before.Version {
			return apperrors.ErrVersionSkew
		}
		if in.Params != nil {
			entry, ok := CatalogEntryByKey(before.Key)
			if !ok {
				return fmt.Errorf("automation %s names catalog key %q the registry no longer carries", id, before.Key)
			}
			if err := entry.Validate(in.Params); err != nil {
				return err
			}
		}
		var paramsJSON []byte
		if in.Params != nil {
			if paramsJSON, err = json.Marshal(in.Params); err != nil {
				return err
			}
		}
		a, err = scanAutomation(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE automation SET
			  name = coalesce($2, name),
			  params = coalesce($3, params),
			  enabled = coalesce($4, enabled),
			  version = version + 1,
			  updated_at = $5
			WHERE id = $1
			RETURNING %s`, automationColumns),
			id, in.Name, paramsJSON, in.Enabled, s.now().UTC()))
		if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "automation", id.UUID,
			map[string]any{"name": before.Name, "enabled": before.Enabled, "params": json.RawMessage(before.Params)},
			map[string]any{"name": a.Name, "enabled": a.Enabled, "params": json.RawMessage(a.Params)})
		return err
	})
	if err != nil {
		return Automation{}, err
	}
	return a, nil
}

// Archive soft-deletes: the instance stops firing on the next event and
// vanishes from the surface, while its run records keep their referent.
// Audited as `archive` — the vocabulary has no `delete` verb.
func (s *AutomationStore) Archive(ctx context.Context, id ids.AutomationID) error {
	if err := auth.Require(ctx, "automation", principal.ActionDelete); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := scanAutomation(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM automation WHERE id = $1 AND archived_at IS NULL`, automationColumns), id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE automation SET archived_at = $2, enabled = false WHERE id = $1`, id, s.now().UTC()); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "automation", id.UUID,
			map[string]any{"key": before.Key, "name": before.Name, "enabled": before.Enabled}, nil)
		return err
	})
}

// SeedStarterAutomationsTx enrolls the starter library for a fresh
// workspace inside the bootstrap transaction — ENABLED, deliberately:
// the contract's created-paused rule governs user-configured instances;
// a system-seeded floor ("no lead sits unseen") that arrived paused
// would silently not exist. Recorded in this batch's decision file.
func SeedStarterAutomationsTx(ctx context.Context, tx pgx.Tx) error {
	for _, entry := range Catalog() {
		triggerJSON, actionJSON, err := entrySnapshots(entry)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO automation (workspace_id, key, name, origin, trigger, action, params, enabled, tier)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, 'catalog', $3, $4, '{}'::jsonb, true, $5)`,
			entry.Key, entry.Name, triggerJSON, actionJSON, entry.Tier); err != nil {
			return fmt.Errorf("seed automation %s: %w", entry.Key, err)
		}
	}
	return nil
}

// entrySnapshots renders the catalog entry's trigger/action jsonb
// images stored on the instance row (§12.5 shape).
func entrySnapshots(entry CatalogEntry) ([]byte, []byte, error) {
	triggerJSON, err := json.Marshal(map[string]string{"event_type": entry.Trigger})
	if err != nil {
		return nil, nil, err
	}
	actionJSON, err := json.Marshal(map[string]string{"kind": entry.Action})
	if err != nil {
		return nil, nil, err
	}
	return triggerJSON, actionJSON, nil
}

func nonNilParams(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	return params
}
