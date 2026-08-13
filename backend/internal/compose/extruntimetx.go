// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The unit's own transaction: opening one, and the one thing holding one
// forbids.
//
// It sits apart from the Runtime's other capabilities because the two halves
// here are one fact read from opposite ends. Tx hands a unit a transaction on
// the invocation's tenant; the counter beside it is how the INGRESS port knows
// one is open, since capture opens a transaction of its own and a second
// acquire inside the first does not fail on a small pool — it waits for a
// connection this Runtime is holding.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// enterTx and leaveTx bracket one open transaction. They take the write lock
// the flag already uses, so the count and the liveness answer cannot disagree.
func (r *callRuntime) enterTx() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.txDepth++
}

func (r *callRuntime) leaveTx() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.txDepth--
}

// insideTx reports whether this Runtime is holding a transaction open.
//
// A handler goroutine ingesting while an UNRELATED transaction of the same
// runtime is open is refused too. That false positive is accepted rather than
// engineered away: the alternative — marking the context Tx hands its callback
// — is evaded by a handler that ingests with the outer context from inside the
// callback, which still hangs. Refusing a call that would have worked is a
// better failure than hanging a worker.
func (r *callRuntime) insideTx() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.txDepth > 0
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
	// Derived BEFORE the transaction opens. The unit name was validated at
	// registration, so an invalid one here is a composition that should never
	// have booted — and learning that inside a transaction would only make the
	// report harder to read than it needs to be.
	namespace, err := extension.Name(r.unit).Namespace()
	if err != nil {
		return fmt.Errorf("compose: the invoking unit's name has no SQL namespace: %w", err)
	}
	r.enterTx()
	defer r.leaveTx()
	return database.WithWorkspaceTx(ctx, r.deps.pool, func(tx pgx.Tx) error {
		// Re-checked inside: opening a transaction is a round trip, and a
		// Runtime released during it must not reach the callback with a live
		// handle. Refusing here rolls the (empty) transaction back.
		if err := r.usable(); err != nil {
			return err
		}
		return fn(ctx, extensionTx{
			tx: tx,
			core: extensionCore{
				tx: tx, unattended: r.unattended, deps: r.deps, authority: r.scoped,
			},
			ledger: extensionLedger{tx: tx, namespace: namespace, authority: r.scoped},
		})
	})
}
