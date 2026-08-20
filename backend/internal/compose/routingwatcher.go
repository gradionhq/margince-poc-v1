// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Keeping every role on the binding the installation currently holds.
//
// An admin changes the binding through the api. The WORKER is a separate
// process and never sees that write — so without this, re-pointing a lane would
// take effect on one role and not the other, and the two would serve different
// models with nothing saying so. That is the failure moving routing into the
// database was meant to end, and a write surface alone would have reintroduced
// it in a worse form: visible in the UI, and wrong.
//
// So each role re-reads on an interval, the same shape licensecheck.Watcher
// uses for a license renewed in place. Convergence is bounded by the interval
// rather than instant, and that is the right trade here: a routing change is
// rare and deliberate, while a push would need every role to hold a live
// subscription whose failure mode is silence — a role that missed the message
// keeps serving the old binding forever, which is exactly what this prevents.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// routingRecheckInterval bounds how long a role may serve a superseded binding.
// Short enough that an operator who changed a lane sees it take effect while
// they are still watching; long enough that the read is nothing against a
// database that serves every request.
const routingRecheckInterval = 30 * time.Second

// RoutingWatcher re-reads the stored binding and rebinds what serves it.
type RoutingWatcher struct {
	pool   *pgxpool.Pool
	target *ai.Router
	keys   config.Lookup
	log    *slog.Logger
}

// NewRoutingWatcher binds a watcher to whatever this role is serving with. A
// role that resolved no binding at boot passes nil and gets nil: there is
// nothing to keep current, and a watcher polling to rebind nothing would be
// work with no observable effect.
func NewRoutingWatcher(pool *pgxpool.Pool, target *ai.Router, keys config.Lookup, log *slog.Logger) *RoutingWatcher {
	if pool == nil || target == nil {
		return nil
	}
	return &RoutingWatcher{pool: pool, target: target, keys: keys, log: log}
}

// Run re-reads until ctx is cancelled. Started by each role that resolved a
// binding at boot; nothing else drives it.
func (w *RoutingWatcher) Run(ctx context.Context) {
	if w == nil {
		return
	}
	ticker := time.NewTicker(routingRecheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Recheck(ctx)
		}
	}
}

// Recheck applies the stored binding if it differs from what is being served.
//
// A failed read keeps the current binding rather than unbinding: a database
// blip must not take an installation's AI lanes down, and the next tick tries
// again. The same reasoning the license watcher applies to a failed re-check.
func (w *RoutingWatcher) Recheck(ctx context.Context) {
	if w == nil {
		return
	}
	next, err := ResolveRouting(ctx, w.pool, "", w.keys, w.log)
	if err != nil {
		w.log.WarnContext(ctx, "re-reading the model binding failed; keeping the one this process is serving",
			"error", err, "routing_version", w.target.RoutingVersion())
		return
	}
	w.applyIfChanged(ctx, next)
}

// applyIfChanged is the decision, split from the read so it can be judged
// without a database — the read is what the integration suite covers, and this
// is where every choice worth getting wrong lives.
// Reports whether it rebound, which is what the tick's decision amounts to and
// what a test can judge without reaching inside the Router.
func (w *RoutingWatcher) applyIfChanged(ctx context.Context, next ai.RoutingConfig) bool {
	// Unconfigured means the binding was deleted out from under a running
	// role. Keep serving rather than tearing the lanes down mid-flight: an
	// operator removing a binding is choosing what the NEXT boot does, and an
	// installation whose AI stopped answering with nothing written anywhere is
	// the worst way to learn that.
	if next.Unconfigured() {
		return false
	}
	// The version is the whole comparison. It is a digest of what the binding
	// BINDS, so it changes exactly when the models change and not when the
	// document is merely rewritten — which is what keeps this from rebinding,
	// and dropping every cached completion, on every tick.
	previous := w.target.RoutingVersion()
	if next.RoutingVersion() == previous {
		return false
	}
	if err := w.target.Rebind(next); err != nil {
		// A stored binding this process cannot serve leaves the running one
		// alone. Turning a bad edit into an outage would be worse than serving
		// a superseded binding, and the operator still has a way back.
		w.log.ErrorContext(ctx, "the stored model binding cannot be served; keeping the current one",
			"error", err, "routing_version", previous)
		return false
	}
	w.log.InfoContext(ctx, "adopted a changed model binding without restarting",
		"from", previous, "to", next.RoutingVersion())
	return true
}
