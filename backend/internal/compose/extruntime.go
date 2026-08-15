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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/extsecrets"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
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
	mu sync.RWMutex
	extensionRuntimeBinding
}

// extensionRuntimeBinding is what one role bound for every per-call Runtime:
// the pool a unit's own SQL and the governed core port both run on, and the
// custodian its secrets need.
//
// The core port needs no binding of its own, and that is worth stating because
// the design expected one. Its two dependencies — the store that owns the write
// and the fresh read of the workspace's record mode — are each derived from the
// pool at the call (activities.NewStore is the tree's own idiom for exactly
// that), so no role can wire the port half-way and no role serves a different
// set of capabilities than another.
type extensionRuntimeBinding struct {
	pool  *pgxpool.Pool
	vault keyvault.Vault
	// captureSink is the ONE fully-guarded capture pipeline a unit's ingress
	// lands through, bound separately by BindExtensionCapture.
	//
	// It IS a binding, unlike the core port above, and the difference is that
	// this dependency is not derivable from the pool: newCaptureSink attaches
	// the file keeper, the merge stager and the counterparty ensurer — three
	// cross-module adapters — plus the deployment's suppression config. A sink
	// built here from the pool alone would compile, run, land activities and
	// silently create no people, which is the failure this field exists to make
	// impossible. Nil on a role that composed no capture, and the port then
	// refuses by name rather than half-working.
	captureSink *capture.Sink
}

// BindExtensionCapture records the capture pipeline a unit's ingress lands
// through. A role that runs unattended extension work — a job tick, a
// subscription delivery — calls it once at boot with the same deployment
// capture config its own connectors run on.
//
// It takes the CONFIG and assembles the sink here rather than accepting one,
// which is the whole point of the signature: newCaptureSink is the one spelling
// that attaches the file keeper, the merge stager and the counterparty ensurer,
// and a parameter of type *capture.Sink would let a caller hand over a
// hand-assembled pipeline that lands activities and silently creates no people.
//
// Separate from BindExtensionRuntime rather than a third parameter on it,
// because the two answer different questions: every role has a pool and a
// custodian, and only some run work that can ingest. A role that never calls
// this leaves ingress refusing with errIngressUnwired, which is the honest
// answer for a process that has nowhere to put a captured record.
func BindExtensionCapture(pool *pgxpool.Pool, cfg CaptureConfig) {
	extensionRuntimeDeps.mu.Lock()
	defer extensionRuntimeDeps.mu.Unlock()
	extensionRuntimeDeps.captureSink = newCaptureSink(pool, cfg)
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
	extensionRuntimeDeps.extensionRuntimeBinding = extensionRuntimeBinding{pool: pool, vault: vault}
}

// boundExtensionRuntime reads the binding. Read per CALL rather than
// captured at registry construction, so the ordering between binding and
// building a registry cannot matter — only the ordering against the first
// tool call, which is after the boot either way.
func boundExtensionRuntime() extensionRuntimeBinding {
	extensionRuntimeDeps.mu.RLock()
	defer extensionRuntimeDeps.mu.RUnlock()
	return extensionRuntimeDeps.extensionRuntimeBinding
}

// callRuntime is ONE invocation's extension.Runtime.
//
// unit is closed over here, at the one place that knows which unit is being
// invoked, and is never a parameter of anything the handler can call. That
// is the whole namespace wall: not a check the store performs, but a name
// the surface gives a unit no way to say.
type callRuntime struct {
	unit string
	// version and via are what a core write made through this Runtime is
	// ATTRIBUTED to. Both are the core's own knowledge — the composed
	// declaration's version and the surface the call arrived on — and neither
	// is anything the handler can reach or influence.
	version string
	via     string
	deps    extensionRuntimeBinding

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

	// unattended forces Caller to answer the zero value whatever principal
	// callCtx carries, and refuses the core port. Two paths set it — a job tick
	// (jobRuntimeFor) and a bus delivery (deliveryRuntimeFor) — and they share
	// the one fact that decides both: nobody is there, so there is no authority
	// a core write could be checked against.
	unattended bool

	// mu orders live: it is read and written under the same lock, so release
	// and a handler-spawned goroutine cannot race the FLAG. It does not order
	// the WORK — see usable.
	mu   sync.RWMutex
	live bool

	// txDepth counts the transactions this Runtime currently holds open, and it
	// is a COUNTER rather than a flag on purpose. A handler may call Tx twice
	// concurrently; with a boolean the first to return would clear it while the
	// second still held a connection, and an ingest nested inside that second
	// one would pass the check and then wait for a connection the same runtime
	// is holding — which on a small pool does not fail, it hangs.
	//
	// It is deliberately per-RUNTIME, and so is its guarantee. Two DIFFERENT
	// runtimes — one holding a transaction while the other ingests — are not
	// visible to each other and can still deadlock a pool of one. No
	// per-runtime mechanism sees that, and this is the ordinary distance
	// between a shape check and a proof rather than a claim of safety.
	txDepth int

	// ingesting counts the ingests in flight, and it is what makes the
	// transaction refusal a guarantee rather than a check-then-use race: an
	// ingest claims this slot under the same lock that admits a transaction,
	// so a sibling goroutine cannot open one in the window between the check
	// and capture's own acquire.
	ingesting int
}

var _ extension.Runtime = (*callRuntime)(nil)

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
	if r.deps.pool == nil {
		return errExtensionRuntimeUnwired
	}
	return nil
}

// scoped is the gate plus the pin: it checks the lifetime and returns ctx
// re-bound to what THE INVOCATION arrived under — its workspace, its actor and
// its correlation id.
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
	ctx = principal.WithWorkspaceID(ctx, ws)

	// The tenant is not the only thing the invocation knows and the handler's
	// context may not. A core write resolves its actor and its correlation id
	// from the context as well — Audit refuses without an actor, Emit without a
	// correlation — so a handler that opened its transaction on
	// context.Background() would reach the write path with neither, and the
	// write would fail on plumbing rather than on anything the unit did. They
	// travel the same way as the workspace and for the same reason: taken from
	// the INVOCATION, so what a handler passes can shorten a deadline but
	// cannot change who is acting.
	//
	// A value the invocation does not carry is left alone rather than
	// defaulted. There is nothing to forge past — a unit cannot reach
	// principal.With… at all, the package being internal to the backend module
	// — so an unbound actor here means the invocation arrived without one, and
	// inventing a stand-in would put a fictional identity on an audit row.
	if actor, bound := principal.Actor(r.callCtx); bound {
		ctx = principal.WithActor(ctx, actor)
	}
	if correlation, bound := principal.CorrelationID(r.callCtx); bound {
		ctx = principal.WithCorrelationID(ctx, correlation)
	}
	// The CAUSATION travels the same way, and it matters most where the
	// invocation is a reaction: a bus delivery binds the event that triggered
	// it, so what the unit publishes chains back to what caused it. Left to the
	// handler's context it would be right only while the handler passed the
	// context it was given — and wrong in the two ways a handler gets it wrong,
	// each silently: a context built fresh publishes a reaction with no cause,
	// and one retained from an earlier delivery publishes this reaction as
	// caused by that earlier event.
	if causation, bound := principal.CausationEvent(r.callCtx); bound {
		ctx = principal.WithCausationEvent(ctx, causation)
	}
	// And WHAT carried the action, which is a second dimension beside who took
	// it: every core write made under this context records the unit, its
	// declared version and the surface the call arrived on. Bound here rather
	// than at the write, for the same reason as the three above — this is the
	// one place that knows the invocation, and a value a handler could supply
	// would be a unit able to sign another unit's name.
	return provenance.WithExtension(ctx, provenance.Extension{
		Unit: r.unit, Version: r.version, Via: r.via,
	}), nil
}

// Secrets hands out the unit's own namespace, guarded by this Runtime's
// lifetime. The guard is on the returned VALUE and not merely on this method,
// because a handler that stashes the Secrets rather than the Runtime has
// retained exactly the same capability.
//
//nolint:ireturn // returning the published port IS the seam: a unit holds extension.Secrets, never a core type.
func (r *callRuntime) Secrets() extension.Secrets {
	return callSecrets{rt: r, inner: extsecrets.For(r.unit, r.deps.pool, r.deps.vault)}
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
	if !ok || r.unattended {
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
