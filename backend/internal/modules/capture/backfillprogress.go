// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The running page's live tally. A run's counters advance once per COMMITTED
// page (backfillpager.go), and a page is a hundred messages of provider I/O
// and capture work — long enough that the activation view sat at zero for the
// whole first page and read as an import that never started.
//
// So the page also reports what it has walked so far into the run's inflight_*
// columns, and the status read adds the two. Those columns are advisory and
// transient by construction: every write that ends a page resets them, which
// is what keeps the committed counters the one authority and a retried page
// counted once.

package capture

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// pageTally is one page's live counts — absolute since the page began, never
// deltas, so a flush that never lands is corrected by the next one instead of
// leaving the row permanently short.
type pageTally struct {
	scanned  int
	captured int
	skipped  int
}

// advance folds a connector's report in and reports whether the tally moved. A
// report at or behind the one already held is dropped: a page walked
// concurrently can deliver two reports out of order, and the later-arriving
// lower number would make the count on screen go backwards.
func (t *pageTally) advance(scanned, captured, skipped int) bool {
	if scanned <= t.scanned {
		return false
	}
	t.scanned, t.captured, t.skipped = scanned, captured, skipped
	return true
}

// defaultProgressPacing paces the live write. A real import is thousands of
// messages, and one row update per message would be tens of thousands of
// writes to a single row so a number can move faster than anyone can read it.
// Half a second still reads as continuous motion; what the pacing drops is
// only ever an intermediate value, because the tally is absolute and the
// page's commit reconciles regardless.
const defaultProgressPacing = 500 * time.Millisecond

// pageProgress accumulates what ONE backfill page walks and creates, and
// persists it as it goes. Its two sources arrive on different seams: the
// connector reports scanned/captured/skipped through connector.BackfillProgress,
// while counterparty creations happen deep inside the Sink, which reads this
// collector straight off the context — widening connector.Sink to carry a
// count would change four connectors and every test fake for a number none of
// them can produce.
type pageProgress struct {
	// A page is a batch of independent messages and nothing promises a
	// connector walks it serially. The lock is held ACROSS the flush so the
	// row cannot take an older write after a newer one; the tally itself only
	// ever moves forward (Observed refuses a report behind the one it holds),
	// so the two together are what keep an on-screen count from going
	// backwards.
	mu        sync.Mutex
	tally     pageTally
	lastFlush time.Time

	backfillID ids.UUID
	// generation fences every write against a connection rebound under the
	// running page — the same fence the page's commit carries.
	generation int
	registry   *Registry
}

var _ connector.BackfillProgress = (*pageProgress)(nil)

// pageProgressKey is the private context key — unexported and typed, so no
// other package can install or read this.
type pageProgressKey struct{}

// withPageProgress installs a fresh collector for one page. Fresh per page,
// because the counters are folded in at page commit: a shared collector would
// double-count every page after the first.
func withPageProgress(ctx context.Context, r *Registry, backfillID ids.UUID, generation int) (context.Context, *pageProgress) {
	p := &pageProgress{backfillID: backfillID, generation: generation, registry: r}
	ctx = context.WithValue(ctx, pageProgressKey{}, p)
	return connector.WithBackfillProgress(ctx, p), p
}

// pageProgressFrom returns the collector this context carries, or nil when no
// backfill page is running — the incremental sync path, where a created
// counterparty belongs to no run. The methods reached this way (counted,
// totals) tolerate a nil receiver, so absence costs a branch and never a
// panic. Observed does not: it is only ever reached through
// connector.BackfillReporter, which holds a non-nil collector or discards the
// report itself.
func pageProgressFrom(ctx context.Context) *pageProgress {
	c, _ := ctx.Value(pageProgressKey{}).(*pageProgress)
	return c
}

// Observed takes the connector's running count for this page.
func (c *pageProgress) Observed(ctx context.Context, scanned, captured, skipped int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tally.advance(scanned, captured, skipped) {
		c.persist(ctx, paced)
	}
}

// counted records ONE counterparty creation on the run, immediately, in the
// committed columns.
//
// Per creation and not per page, because a page's total is a batch and a batch
// is something to lose. Whatever writes it can fail, and nothing can rebuild it
// afterwards: capture is idempotent, so a replayed message never reaches the
// resolver again and no retry re-offers these rows to anybody. Counting each row
// as it appears leaves no batch anywhere to be dropped, double-credited, or
// fenced off.
//
// What this guarantees, exactly, and what it does not:
//
//   - It never DOUBLE-counts. A row is created once, and this runs on that
//     single outcome; nothing retries the write, so nothing can apply it twice.
//   - It is not exactly-once. The row is created in the resolver's transaction
//     and counted in this one, so a failed write loses the count of the creation
//     it was about, permanently — capture's idempotency means no replay re-offers
//     that row to anyone.
//   - The loss is per failure, and NOTHING caps the total. A database fault
//     spanning a page loses one count for every creation inside it. Calling it
//     "one row" would be true only of a single blip.
//
// So the committed columns are a floor on what the run created, never an
// overcount. Closing the gap takes a ledger keyed on the created row's id,
// idempotent under retry, with the counts derived from it rather than
// accumulated; the number feeds a progress display and the cost estimator's
// ratios. Recorded as open work in STATUS.md rather than papered over here.
//
// Unfenced on the run's liveness and on the connection's generation. The row
// exists; a cancelled run and a rebound connection do not un-create it, and
// refusing the count would only misreport it.
//
// Detached, because a creation that lands as the job's context expires must
// still be counted, and that context is the commonest thing to die here.
func (c *pageProgress) counted(ctx context.Context, outcome EnsureOutcome) {
	if c == nil {
		return
	}
	people, organizations := 0, 0
	if outcome.PersonCreated {
		people = 1
	}
	if outcome.OrganizationCreated {
		organizations = 1
	}
	if people == 0 && organizations == 0 {
		// Resolved onto rows that already existed — on a widen re-import that is
		// nearly every message.
		return
	}
	countCtx, cancel := detachedWrite(ctx)
	defer cancel()
	err := database.WithWorkspaceTx(countCtx, c.registry.pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(countCtx, `
			UPDATE capture_backfill
			SET people_created = people_created + $2, organizations_created = organizations_created + $3
			WHERE id = $1`, c.backfillID, people, organizations)
		return execErr
	})
	if err != nil {
		slog.ErrorContext(ctx, "capture: a counterparty this backfill created was not counted on the run — its reported reach is one row short",
			"backfill_id", c.backfillID, "err", err)
	}
}

// flushPacing says whether a write may be dropped for being too soon. Only the
// message tally is written this way, and the next report restates it.
type flushPacing bool

const (
	paced   flushPacing = true
	unpaced flushPacing = false
)

// persist writes the current tally to the run row, honouring the registry's
// progressPacing when the caller allows it. Caller holds the lock.
//
// A failure is logged and dropped rather than returned, and that is the
// deliberate call: this write exists so a screen can move, and failing a
// captured message — a real, committed CRM row — because its progress ping
// did not land would trade the product for the indicator. The next message
// restates the absolute tally, and the page's own commit reconciles
// regardless, so a lost flush costs one frame of animation and nothing else.
func (c *pageProgress) persist(ctx context.Context, pacing flushPacing) {
	now := c.registry.now()
	if pacing == paced && !c.lastFlush.IsZero() && now.Sub(c.lastFlush) < c.registry.progressPacing {
		return
	}
	c.lastFlush = now
	err := c.registry.flushBackfillProgress(ctx, c.backfillID, c.generation, c.tally)
	if err == nil {
		return
	}
	if ctx.Err() != nil {
		// The worker is shutting down or the job timed out. Every remaining
		// message in the page would fail the same way, and "the import is
		// unaffected" would be a lie — the page is ending too.
		return
	}
	slog.WarnContext(ctx, "capture: the backfill's live progress was not written — the import is unaffected and the page's commit will reconcile it",
		"backfill_id", c.backfillID, "err", err)
}

// flushBackfillProgress stores the running page's tally on the run row.
//
// It also promotes a 'queued' run to 'running', because by the time a page has
// walked a message the run demonstrably IS running — leaving it queued would
// put "Import queued" above a set of numbers that are climbing.
//
// It carries BOTH fences the commit carries, for the same reasons. The live
// states, so a run someone cancelled — or one a fault already ended — is not
// resurrected by a page that has not noticed yet. And the connection
// generation, so a page still walking the account the connection was rebound
// away from cannot report that account's mail as this run's progress: the
// commit will refuse the same page and cancel the run, and until it does the
// screen must not show work that is about to be thrown away.
func (r *Registry) flushBackfillProgress(ctx context.Context, backfillID ids.UUID, generation int, t pageTally) error {
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_backfill
			SET inflight_scanned = $2, inflight_captured = $3, inflight_skipped = $4,
			    status = CASE WHEN status = 'queued' THEN 'running' ELSE status END
			WHERE id = $1 AND status IN ('queued','running')
			  AND EXISTS (SELECT 1 FROM capture_connection c
			              WHERE c.id = capture_backfill.connection_id AND c.generation = $5)`,
			backfillID, t.scanned, t.captured, t.skipped, generation)
		return err
	})
}

// resetInflightProgress is what the page COMMIT carries: the commit folds the
// page's work into the committed columns from the connector's own result and
// the collector's totals, so the transient copy has done its job and goes.
//
// It BEGINS with a comma, so it splices in after an existing SET assignment
// and never as the first one.
const resetInflightProgress = `, inflight_scanned = 0, inflight_captured = 0, inflight_skipped = 0`
