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
	portsettings "github.com/gradionhq/margince/backend/internal/shared/ports/settings"
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

// Lookup resolves a key to its declaration.
//
//nolint:ireturn // returns the type-erased Definition by design — the registry holds entries of many value types, and the concrete Entry[T] cannot be named without the type parameter the caller is looking up
func (r *Registry) Lookup(key string) (Definition, bool) {
	d, ok := r.byKey[key]
	return d, ok
}

// Store reads and writes settings. It implements ports/settings.Reader.
type Store struct {
	pool *pgxpool.Pool
	reg  *Registry
}

// New builds the store over the pool and the assembled registry.
func New(pool *pgxpool.Pool, reg *Registry) *Store { return &Store{pool: pool, reg: reg} }

var _ portsettings.Reader = (*Store)(nil)

// Raw implements ports/settings.Reader: the stored value, or the registered
// default when no row exists. An unregistered key is an error — a typo must
// not read as "unset and therefore default".
func (s *Store) Raw(ctx context.Context, key string) (json.RawMessage, error) {
	def, ok := s.reg.Lookup(key)
	if !ok {
		return nil, fmt.Errorf("settings: %s is not a registered setting: %w", key, apperrors.ErrNotFound)
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
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
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

// Get resolves a typed setting the caller owns. Cross-module reads go through
// ports/settings.Read against the same store.
func Get[T any](ctx context.Context, s *Store, e *Entry[T]) (T, error) {
	return portsettings.Read(ctx, s, e.TypedKey())
}

// Set writes a setting, committing the row + audit in ONE transaction like
// every other mutation. No event: the closed event catalog defines no
// settings verb (EVT-NOEVT-3), the same ruling the capture-settings and
// fx-rate config writes already carry.
//
// An unchanged value is a no-op — no write, no audit row — because an
// idempotent PATCH should not litter the ledger.
func Set[T any](ctx context.Context, s *Store, e *Entry[T], v T) error {
	if err := auth.Require(ctx, e.Object(), principal.ActionUpdate); err != nil {
		return err
	}
	next, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("settings: encoding %s: %w", e.Key(), err)
	}
	if err := e.ValidateJSON(next); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := currentJSON(ctx, tx, e)
		if err != nil {
			return err
		}
		same, err := sameValue(before, next, e)
		if err != nil {
			return err
		}
		if same {
			return nil
		}
		frozen, why, err := e.Frozen(ctx, tx)
		if err != nil {
			return fmt.Errorf("settings: probing %s: %w", e.Key(), err)
		}
		if frozen {
			// The owning module's own sentence, so the caller learns what to
			// do rather than only that they may not.
			return fmt.Errorf("%w: %s is no longer changeable: %s", apperrors.ErrConflict, e.Key(), why)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO setting (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
			e.Key(), next); err != nil {
			return fmt.Errorf("settings: writing %s: %w", e.Key(), err)
		}
		if _, err := storekit.Audit(ctx, tx, e.AuditVerb(), e.Object(), storekit.MustWorkspace(ctx),
			map[string]any{e.Key(): json.RawMessage(before)},
			map[string]any{e.Key(): json.RawMessage(next)}); err != nil {
			return fmt.Errorf("settings: auditing %s: %w", e.Key(), err)
		}
		return nil
	})
}

// sameValue reports whether two encodings mean the same value, by decoding
// both to the entry's own type and re-encoding canonically.
//
// A byte comparison would be wrong, and wrong in a way that only shows up
// later: `next` comes from Go's json.Marshal, while `before` comes back from
// Postgres, which normalizes jsonb — key order and whitespace are its choice,
// not ours. For a scalar the two happen to agree; for a composite value (the
// settings whose fields must change together) they need not, and every write
// would look like a change, writing a row and an audit entry saying nothing
// happened.
func sameValue[T any](before, next json.RawMessage, e *Entry[T]) (bool, error) {
	var prev T
	if err := json.Unmarshal(before, &prev); err != nil {
		// A stored value that no longer decodes is not "different" — it is a
		// value this build cannot reason about, and overwriting it silently
		// would destroy the evidence of whatever wrote it.
		return false, fmt.Errorf("settings: %s holds a value this build cannot decode: %w", e.Key(), err)
	}
	canonical, err := json.Marshal(prev)
	if err != nil {
		return false, fmt.Errorf("settings: re-encoding %s for comparison: %w", e.Key(), err)
	}
	return string(canonical) == string(next), nil
}

// currentJSON reads the value inside an open transaction, falling back to the
// declared default so the audit row's "before" is the value that was actually
// in effect — not an empty stand-in that would misreport the first change.
func currentJSON[T any](ctx context.Context, tx pgx.Tx, e *Entry[T]) (json.RawMessage, error) {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT value FROM setting WHERE key = $1`, e.Key()).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return e.DefaultJSON()
	case err != nil:
		return nil, fmt.Errorf("settings: reading %s before write: %w", e.Key(), err)
	}
	return raw, nil
}
