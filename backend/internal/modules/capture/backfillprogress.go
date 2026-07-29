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
	scanned       int
	captured      int
	skipped       int
	people        int
	organizations int
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

// counted folds one ensure's outcome into the page's yield. An ensure that
// resolved onto records that already exist moves no counter and writes
// nothing — on a widen re-import that is nearly every message.
//
// It flushes rather than leaving the yield for the connector's next report,
// because the seam does not oblige a connector to report AFTER capturing a
// message. Both flushes write the whole tally, so whichever wins the pacing
// window carries the other's numbers too.
func (c *pageProgress) counted(ctx context.Context, outcome EnsureOutcome) {
	if c == nil {
		return
	}
	if !outcome.PersonCreated && !outcome.OrganizationCreated {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if outcome.PersonCreated {
		c.tally.people++
	}
	if outcome.OrganizationCreated {
		c.tally.organizations++
	}
	// Unpaced, unlike the message tally. A yield write is rare — only an ensure
	// that actually minted a row reaches here — and it is the ONE number a
	// page-ending write promotes out of the row rather than out of this
	// collector, so a paced-away increment would be lost for good.
	c.persist(ctx, unpaced)
}

// totals reads the page's yield, for the commit that folds it into the run's
// own counter columns.
func (c *pageProgress) totals() (people, organizations int) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tally.people, c.tally.organizations
}

// flushPacing says whether a write may be dropped for being too soon. The
// message tally is paced (the next report restates it); the counterparty
// yields are not (nothing restates them).
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
			    inflight_people = $5, inflight_organizations = $6,
			    status = CASE WHEN status = 'queued' THEN 'running' ELSE status END
			WHERE id = $1 AND status IN ('queued','running')
			  AND EXISTS (SELECT 1 FROM capture_connection c
			              WHERE c.id = capture_backfill.connection_id AND c.generation = $7)`,
			backfillID, t.scanned, t.captured, t.skipped, t.people, t.organizations, generation)
		return err
	})
}

// resetInflightProgress is what the page COMMIT carries: the commit folds the
// page's work into the committed columns from the connector's own result and
// the collector's totals, so the transient copy has done its job and goes.
//
// It BEGINS with a comma, so it splices in after an existing SET assignment
// and never as the first one.
const resetInflightProgress = `, inflight_scanned = 0, inflight_captured = 0, inflight_skipped = 0,
	    inflight_people = 0, inflight_organizations = 0`

// settleInflightProgress is what every OTHER page-ending write carries — a
// transient fault, a terminal failure, a cancel. It keeps the counterparty
// yields and drops only the message tally, because the two halves survive a
// failed page differently:
//
//   - scanned/captured/skipped describe messages the retry will walk again and
//     restate, so keeping them would double-count.
//   - people/organizations describe rows that ALREADY EXIST. Capture is
//     idempotent on the natural key, so a replayed message returns
//     created=false and never reaches the counterparty resolver again — the
//     retry cannot re-count them, and anything dropped here is undercounted
//     for the life of the run.
//
// Promoting from the ROW rather than from the collector is deliberate: cancel
// runs in the api process, which holds no collector for a page the worker is
// still walking. The same statement zeroes what it promoted, so no later write
// can promote it twice.
const settleInflightProgress = `, people_created = people_created + inflight_people,
	    organizations_created = organizations_created + inflight_organizations` + resetInflightProgress
