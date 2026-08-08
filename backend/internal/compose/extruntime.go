// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The core's half of the published extension.Runtime contract: one Runtime
// per invocation, built over the process's pool and custodian, scoped to the
// unit being invoked, and released the moment the handler returns.
//
// Everything here is the CORE's obligation rather than the interface's. An
// interface cannot enforce its own lifetime and a published stdlib-only type
// cannot hold a pgx transaction, so runtime.go states the promises and this
// file is the only thing that keeps them.

import (
	"context"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/extsecrets"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// errExtensionRuntimeUnwired refuses a capability call on a role that never
// bound the runtime dependencies. It is deliberately NOT ErrRuntimeExpired:
// that error tells a unit author their handler retained its Runtime, and
// pointing them at their own lifetime for a deployment's wiring fault would
// cost them the afternoon.
var errExtensionRuntimeUnwired = errors.New("compose: this role bound no pool for the extension runtime, so no extension capability can be served")

// extensionRuntimeDeps is the boot's binding of what every per-call Runtime
// is built over. It is process-wide for the same reason composedTools is: a
// role has one pool and one custodian, both settled at boot, and the tool
// adapter that needs them is reached through a registry that cannot carry
// them (mcp.Tool.Handle takes a context and raw JSON, nothing else).
//
// The mutex guards the write-then-read ordering across the boot/serve
// boundary, not concurrent bindings — a role binds once.
var extensionRuntimeDeps struct {
	mu    sync.RWMutex
	pool  *pgxpool.Pool
	vault keyvault.Vault
}

// BindExtensionRuntime records what a governed extension tool's per-call
// Runtime reaches the installation through. Every role that serves or runs
// agent tools calls it once at boot, after the pool and the custodian exist
// — which is later than RegisterExtensions, because a declaration is inert
// and needs neither.
//
// vault may be nil on a deployment that configured no keyvault: the secret
// namespace then refuses by name (extsecrets.ErrNoCustodian) rather than
// writing a mapping row naming material nothing could unseal. A nil pool
// leaves the whole capability surface refusing with
// errExtensionRuntimeUnwired.
func BindExtensionRuntime(pool *pgxpool.Pool, vault keyvault.Vault) {
	extensionRuntimeDeps.mu.Lock()
	defer extensionRuntimeDeps.mu.Unlock()
	extensionRuntimeDeps.pool = pool
	extensionRuntimeDeps.vault = vault
}

// boundExtensionRuntime reads the binding. Read per CALL rather than
// captured at registry construction, so the ordering between binding and
// building a registry cannot matter — only the ordering against the first
// tool call, which is after the boot either way.
//
//nolint:ireturn // keyvault.Vault IS the custodian seam; this hands back what was bound, unchanged.
func boundExtensionRuntime() (*pgxpool.Pool, keyvault.Vault) {
	extensionRuntimeDeps.mu.RLock()
	defer extensionRuntimeDeps.mu.RUnlock()
	return extensionRuntimeDeps.pool, extensionRuntimeDeps.vault
}

// callRuntime is ONE invocation's extension.Runtime.
//
// unit is closed over here, at the one place that knows which unit is being
// invoked, and is never a parameter of anything the handler can call. That
// is the whole namespace wall: not a check the store performs, but a name
// the surface gives a unit no way to say.
type callRuntime struct {
	unit  string
	pool  *pgxpool.Pool
	vault keyvault.Vault

	// mu guards live. A handler may hand its Runtime to a goroutine it
	// spawns, so the release that races that goroutine has to be ordered.
	mu   sync.RWMutex
	live bool
}

var _ extension.Runtime = (*callRuntime)(nil)

// runtimeFor mints the Runtime for one invocation of one unit's tool. It
// returns the concrete type rather than the published interface because the
// caller needs release, which is the core's side of the lifetime contract and
// deliberately not on the surface a handler holds.
func runtimeFor(unit string, pool *pgxpool.Pool, vault keyvault.Vault) *callRuntime {
	return &callRuntime{unit: unit, pool: pool, vault: vault, live: true}
}

// release ends the Runtime's lifetime. Called when the handler returns, so a
// retained Runtime answers ErrRuntimeExpired rather than working against
// resources the call has finished with.
func (r *callRuntime) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live = false
}

// usable is the one gate every capability passes through, in the order the
// two failures matter: a released Runtime is the unit's own mistake, an
// unwired role is the deployment's.
func (r *callRuntime) usable() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.live {
		return extension.ErrRuntimeExpired
	}
	if r.pool == nil {
		return errExtensionRuntimeUnwired
	}
	return nil
}

// Secrets hands out the unit's own namespace, guarded by this Runtime's
// lifetime. The guard is on the returned VALUE and not merely on this method,
// because a handler that stashes the Secrets rather than the Runtime has
// retained exactly the same capability.
//
//nolint:ireturn // returning the published port IS the seam: a unit holds extension.Secrets, never a core type.
func (r *callRuntime) Secrets() extension.Secrets {
	return callSecrets{rt: r, inner: extsecrets.For(r.unit, r.pool, r.vault)}
}

// Tx opens ONE transaction, already pinned to the workspace the invocation
// belongs to, and hands the callback the published seam over it.
//
// The pinning is database.WithWorkspaceTx — the same transaction-local
// app.workspace_id GUC every core store binds, read by the same tenant
// policies — rather than a second mechanism this surface invents. It reads
// the workspace off the call's context and takes no workspace parameter, so
// there is nothing for a handler to widen: the pin is bound before fn runs
// and the tenant policies hold whatever SQL fn then issues.
func (r *callRuntime) Tx(ctx context.Context, fn func(ctx context.Context, tx extension.Tx) error) error {
	if err := r.usable(); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		// Re-checked inside: opening a transaction is a round trip, and a
		// Runtime released during it must not reach the callback with a live
		// handle. Refusing here rolls the (empty) transaction back.
		if err := r.usable(); err != nil {
			return err
		}
		return fn(ctx, extensionTx{tx: tx})
	})
}

// callSecrets is the unit's secret namespace with this call's lifetime
// wrapped around it. Every method is the same two lines because the guard is
// the same fact: the port has six methods and no place to hang a shared
// pre-check that a handler could not step around by holding the value.
type callSecrets struct {
	rt    *callRuntime
	inner extension.Secrets
}

func (s callSecrets) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.rt.usable(); err != nil {
		return nil, err
	}
	return s.inner.Get(ctx, key)
}

func (s callSecrets) Put(ctx context.Context, key string, secret []byte) error {
	if err := s.rt.usable(); err != nil {
		return err
	}
	return s.inner.Put(ctx, key, secret)
}

func (s callSecrets) Delete(ctx context.Context, key string) error {
	if err := s.rt.usable(); err != nil {
		return err
	}
	return s.inner.Delete(ctx, key)
}

func (s callSecrets) GetUser(ctx context.Context, userID extension.UserID, key string) ([]byte, error) {
	if err := s.rt.usable(); err != nil {
		return nil, err
	}
	return s.inner.GetUser(ctx, userID, key)
}

func (s callSecrets) PutUser(ctx context.Context, userID extension.UserID, key string, secret []byte) error {
	if err := s.rt.usable(); err != nil {
		return err
	}
	return s.inner.PutUser(ctx, userID, key, secret)
}

func (s callSecrets) DeleteUser(ctx context.Context, userID extension.UserID, key string) error {
	if err := s.rt.usable(); err != nil {
		return err
	}
	return s.inner.DeleteUser(ctx, userID, key)
}

// extensionTx bridges the published three-verb seam to pgx. It lives in
// internal/ and not in pkg/ because pkg/ is stdlib-only (depguard
// pkg-purity, and TestPublishedSurfaceIsPure): a published interface can
// describe a transaction, but only the core can hold one.
//
// There is no lifetime guard on this type. A Tx used after its transaction
// ended answers pgx's own ErrTxClosed, which is already an honest refusal —
// and it is the accurate one, because the transaction can end while the
// Runtime is still perfectly live (the callback returned, the call did not).
// ErrRuntimeExpired there would name the wrong fault.
type extensionTx struct{ tx pgx.Tx }

func (t extensionTx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

//nolint:ireturn // Rows is the published cursor; a unit must never hold pgx.Rows.
func (t extensionTx) Query(ctx context.Context, sql string, args ...any) (extension.Rows, error) {
	rows, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return extensionRows{rows: rows}, nil
}

//nolint:ireturn // Row is the published deferred read; the error is deferred to Scan by design.
func (t extensionTx) QueryRow(ctx context.Context, sql string, args ...any) extension.Row {
	return extensionRow{row: t.tx.QueryRow(ctx, sql, args...)}
}

// extensionRows is pgx's cursor behind the published one. The four methods
// line up exactly, which is why the published seam was spelled this way.
type extensionRows struct{ rows pgx.Rows }

func (r extensionRows) Next() bool             { return r.rows.Next() }
func (r extensionRows) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r extensionRows) Err() error             { return r.rows.Err() }
func (r extensionRows) Close()                 { r.rows.Close() }

// extensionRow defers one read's error to Scan, translating the empty match
// into the published sentinel — a unit matching on pgx.ErrNoRows would be
// binding a driver this surface never published.
type extensionRow struct{ row pgx.Row }

func (r extensionRow) Scan(dest ...any) error {
	if err := r.row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return extension.ErrNoRows
		}
		return err
	}
	return nil
}
