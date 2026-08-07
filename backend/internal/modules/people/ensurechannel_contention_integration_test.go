// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// What the channel ensure does when another transaction is already working on
// the same account — the sibling file holds its single-writer behaviour. Every
// case here is an ORDERING, so none of them may depend on scheduling: the two
// first messages let Postgres block the loser on the speculative insert, the
// erasure case holds the account's lock across the whole call and bounds the
// waiter, the merge case opens the window by hand in the order the two
// transactions really interleave, and the handle races hold a row and wait for
// a provably blocked backend before releasing it. No sleep and no guess: the
// one clock is probeBudget, a give-up bound on waiting for a backend that never
// blocks, and it decides when to REPORT a miss rather than what to assert.

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Two first messages from one new sender arriving at once. The bind is the
// arbiter, so the loser's speculatively-created person, its audit row and its
// outbox event all have to leave no trace — otherwise one human ends up on two
// records with the conversation on only one of them. No sleep and no polling:
// Postgres blocks the loser on the speculative-insert lock until the winner
// commits, so the database is the synchronizer.
func TestTwoConcurrentFirstChannelMessagesConvergeOnOnePerson(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	const name = "Concurrent Channel Sender"
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880501", Username: "racer"}

	// Both activities are captured up front: a t.Fatal from inside a goroutine
	// is not the test's to make, and the race under test is the ensure's.
	inputs := [2]EnsureChannelCounterpartyInput{
		e.channelEnsureInput(ctx, t, ci, name),
		e.channelEnsureInput(ctx, t, ci, name),
	}

	barrier := make(chan struct{})
	results := make([]EnsureChannelCounterpartyResult, len(inputs))
	failures := make([]error, len(inputs))
	var running sync.WaitGroup
	running.Add(len(inputs))
	for slot, in := range inputs {
		go func(slot int, in EnsureChannelCounterpartyInput) {
			defer running.Done()
			<-barrier
			results[slot], failures[slot] = e.store.EnsureChannelCounterparty(ctx, in)
		}(slot, in)
	}
	close(barrier)
	running.Wait()

	for slot, err := range failures {
		if err != nil {
			t.Fatalf("ensure %d: %v", slot, err)
		}
	}
	if results[0].PersonID != results[1].PersonID {
		t.Fatalf("the two messages landed on %s and %s; one sender is one person",
			results[0].PersonID, results[1].PersonID)
	}
	created := 0
	for _, res := range results {
		if res.PersonCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("%d of the two ensures reported creating the person, want exactly 1 — the loser adopted, it did not create", created)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person WHERE full_name = $1 AND archived_at IS NULL`, name); n != 1 {
		t.Fatalf("%d person rows, want 1 — the loser's speculative person must leave no trace", n)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE channel_user_id = $1 AND archived_at IS NULL`,
		ci.ChannelUserID); n != 1 {
		t.Fatalf("%d live bindings, want 1", n)
	}
	// Both activities are on the surviving person's timeline: the loser adopted
	// the winner's record and linked to it, rather than dropping its message.
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM activity_link WHERE person_id = $1 AND entity_type = 'person'`,
		results[0].PersonID); n != len(inputs) {
		t.Fatalf("%d activity links on the surviving person, want %d", n, len(inputs))
	}
}

// lockWaitBoundedStore is a Store whose statements refuse to WAIT for a lock.
// It makes a contended lock decide the outcome instead of the clock: a call
// that takes the lock under test fails immediately rather than blocking for as
// long as the holder lives, and a call that never takes it never waits at all.
// The bound is a failure guard, not a race — the holding transaction stays open
// across the whole call below.
func lockWaitBoundedStore(t *testing.T) *Store {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("MARGINCE_TEST_APP_DSN"))
	if err != nil {
		t.Fatalf("parsing the app DSN: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "250ms"
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		database.RegisterIDTypes(conn)
		return nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening the lock-bounded pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool)
}

// An Art. 17 erasure holds the subject's account lock from its purge until its
// suppression row commits, so the ensure has to take that same lock before it
// reads the suppression list. Without it the ensure runs at READ COMMITTED: it
// probes, sees nothing, and binds a LIVE identity to a brand-new person while
// the erasure is destroying the old one — and that rival is not covered by the
// erasure's own person-scoped writes, so it survives with the erased human's
// name and account, reachable by any rep.
//
// The state seeded here is the one that makes a rival possible at all: the
// subject is archived, so their binding is archived too (people/person.go), and
// uq_person_channel_identity is partial on archived_at IS NULL — the account is
// free for a second live binding.
func TestEnsureRefusesToCreateARivalDuringErasure(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	const subjectName = "Erasing Subject"
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880601", Username: "erasing"}

	subject := e.seedPerson(e.as(), t, subjectName, nil, nil)
	e.bindIdentity(e.as(), t, subject, ci)
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE person SET archived_at = now() WHERE id = $1`, subject); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE person_channel_identity SET archived_at = now() WHERE person_id = $1`, subject)
		return err
	}); err != nil {
		t.Fatalf("putting the subject into the state an erasure works through: %v", err)
	}

	in := e.channelEnsureInput(ctx, t, ci, subjectName)
	bounded := lockWaitBoundedStore(t)
	var ensureErr error
	if err := database.WithWorkspaceTx(ctx, e.store.pool, func(tx pgx.Tx) error {
		if err := storekit.LockChannelIdentities(ctx, tx, []storekit.ChannelIdentityKey{
			{Provider: ci.Provider, ChannelUserID: ci.ChannelUserID},
		}); err != nil {
			return err
		}
		_, ensureErr = bounded.EnsureChannelCounterparty(ctx, in)
		return nil
	}); err != nil {
		t.Fatalf("holding the account's identity lock: %v", err)
	}

	var pgErr *pgconn.PgError
	if !errors.As(ensureErr, &pgErr) || pgErr.Code != pgerrcode.LockNotAvailable {
		t.Fatalf("the ensure returned %v, want a lock-wait timeout — it did not take the account's identity lock, so it can bind a live identity in the middle of an erasure of the same human", ensureErr)
	}
	if n := e.countInWorkspace(ctx, t, `
		SELECT count(*) FROM person_channel_identity
		 WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
		ci.Provider, ci.ChannelUserID); n != 0 {
		t.Errorf("%d live bindings for the account being erased, want 0", n)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person WHERE full_name = $1 AND archived_at IS NULL`, subjectName); n != 0 {
		t.Errorf("%d live people hold the erased human's name, want 0 — the erasure never named them, so nothing will ever erase them", n)
	}
}

// A merge can commit between the moment the ensure names its person and the
// moment it links the message to them, and nothing walks merged_into_id for
// activity links: the message would land on the record the merge retired, where
// no reader looks. The window is opened by hand here, in the order the two
// transactions really interleave — the resolve, then the whole merge, then the
// link — so the outcome depends on the code and not on scheduling.
func TestAnInboundMessageConcurrentWithAMergeLinksTheSurvivor(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880701", Username: "merging"}

	first, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, ci, "Merging Mara"))
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	survivor := e.seedPerson(e.as(), t, "Mara Survivor", []string{"mara@merge.test"}, nil)

	in := e.channelEnsureInput(ctx, t, ci, "Merging Mara")
	var res EnsureChannelCounterpartyResult
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := e.store.resolveChannelPerson(ctx, tx, in, &res); err != nil {
			return err
		}
		if _, err := e.store.MergePerson(e.as(), res.PersonID, survivor); err != nil {
			return err
		}
		return e.store.linkActivityToPerson(ctx, tx, in.ActivityID, res.PersonID)
	}); err != nil {
		t.Fatalf("the ensure spanning a merge: %v", err)
	}
	if res.PersonID != first.PersonID {
		t.Fatalf("the second message resolved to %s, want the incumbent %s — this test needs the merged-away record to be the one the resolve named",
			res.PersonID, first.PersonID)
	}

	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM activity_link WHERE activity_id = $1 AND entity_type = 'person' AND person_id = $2`,
		in.ActivityID, survivor); n != 1 {
		t.Errorf("%d links to the surviving record %s, want 1 — a message on the merged-away id is invisible on the record the human is now on",
			n, survivor)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM activity_link WHERE activity_id = $1 AND entity_type = 'person' AND person_id = $2`,
		in.ActivityID, first.PersonID); n != 0 {
		t.Errorf("%d links still name the merged-away person %s", n, first.PersonID)
	}
}

// handleUpdatedEventCount counts the person.updated envelopes a handle refresh
// staged. event_outbox is a global infra table outside RLS, so the count scopes
// itself on the envelope's own workspace and subject.
func (e *dedupeEnv) handleUpdatedEventCount(ctx context.Context, t *testing.T, personID ids.PersonID) int {
	t.Helper()
	return e.countInWorkspace(ctx, t, `
		SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'person.updated'
		   AND envelope->>'workspace_id' = $1::text
		   AND envelope->'entity'->>'id' = $2::text
		   AND envelope->'payload'->'changed_fields'->'channel_username' IS NOT NULL`,
		e.ws, personID)
}

// beginBlockingRefresh runs one whole handle refresh in a transaction the test
// holds open, so the binding's row lock is provably held while a second refresh
// runs against it. It returns that transaction and its backend pid: the pid is
// what turns "the other refresh is waiting" into an exact question instead of a
// guess about timing.
func (e *dedupeEnv) beginBlockingRefresh(ctx context.Context, t *testing.T, ci connector.ChannelIdentity) (pgx.Tx, int) {
	t.Helper()
	tx, err := e.store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("opening the blocking transaction: %v", err)
	}
	// Released whatever happens below. A failure before the commit would
	// otherwise leave this transaction holding a pooled connection and the row
	// lock with it, and the test that meant to fail loudly would hang instead.
	t.Cleanup(func() {
		err := tx.Rollback(context.Background())
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("releasing the blocking transaction: %v", err)
		}
	})
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, e.ws.String()); err != nil {
		t.Fatalf("binding the workspace GUC: %v", err)
	}
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("reading the blocking backend pid: %v", err)
	}
	if err := refreshChannelUsername(ctx, tx, ci); err != nil {
		t.Fatalf("the refresh that holds the row: %v", err)
	}
	return tx, pid
}

// refreshInBackground runs a refresh on its own transaction and reports what it
// returned — the second inbound message, arriving while the first is still
// uncommitted.
func (e *dedupeEnv) refreshInBackground(ctx context.Context, ci connector.ChannelIdentity) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- e.store.tx(ctx, func(tx pgx.Tx) error {
			return refreshChannelUsername(ctx, tx, ci)
		})
	}()
	return done
}

// probeBudget bounds the wait for a racing backend to block.
//
// It is a DURATION, and it used to be a count of 20 000 probes. A count is not a
// budget: it is a race between how fast probe round-trips complete and how fast
// the writer reaches its lock, and the lane's own concurrency slows BOTH, so a
// count generous on an idle machine is not generous on a loaded one. Three
// tests here learned that by failing on CI and passing on re-run. A duration
// means the same thing on every machine.
//
// Generous enough that only a genuine miss trips it, short enough that the miss
// reports itself rather than running into the package timeout, where it would
// read as a hung suite instead of a stated fact.
const probeBudget = 60 * time.Second

// waitUntilBlocked returns once a backend in this database is provably waiting
// on a lock held by pid, or once the racer finishes first — reporting WHICH, so
// the caller decides what that means. For the refresh races below "finished
// first" means the run proved nothing and must fail loudly; for the
// organization-name lock it is the expected answer on the path that owes no
// lock. One probe, two policies.
//
// Busy-read of pg_stat_activity, with the pid making it exact: that view is
// cluster-wide and the parallel lane runs a dozen packages against one server.
// A run in which nothing ever blocked reports that instead of passing having
// proved nothing.
func waitUntilBlocked[T any](t *testing.T, probe pgx.Tx, pid int, done <-chan T) (T, bool) {
	t.Helper()
	var zero T
	ctx, cancel := context.WithTimeout(context.Background(), probeBudget)
	defer cancel()
	for probes := 1; ; probes++ {
		var blocked bool
		switch err := probe.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM pg_stat_activity a
			   WHERE a.datname = current_database() AND $1 = ANY (pg_blocking_pids(a.pid)))`,
			pid).Scan(&blocked); {
		case err != nil && ctx.Err() != nil:
			t.Fatalf("no backend waited on the held row within %s (%d probes): the writer neither "+
				"reached the lock nor returned, so this run proved nothing", probeBudget, probes)
		case err != nil:
			t.Fatalf("probing for a waiting backend: %v", err)
		}
		if blocked {
			return zero, false
		}
		select {
		case result := <-done:
			return result, true
		default:
		}
		if ctx.Err() != nil {
			t.Fatalf("no backend waited on the held row within %s (%d probes): the writer neither "+
				"reached the lock nor returned, so this run proved nothing", probeBudget, probes)
		}
		// Yield rather than spin. This loop and the goroutine it is waiting for
		// compete for the same processor under the lane's concurrency, and a
		// tight loop that never yields is one of the ways a loaded runner starves
		// the very writer whose progress it is watching for.
		runtime.Gosched()
	}
}

// waitUntilBlockedBy is waitUntilBlocked for a racer that reports only an error.
func waitUntilBlockedBy(t *testing.T, probe pgx.Tx, pid int, done <-chan error) (bool, error) {
	t.Helper()
	err, finished := waitUntilBlocked(t, probe, pid, done)
	return !finished, err
}

// mustBlockOn is waitUntilBlockedBy for a caller where finishing without ever
// waiting means the run exercised no ordering at all.
func mustBlockOn(t *testing.T, probe pgx.Tx, pid int, done <-chan error) {
	t.Helper()
	if blocked, err := waitUntilBlockedBy(t, probe, pid, done); !blocked {
		t.Fatalf("the second writer finished (%v) without ever waiting on the first — "+
			"it never reached the binding, so this run exercises no ordering at all", err)
	}
}

// The trail has to name the handle a refresh actually displaced. Two messages
// from one renaming sender can be in flight at once, and the second one's
// before-image is read while the first is still uncommitted: an image taken
// from that pre-block read names a handle the account had two writes ago, so
// the trail shows a rename that never happened and hides the one that did.
func TestConcurrentUsernameRefreshesAuditTheRealPriorValue(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880801", Username: "handle_a"}
	bound, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, ci, "Renaming Twice"))
	if err != nil {
		t.Fatalf("the first ensure: %v", err)
	}

	toB, toC := ci, ci
	toB.Username, toC.Username = "handle_b", "handle_c"
	blocker, pid := e.beginBlockingRefresh(ctx, t, toB)
	displaced := e.refreshInBackground(ctx, toC)
	mustBlockOn(t, blocker, pid, displaced)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("committing the first rename: %v", err)
	}
	if err := <-displaced; err != nil {
		t.Fatalf("the second rename: %v", err)
	}

	if was, is := e.newestHandleImages(ctx, t, bound.PersonID); was == nil || *was != toB.Username || is == nil || *is != toC.Username {
		t.Fatalf("the second rename audited %s → %s, want %q → %q — the before-image must name the handle this write displaced, not one it never saw",
			handleText(was), handleText(is), toB.Username, toC.Username)
	}
	if n := e.handleAuditCount(ctx, t, bound.PersonID); n != 2 {
		t.Errorf("%d handle audit rows after two renames, want 2", n)
	}
	var stored string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT username FROM person_channel_identity
			 WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
			ci.Provider, ci.ChannelUserID).Scan(&stored)
	}); err != nil {
		t.Fatal(err)
	}
	if stored != toC.Username {
		t.Errorf("stored handle = %q, want the one the later message reported (%q)", stored, toC.Username)
	}
}

// The mirror case, and the one that costs a reader most: both in-flight
// messages report the SAME new handle, which is what two messages from a sender
// who renamed once actually carry. It is one rename, so it owes exactly one
// audit row and one person.updated — a second envelope for a change already
// published is a rename subscribers see twice and a version bump nobody made.
func TestTwoMessagesReportingTheSameRenameAuditItOnce(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880901", Username: "handle_a"}
	bound, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, ci, "Renaming Once"))
	if err != nil {
		t.Fatalf("the first ensure: %v", err)
	}

	renamed := ci
	renamed.Username = "handle_b"
	blocker, pid := e.beginBlockingRefresh(ctx, t, renamed)
	second := e.refreshInBackground(ctx, renamed)
	mustBlockOn(t, blocker, pid, second)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("committing the rename: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("the second message's refresh: %v", err)
	}

	if n := e.handleAuditCount(ctx, t, bound.PersonID); n != 1 {
		t.Errorf("%d handle audit rows for one rename, want 1 — the second message displaced nothing", n)
	}
	if n := e.handleUpdatedEventCount(ctx, t, bound.PersonID); n != 1 {
		t.Errorf("%d person.updated events for one rename, want 1 — every subscriber sees the rename twice", n)
	}
}
