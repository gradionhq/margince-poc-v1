// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package keyvault

import (
	"context"
	"log/slog"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// CleanupTimeout bounds every credential delete that runs outside a request's
// own success path. Each such delete is detached from the caller's
// cancellation, so without a deadline of its own a stalled vault would hang
// the worker that started it indefinitely.
const CleanupTimeout = 5 * time.Second

// DeleteDetached removes a credential that a COMMITTED lifecycle change left
// unreferenced: the superseded blob after a reconnect re-pointed a row at a
// fresh one, the revoked blob after a disconnect.
//
// It never fails its caller. The lifecycle change is already authoritative and
// the caller has nothing to undo; worse, a retry could never re-attempt this
// delete (a second disconnect finds no live row, a second reconnect finds no
// superseded ref), so failing here would misreport a change that DID happen AND
// strand the blob anyway. On failure it logs at ERROR for operational cleanup
// instead: the blob is inert — unreferenced, encrypted at rest — and ref is a
// vault key, never the secret, so it is safe to log.
//
// The delete runs AFTER its transaction commits, which means it must OUTLIVE
// the request. ctx is the caller's cancellable request context; a client that
// hangs up the moment it has its response would otherwise cancel this cleanup
// before it starts, stranding the blob on every such call. So the vault sees a
// context detached from that cancellation (context.WithoutCancel) under its own
// CleanupTimeout deadline.
//
// A nil vault is a wiring fault, not a reason to stay quiet: something holds a
// ref that no configured custodian can destroy, and only a log line will ever
// surface it.
func DeleteDetached(ctx context.Context, v Vault, log *slog.Logger, ws ids.UUID, ref Ref, lifecycle string) {
	if ref == "" {
		return
	}
	if v == nil {
		log.ErrorContext(ctx, "keyvault: a lifecycle change left a credential unreferenced but no vault is configured to destroy it — the secret survives its connection",
			"lifecycle", lifecycle, "workspace", ws.String(), "credential_ref", string(ref))
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
	defer cancel()
	if err := v.Delete(cleanupCtx, ids.From[ids.WorkspaceKind](ws), ref); err != nil {
		log.ErrorContext(ctx, "keyvault: the lifecycle change committed, but deleting the now-unreferenced credential failed — the orphaned (inert) blob needs cleanup",
			"lifecycle", lifecycle, "workspace", ws.String(), "credential_ref", string(ref), "err", err)
	}
}
