//go:build integration

package bus

// Real-infrastructure lane for the event backbone (B-EP04.4/.6/.13):
// relay exactly-once + crash-safety + commit order against a migrated
// Postgres, subscriber consume/ack/reclaim + workspace filtering and the
// dedupe wrapper against a real Redis. In-package on purpose: the tests
// drive relayBatch and shorten the pending-window knobs directly.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/fable-poc/internal/pg"
	"github.com/gradionhq/fable-poc/internal/pgmigrate"
	"github.com/gradionhq/fable-poc/kernel/events"
	"github.com/gradionhq/fable-poc/kernel/ids"
	"github.com/gradionhq/fable-poc/migrations"
)

// testDB is Redis database 15: isolated from the dev server's default DB
// so FlushDB between tests cannot eat a developer's local streams.
const testDB = 15

type busEnv struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	ws   ids.UUID
}

func setup(t *testing.T) *busEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	redisAddr := os.Getenv("MARGINCE_TEST_REDIS")
	if ownerDSN == "" || appDSN == "" || redisAddr == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN / MARGINCE_TEST_REDIS not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := t.Context()

	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading migrations: %v", err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatalf("loading custom migrations: %v", err)
	}
	if _, err := pgmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	// The relay needs a workspace row only because fixtures reference one
	// in their envelopes; the outbox itself is infra-owned (no RLS).
	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Bus Test', 'bus-test', 'EUR')`, ws); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}

	pool, err := pg.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr, DB: testDB})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis at %s unreachable — run `make db-up`: %v", redisAddr, err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushing test redis db: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	return &busEnv{pool: pool, rdb: rdb, ws: ws}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// stage writes an outbox row the way a domain transaction would: a
// complete, validated envelope staged for the relay.
func (e *busEnv) stage(t *testing.T, eventType string, entityID ids.UUID) events.Envelope {
	t.Helper()
	env := events.Envelope{
		EventID:     ids.NewV7(),
		Type:        eventType,
		Version:     1,
		WorkspaceID: e.ws,
		OccurredAt:  time.Now().UTC(),
		Actor:       events.Actor{Type: "human", ID: "human:" + ids.NewV7().String()},
		Entity:      events.EntityRef{Type: "person", ID: entityID},
		Trace:       events.Trace{CorrelationID: ids.NewV7(), AuditLogID: ids.NewV7()},
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("fixture envelope: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := events.StreamFor(eventType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(),
		`INSERT INTO event_outbox (stream, envelope) VALUES ($1, $2)`, stream, raw); err != nil {
		t.Fatalf("staging outbox row: %v", err)
	}
	return env
}

func (e *busEnv) relay(t *testing.T) *Relay {
	t.Helper()
	return NewRelay(e.pool, e.rdb, testLogger())
}

func (e *busEnv) streamEventIDs(t *testing.T, stream string) []string {
	t.Helper()
	entries, err := e.rdb.XRange(t.Context(), stream, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRANGE %s: %v", stream, err)
	}
	var eventIDs []string
	for _, entry := range entries {
		env, err := decodeEnvelope(entry)
		if err != nil {
			t.Fatalf("stream %s carries an undecodable entry: %v", stream, err)
		}
		eventIDs = append(eventIDs, env.EventID.String())
	}
	return eventIDs
}

func TestRelayShipsACommittedRowExactlyOnceInSteadyState(t *testing.T) {
	e := setup(t)
	env := e.stage(t, "person.created", ids.NewV7())

	relay := e.relay(t)
	if n, err := relay.relayBatch(t.Context()); err != nil || n != 1 {
		t.Fatalf("first pass: published %d, err %v; want 1, nil", n, err)
	}

	got := e.streamEventIDs(t, "gw:events:crm:person")
	if len(got) != 1 || got[0] != env.EventID.String() {
		t.Fatalf("stream carries %v, want exactly [%s]", got, env.EventID)
	}

	var unpublished int
	if err := e.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM event_outbox WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
		t.Fatal(err)
	}
	if unpublished != 0 {
		t.Fatalf("%d rows still unpublished after relay pass", unpublished)
	}

	// Steady state: a second pass ships nothing new.
	if n, err := relay.relayBatch(t.Context()); err != nil || n != 0 {
		t.Fatalf("second pass: published %d, err %v; want 0, nil", n, err)
	}
	if got := e.streamEventIDs(t, "gw:events:crm:person"); len(got) != 1 {
		t.Fatalf("second pass duplicated the entry: %v", got)
	}
}

func TestRelayCrashBeforeStampRepublishes_atLeastOnce(t *testing.T) {
	e := setup(t)
	env := e.stage(t, "deal.created", ids.NewV7())

	relay := e.relay(t)
	if _, err := relay.relayBatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash window: the XADD happened, the stamp did not —
	// on disk that is exactly a row whose published_at never committed.
	if _, err := e.pool.Exec(t.Context(),
		`UPDATE event_outbox SET published_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.relayBatch(t.Context()); err != nil {
		t.Fatal(err)
	}

	got := e.streamEventIDs(t, "gw:events:crm:deal")
	if len(got) != 2 || got[0] != env.EventID.String() || got[1] != env.EventID.String() {
		t.Fatalf("restart after crash should re-publish the entry (≥ once): got %v", got)
	}

	// The duplicate is the consumer's problem by design — Dedupe absorbs it.
	var effects atomic.Int32
	handler := Dedupe(e.rdb, "cg:read-model", func(context.Context, events.Envelope) error {
		effects.Add(1)
		return nil
	})
	entries, err := e.rdb.XRange(t.Context(), "gw:events:crm:deal", "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		decoded, err := decodeEnvelope(entry)
		if err != nil {
			t.Fatal(err)
		}
		if err := handler(t.Context(), decoded); err != nil {
			t.Fatal(err)
		}
	}
	if effects.Load() != 1 {
		t.Fatalf("dedupe let the re-published event take effect %d times, want 1", effects.Load())
	}
}

func TestRelayPreservesCommitOrderPerEntity(t *testing.T) {
	e := setup(t)
	entity := ids.NewV7()
	first := e.stage(t, "person.created", entity)
	second := e.stage(t, "person.updated", entity)
	third := e.stage(t, "person.archived", entity)

	if _, err := e.relay(t).relayBatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{first.EventID.String(), second.EventID.String(), third.EventID.String()}
	got := e.streamEventIDs(t, "gw:events:crm:person")
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("stream order %v, want commit order %v", got, want)
	}
}

// consumeUntil runs a subscriber until the predicate holds or the
// deadline passes — the polling half of every subscriber test.
func consumeUntil(t *testing.T, s *Subscriber, deadline time.Duration, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = s.Run(ctx)
	}()

	waited := time.NewTimer(deadline)
	defer waited.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if done() {
				cancel()
				<-finished
				return
			}
		case <-waited.C:
			cancel()
			<-finished
			if !done() {
				t.Fatal("subscriber did not reach the expected state before the deadline")
			}
			return
		}
	}
}

func TestSubscriberDeliversAcksAndFiltersWorkspaces(t *testing.T) {
	e := setup(t)
	mine := e.stage(t, "person.created", ids.NewV7())

	// A second tenant's event on the same stream (workspace is a field,
	// not a stream — events.md §4.1).
	otherWS := ids.NewV7()
	foreign := mine
	foreign.EventID = ids.NewV7()
	foreign.WorkspaceID = otherWS
	raw, _ := json.Marshal(foreign)
	if _, err := e.pool.Exec(t.Context(),
		`INSERT INTO event_outbox (stream, envelope) VALUES ('gw:events:crm:person', $1)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := e.relay(t).relayBatch(t.Context()); err != nil {
		t.Fatal(err)
	}

	var seen atomic.Int32
	var sawForeign atomic.Bool
	group := events.Group{Name: "cg:read-model", Streams: []string{"gw:events:crm:person"}}
	s := NewSubscriber(e.rdb, group, ForWorkspace(e.ws, func(_ context.Context, env events.Envelope) error {
		if env.WorkspaceID != e.ws {
			sawForeign.Store(true)
		}
		seen.Add(1)
		return nil
	}), testLogger())
	s.block = 100 * time.Millisecond

	consumeUntil(t, s, 5*time.Second, func() bool { return seen.Load() >= 1 })

	if sawForeign.Load() {
		t.Fatal("a handler scoped to workspace A saw workspace B's event")
	}
	if seen.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1 (own event only)", seen.Load())
	}

	// Both entries must be acked — the foreign one is filtered, not stuck.
	pending, err := e.rdb.XPending(t.Context(), "gw:events:crm:person", group.Name).Result()
	if err != nil {
		t.Fatal(err)
	}
	if pending.Count != 0 {
		t.Fatalf("%d entries still pending; filtering must ack, not strand", pending.Count)
	}
}

func TestSubscriberReclaimRedeliversAfterHandlerCrash(t *testing.T) {
	e := setup(t)
	env := e.stage(t, "lead.created", ids.NewV7())
	if _, err := e.relay(t).relayBatch(t.Context()); err != nil {
		t.Fatal(err)
	}

	// First delivery "crashes" (handler errors → no ack); once the entry
	// has sat pending past minIdle, the reclaim pass re-delivers it.
	var attempts atomic.Int32
	group := events.Group{Name: "cg:workflows", Streams: []string{"gw:events:crm:lead"}}
	s := NewSubscriber(e.rdb, group, func(_ context.Context, got events.Envelope) error {
		if got.EventID != env.EventID {
			t.Errorf("delivered %s, staged %s", got.EventID, env.EventID)
		}
		if attempts.Add(1) == 1 {
			return fmt.Errorf("simulated consumer crash")
		}
		return nil
	}, testLogger())
	s.block = 100 * time.Millisecond
	s.minIdle = 200 * time.Millisecond

	consumeUntil(t, s, 10*time.Second, func() bool { return attempts.Load() >= 2 })

	pending, err := e.rdb.XPending(t.Context(), "gw:events:crm:lead", group.Name).Result()
	if err != nil {
		t.Fatal(err)
	}
	if pending.Count != 0 {
		t.Fatalf("entry still pending after successful redelivery (count %d)", pending.Count)
	}
}

func TestSubscriberEnsuresEverySpecGroup(t *testing.T) {
	e := setup(t)
	noop := func(context.Context, events.Envelope) error { return nil }
	for _, group := range events.Groups() {
		s := NewSubscriber(e.rdb, group, noop, testLogger())
		if err := s.ensureGroups(t.Context()); err != nil {
			t.Fatalf("declaring %s: %v", group.Name, err)
		}
		// Idempotent: a second declare (a replica booting) must not fail.
		if err := s.ensureGroups(t.Context()); err != nil {
			t.Fatalf("re-declaring %s: %v", group.Name, err)
		}
		for _, stream := range group.Streams {
			groups, err := e.rdb.XInfoGroups(t.Context(), stream).Result()
			if err != nil {
				t.Fatalf("XINFO GROUPS %s: %v", stream, err)
			}
			found := false
			for _, g := range groups {
				found = found || g.Name == group.Name
			}
			if !found {
				t.Errorf("group %s missing on %s", group.Name, stream)
			}
		}
	}
}

// The dedupe mark must be written AFTER the effect: a mark that precedes
// it would, on a crash in between, swallow the redelivery and drop the
// event. This test pins both halves — no mark while the effect has not
// succeeded (so a crash there re-runs, never drops), and a mark once it
// has (so the third delivery is absorbed).
func TestDedupeMarksOnlyAfterTheEffectSucceeded(t *testing.T) {
	e := setup(t)
	env := e.stage(t, "person.created", ids.NewV7())
	markKey := dedupeKey("cg:context-graph", env)

	var calls atomic.Int32
	handler := Dedupe(e.rdb, "cg:context-graph", func(context.Context, events.Envelope) error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("transient effect failure")
		}
		return nil
	})

	if err := handler(t.Context(), env); err == nil {
		t.Fatal("first attempt should surface the handler failure")
	}
	if seen, _ := e.rdb.Exists(t.Context(), markKey).Result(); seen != 0 {
		t.Fatal("mark exists although the effect never ran — a crash here would drop the event")
	}
	if err := handler(t.Context(), env); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if seen, _ := e.rdb.Exists(t.Context(), markKey).Result(); seen != 1 {
		t.Fatal("no mark after a successful effect")
	}
	if err := handler(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2 (failure + one effect; third delivery deduped)", calls.Load())
	}
}
