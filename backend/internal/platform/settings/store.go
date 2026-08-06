// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Registry is the assembled catalog. Compose builds exactly one from every
// module's declarations; nothing mutates it after the store is constructed,
// so a concurrent read needs no lock.
type Registry struct {
	byKey map[string]Definition
}

// NewRegistry assembles the catalog.
//
// It takes no error return, and a duplicate key is NOT guarded here. Two
// modules claiming one setting is a compile-time-static defect — the
// declarations are package vars, so the same duplicate exists in every build
// or in none. Guarding it at wiring time would mean every call site carrying
// an error path for a condition that a test can rule out entirely, so the
// obligation is derived from the system instead: settingscatalog_test.go
// walks the assembled catalog and fails the build on a repeated key. That is
// the same trade the arch and table-ownership gates already make.
func NewRegistry(defs ...Definition) *Registry {
	byKey := make(map[string]Definition, len(defs))
	for _, d := range defs {
		byKey[d.Key()] = d
	}
	return &Registry{byKey: byKey}
}

// Store reads and writes settings.
//
// Both entry points are methods on this type, deliberately. The generic
// helpers below are thin typed wrappers over them, because Go forbids generic
// methods and a package-level generic function is invisible to
// rbacgate_test.go's store-entry-point shape — which would leave the ONE
// table in the schema with no RLS beneath it governed by a gate no fitness
// function checks.
type Store struct {
	pool *pgxpool.Pool
	reg  *Registry
}

// New builds the store over the pool and the assembled registry.
func New(pool *pgxpool.Pool, reg *Registry) *Store { return &Store{pool: pool, reg: reg} }

// Raw returns the stored value for a key, or the registered default when no
// row exists. An unregistered key is an error — a typo must not read as
// "unset and therefore default".
func (s *Store) Raw(ctx context.Context, key string) (json.RawMessage, error) {
	def, err := s.lookup(key)
	if err != nil {
		return nil, err
	}
	if err := auth.Require(ctx, def.Object(), principal.ActionRead); err != nil {
		return nil, err
	}
	// `setting` is non-tenant and carries no RLS, so the workspace GUC buys
	// this read nothing directly. It still rides WithWorkspaceTx because the
	// WRITE path must (its audit row is stamped with the workspace), and one
	// transaction helper across both keeps the store honest about needing a
	// resolved principal — a settings read with no workspace bound is a caller
	// that has not authenticated, which the gate above should be judging.
	var raw json.RawMessage
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT value FROM setting WHERE key = $1`, key).Scan(&raw)
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Unset is not missing: the registered default IS the value until a
		// human changes it, which is what lets a new setting ship without a
		// backfill of every installation.
		return def.DefaultJSON()
	case err != nil:
		return nil, fmt.Errorf("settings: reading %s: %w", key, err)
	}
	return raw, nil
}

// SetRaw writes a setting, committing the row + audit in ONE transaction like
// every other mutation. No event: the closed event catalog defines no
// settings verb (EVT-NOEVT-3), the same ruling the capture-settings and
// fx-rate config writes already carry.
//
// An unchanged value is a no-op — no write, no audit row — because an
// idempotent PATCH should not litter the ledger.
func (s *Store) SetRaw(ctx context.Context, key string, next json.RawMessage) error {
	// Through the registry, not off a caller-supplied entry: an entry a module
	// declares but compose never registers would otherwise be writable while
	// invisible to every catalog gate — and unreadable through Raw, which does
	// resolve through the registry.
	def, err := s.lookup(key)
	if err != nil {
		return err
	}
	if err := auth.Require(ctx, def.Object(), principal.ActionUpdate); err != nil {
		return err
	}
	if err := def.ValidateJSON(next); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := currentJSON(ctx, tx, def)
		if err != nil {
			return err
		}
		canonical, err := def.CanonicalJSON(before)
		if err != nil {
			return err
		}
		if string(canonical) == string(next) {
			return nil
		}
		// Probed only for a REAL change: re-asserting the value a frozen
		// setting already holds is a no-op, and refusing it would make an
		// idempotent PATCH fail for a caller changing something else.
		frozen, why, err := def.Frozen(ctx, tx)
		if err != nil {
			return fmt.Errorf("settings: probing %s: %w", key, err)
		}
		if frozen {
			return FrozenValue{Setting: key, Reason: why}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO setting (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
			key, next); err != nil {
			return fmt.Errorf("settings: writing %s: %w", key, err)
		}
		if _, err := storekit.Audit(ctx, tx, def.AuditVerb(), def.Object(), storekit.MustWorkspace(ctx),
			map[string]any{key: json.RawMessage(before)},
			map[string]any{key: json.RawMessage(next)}); err != nil {
			return fmt.Errorf("settings: auditing %s: %w", key, err)
		}
		return nil
	})
}

// lookup resolves a key to its declaration, refusing an unregistered one.
func (s *Store) lookup(key string) (Definition, error) { //nolint:ireturn // returns the type-erased Definition by design — the registry holds entries of many value types, and the concrete Entry[T] cannot be named without the type parameter the caller is looking up
	def, ok := s.reg.byKey[key]
	if !ok {
		return nil, fmt.Errorf("settings: %s is not a registered setting: %w", key, apperrors.ErrNotFound)
	}
	return def, nil
}

// Get resolves a typed setting. A thin wrapper over Raw: the gate, the
// registry lookup and the default all live there.
func Get[T any](ctx context.Context, s *Store, e *Entry[T]) (T, error) {
	var zero T
	raw, err := s.Raw(ctx, e.Key())
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("settings: decoding %s: %w", e.Key(), err)
	}
	return out, nil
}

// Set writes a typed setting. A thin wrapper over SetRaw, which owns the
// gate, the validation and the write shape.
func Set[T any](ctx context.Context, s *Store, e *Entry[T], v T) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("settings: encoding %s: %w", e.Key(), err)
	}
	return s.SetRaw(ctx, e.Key(), raw)
}

// Seed writes a bootstrap value, consumed exactly once (ADR-0061 §2): it
// inserts only when no row exists, so a restart never overwrites a setting a
// human has since changed. Runs inside the caller's bootstrap transaction and
// takes no RBAC gate — bootstrap runs before any human exists, and the caller
// IS the boot path.
func Seed(ctx context.Context, tx pgx.Tx, def Definition, raw json.RawMessage) error {
	if err := def.ValidateJSON(raw); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO setting (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`,
		def.Key(), raw); err != nil {
		return fmt.Errorf("settings: seeding %s: %w", def.Key(), err)
	}
	return nil
}

// currentJSON reads the value inside an open transaction, falling back to the
// declared default so the audit row's "before" is the value that was actually
// in effect — not an empty stand-in that would misreport the first change.
func currentJSON(ctx context.Context, tx pgx.Tx, def Definition) (json.RawMessage, error) {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT value FROM setting WHERE key = $1`, def.Key()).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return def.DefaultJSON()
	case err != nil:
		return nil, fmt.Errorf("settings: reading %s before write: %w", def.Key(), err)
	}
	return raw, nil
}
