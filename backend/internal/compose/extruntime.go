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
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/extsecrets"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
// A second bind to a DIFFERENT non-nil pool is a wiring fault: two pools in
// one process means half the extension calls run on the wrong one, silently.
// It is logged rather than refused because this is not the layer that gets to
// end a boot, and because a test restoring a previous binding legitimately
// rebinds — but it must never happen unremarked.
func BindExtensionRuntime(pool *pgxpool.Pool, vault keyvault.Vault) {
	extensionRuntimeDeps.mu.Lock()
	defer extensionRuntimeDeps.mu.Unlock()
	if prev := extensionRuntimeDeps.pool; prev != nil && pool != nil && prev != pool {
		slog.Default().Warn("compose: the extension runtime was rebound to a different pool; " +
			"every extension capability from now on runs against the new one")
	}
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

	// callCtx is the context the INVOCATION arrived on, held for exactly one
	// value: the workspace. Every capability re-derives the tenant from here
	// rather than from the context the handler passes in, which is what makes
	// "a handler cannot widen its own scope" a property of construction
	// instead of a property of principal.WithWorkspaceID happening to be
	// unreachable from an extension module. See scoped.
	//
	// Held in a field rather than threaded, because the capability methods
	// are the published Runtime's and cannot grow a parameter for it.
	callCtx context.Context //nolint:containedctx // the invocation's tenant scope IS this value's lifetime; see above.

	// systemCaller forces Caller to answer the zero value whatever principal
	// callCtx carries. Exactly one path sets it — a job tick — and the reason
	// is in jobRuntimeFor.
	systemCaller bool

	// mu orders live: it is read and written under the same lock, so release
	// and a handler-spawned goroutine cannot race the FLAG. It does not order
	// the WORK — see usable.
	mu   sync.RWMutex
	live bool
}

var _ extension.Runtime = (*callRuntime)(nil)

// runtimeFor mints the Runtime for one invocation of one unit's tool. ctx is
// the invocation's, not a handler's.
//
// It returns the concrete type rather than the published interface because
// the caller needs release, which is the core's side of the lifetime contract
// and deliberately not on the surface a handler holds.
func runtimeFor(ctx context.Context, unit string, pool *pgxpool.Pool, vault keyvault.Vault) *callRuntime {
	return &callRuntime{unit: unit, pool: pool, vault: vault, callCtx: ctx, live: true}
}

// jobRuntimeFor mints the Runtime for one JOB tick, which differs from an
// invocation in exactly one way: who it answers as.
//
// A tick's context carries a principal — deriveAuthority re-reads the
// dispatcher's seat at execution and binds it, because the tenant policies and
// the audit rows need an actor. That actor is an AGENT seat with no human
// behind it: its OnBehalfOf is zero and its UserID is the synthetic is_agent
// app_user the dispatcher minted. Mapping it through Caller's ordinary rules
// would hand a unit precisely the thing Caller.UserID promises never to be —
// "a synthetic id for the agent" rather than the person accountable for the
// row — and would contradict Runtime.Caller's promise that a tick answers the
// zero Caller. So the tick says so at construction rather than leaving Caller
// to guess it from a principal that looks, field by field, like a real agent
// call. Nothing else about the tick changes: the actor on callCtx is still the
// one every capability and every policy sees.
func jobRuntimeFor(ctx context.Context, unit string, pool *pgxpool.Pool, vault keyvault.Vault) *callRuntime {
	rt := runtimeFor(ctx, unit, pool, vault)
	rt.systemCaller = true
	return rt
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
//
// It is a CHECK, not a hold. A handler-spawned goroutine that passes it
// microseconds before release proceeds anyway, and Tx's re-check inside the
// transaction narrows that window without closing it. Closing it would mean
// holding the read lock for the whole of a capability call, which makes
// release — and therefore the request that is trying to return — block until
// a goroutine the handler leaked finishes its transaction. A hung request is
// a worse failure than a last-microsecond call that completes, so the window
// is documented rather than closed. What the lock DOES buy is that the flag
// itself is race-free and that a retained Runtime used on any later call
// (the actual failure mode this guards) is refused every time.
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

// scoped is the gate plus the pin: it checks the lifetime and returns ctx
// re-bound to the workspace THE INVOCATION arrived under.
//
// Rebinding rather than trusting the incoming ctx is the point. Everything a
// handler passes down — cancellation, deadline, request values — is kept,
// because a handler shortening its own deadline is legitimate; the one thing
// it cannot carry is a different tenant. Without this the workspace would be
// whatever the handler supplied, and the design's claim that a unit cannot
// widen its own scope would rest on principal.WithWorkspaceID being
// unreachable from an extension module rather than on anything structural.
func (r *callRuntime) scoped(ctx context.Context) (context.Context, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	ws, ok := principal.WorkspaceID(r.callCtx)
	if !ok {
		// The same refusal WithWorkspaceTx would give, raised against the
		// invocation rather than the handler's context — an unpinned
		// invocation is a core wiring fault, and no ctx a handler builds
		// should be able to supply the missing tenant.
		return nil, database.ErrNoWorkspace
	}
	return principal.WithWorkspaceID(ctx, ws), nil
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
// policies — rather than a second mechanism this surface invents. The
// workspace comes from scoped, so it is the INVOCATION's and not whatever
// tenant the handler's own ctx happens to carry: the pin is bound before fn
// runs, and the tenant policies then hold whatever SQL fn issues.
func (r *callRuntime) Tx(ctx context.Context, fn func(ctx context.Context, tx extension.Tx) error) error {
	ctx, err := r.scoped(ctx)
	if err != nil {
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

// Caller answers who the invocation runs as, copied out of the principal the
// call arrived under. It reads r.callCtx and nothing the handler supplies, for
// the same reason Tx re-derives the tenant there: an identity a handler can
// pass in is an identity a handler can choose.
//
// It does NOT pass through usable, and that is deliberate rather than an
// omission. Every other capability gates on the lifetime because it REACHES
// something — a pool, a custodian — that the call has finished with; this one
// reads a value already in hand and grants nothing, which is why the published
// type is a copied struct that runtime.go says is harmless to retain. Refusing
// here would also need an error return the surface does not have, so the choice
// is between answering after release and lying with a zero Caller — and a unit
// that logs its caller from a deferred line deserves the true answer.
//
// It cannot fail and it issues no query: a display name or a team list would
// each be an app_user read, so this carries only what the principal already
// holds.
func (r *callRuntime) Caller() extension.Caller {
	actor, ok := principal.Actor(r.callCtx)
	if !ok || r.systemCaller {
		// No principal is the unauthenticated or unbound path, and the zero
		// Caller is CallerSystem — the least authority, so a wiring gap reads
		// as "nobody" rather than as a human whose id happens to be empty.
		return extension.Caller{}
	}
	switch actor.Type {
	case principal.PrincipalHuman:
		return extension.Caller{Type: extension.CallerHuman, UserID: callerUserID(actor.UserID)}
	case principal.PrincipalAgent:
		return extension.Caller{
			Type: extension.CallerAgent, UserID: callerUserID(humanBehind(actor)), IsAgent: true,
		}
	case principal.PrincipalConnector:
		return extension.Caller{
			Type: extension.CallerConnector, UserID: callerUserID(humanBehind(actor)), IsAgent: true,
		}
	case principal.PrincipalSystem:
		return extension.Caller{}
	default:
		// An unmapped principal type is a kernel vocabulary this file has not
		// been taught. Fail towards the least authority rather than towards a
		// human: a unit that gates on Type must not be opened by a type it
		// cannot have heard of either.
		return extension.Caller{}
	}
}

// humanBehind is the app_user whose authority a non-human call carries: the
// granting human when the loader recorded one, and otherwise the principal's
// own user — a connector configured against a seat directly names it in UserID
// with OnBehalfOf left zero, and the fallback keeps that call attributable
// instead of anonymous.
func humanBehind(actor principal.Principal) ids.UUID {
	if !actor.OnBehalfOf.IsZero() {
		return actor.OnBehalfOf
	}
	return actor.UserID
}

// callerUserID renders an id for the published surface, where the ABSENCE of a
// user must read as "". ids.UUID.String() spells the zero value as the all-zero
// uuid, which is a perfectly valid-looking id a unit would happily stamp on a
// row, so the emptiness has to be restored here.
func callerUserID(id ids.UUID) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
}

// callSecrets is the unit's secret namespace with this call's lifetime and
// this call's tenant wrapped around it. Every method is the same three lines
// because the guard is the same fact: the port has six methods and no place
// to hang a shared pre-check that a handler could not step around by holding
// the value. The scoped call is the same one Tx makes — a secret read must
// not be reachable in a workspace the invocation did not arrive under, and
// the store resolves the tenant from the ctx it is handed.
type callSecrets struct {
	rt    *callRuntime
	inner extension.Secrets
}

func (s callSecrets) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, err := s.rt.scoped(ctx)
	if err != nil {
		return nil, err
	}
	return s.inner.Get(ctx, key)
}

func (s callSecrets) Put(ctx context.Context, key string, secret []byte) error {
	ctx, err := s.rt.scoped(ctx)
	if err != nil {
		return err
	}
	return s.inner.Put(ctx, key, secret)
}

func (s callSecrets) Delete(ctx context.Context, key string) error {
	ctx, err := s.rt.scoped(ctx)
	if err != nil {
		return err
	}
	return s.inner.Delete(ctx, key)
}

func (s callSecrets) GetUser(ctx context.Context, userID extension.UserID, key string) ([]byte, error) {
	ctx, err := s.rt.scoped(ctx)
	if err != nil {
		return nil, err
	}
	return s.inner.GetUser(ctx, userID, key)
}

func (s callSecrets) PutUser(ctx context.Context, userID extension.UserID, key string, secret []byte) error {
	ctx, err := s.rt.scoped(ctx)
	if err != nil {
		return err
	}
	return s.inner.PutUser(ctx, userID, key, secret)
}

func (s callSecrets) DeleteUser(ctx context.Context, userID extension.UserID, key string) error {
	ctx, err := s.rt.scoped(ctx)
	if err != nil {
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
