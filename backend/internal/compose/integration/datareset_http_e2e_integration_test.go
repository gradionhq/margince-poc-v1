// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// resetResponse is the 200 body of POST /admin/reset-data as a client reads
// it. Spelled once and decoded by both tests below, so the endpoint's whole
// wire shape is exercised whether or not a scenario wired the surfaces the
// counts describe.
type resetResponse struct {
	Status           string `json:"status"`
	TablesCleared    int    `json:"tables_cleared"`
	JobsCancelled    int    `json:"jobs_cancelled"`
	StreamsPurged    int    `json:"streams_purged"`
	CacheKeysDeleted int    `json:"cache_keys_deleted"`
	ObjectsDeleted   int    `json:"objects_deleted"`
	DrainTimedOut    bool   `json:"drain_timed_out"`
}

// TestResetDataOverHTTP drives the non-production admin reset end to end over
// the real router and a real admin session (what the direct-handler test
// cannot see): the WithDataReset / WithNonProduction wiring, the non_production
// posture /me carries for the client gate, the confirmation refusal, and a
// successful reset. Reaching 200 also proves the live session path populates
// the admin RoleKeys that RequireAdmin gates on.
func TestResetDataOverHTTP(t *testing.T) {
	e := setupWithOptions(t,
		compose.WithDataReset(nil, deployconfig.Seeds{}, runtimeenv.Development),
		compose.WithNonProduction(runtimeenv.Development),
	)
	bootstrapWorkspaceSession(t, e, "Fable E2E", "ada@example.com", "Ada Admin")

	var me struct {
		NonProduction bool `json:"non_production"`
	}
	if code := e.call(t, "GET", "/v1/me", nil, nil, &me); code != 200 {
		t.Fatalf("GET /me = %d, want 200", code)
	}
	if !me.NonProduction {
		t.Fatal("me.non_production = false; want true under the Development posture")
	}

	// Wrong confirmation is refused before anything is deleted.
	if code := e.call(t, "POST", "/v1/admin/reset-data", anyMap{"confirmation": "wrong"}, nil, nil); code != 422 {
		t.Fatalf("reset with wrong confirmation = %d, want 422", code)
	}

	// The organization name resets the workspace to first-boot state.
	var out resetResponse
	if code := e.call(t, "POST", "/v1/admin/reset-data", anyMap{"confirmation": "Fable E2E"}, nil, &out); code != 200 {
		t.Fatalf("reset with the org name = %d, want 200", code)
	}
	if out.Status != "reset" {
		t.Fatalf("reset status = %q, want %q", out.Status, "reset")
	}
	if out.TablesCleared == 0 {
		t.Fatal("reset reported 0 tables cleared; the catalog-derived sweep set is never empty")
	}
	// This scenario wires no reset runtime and no object store, which is the
	// Postgres-only posture a role declares by omission. Every other count must
	// therefore read zero: a number here would mean the endpoint reported a
	// purge nothing performed.
	if out.JobsCancelled != 0 || out.StreamsPurged != 0 || out.CacheKeysDeleted != 0 || out.ObjectsDeleted != 0 {
		t.Errorf("out = %+v; a Postgres-only reset reports zero for every surface it was never given", out)
	}
	if out.DrainTimedOut {
		t.Error("drain_timed_out = true with no queue runtime wired; there was nothing to drain")
	}
}

// TestResetDataClearsQueueBusAndObjects is the whole-stack proof. Each purge
// already has its own lane, and each passes in isolation; what no other test
// can see is whether the ONE endpoint reaches all of them, reports a single
// consistent tally, and hands the bus back in a usable state. So every surface
// is seeded first, the reset runs over the real wire, and each is read back.
func TestResetDataClearsQueueBusAndObjects(t *testing.T) {
	rdb := resetTestRedis(t)
	store := blobstore.NewMemory()
	e := setupWithOptions(t,
		compose.WithDataReset(nil, deployconfig.Seeds{}, runtimeenv.Development),
		compose.WithNonProduction(runtimeenv.Development),
		compose.WithBlobstore(store),
		withResetRuntime(t, rdb),
	)
	bootstrapWorkspaceSession(t, e, "Fable E2E", "ada@example.com", "Ada Admin")
	ws := workspaceIDOf(t, e)

	probeStream := kevents.Streams()[0]
	seedQueuedJob(t, e, ws)
	seedBusEntry(t, rdb, probeStream)
	seedDedupeMark(t, rdb)
	objectKey := blobstore.WorkspaceKey(ws, "attachment", "probe")
	if err := store.Put(t.Context(), objectKey, strings.NewReader("bytes"), 5, "text/plain"); err != nil {
		t.Fatalf("seeding an object: %v", err)
	}
	// A second, unrelated tenant's object — the reset must never reach past its
	// own workspace prefix to find it. Without this, a purge scoped to an empty
	// prefix or to any prefix that happens to include the probe above would
	// look identical to a correctly scoped one.
	otherWS := ids.New[ids.WorkspaceKind]()
	otherKey := blobstore.WorkspaceKey(otherWS, "attachment", "probe")
	if err := store.Put(t.Context(), otherKey, strings.NewReader("bytes"), 5, "text/plain"); err != nil {
		t.Fatalf("seeding another workspace's object: %v", err)
	}

	var out resetResponse
	if code := e.call(t, "POST", "/v1/admin/reset-data", anyMap{"confirmation": "Fable E2E"}, nil, &out); code != 200 {
		t.Fatalf("reset = %d, want 200", code)
	}

	if out.JobsCancelled == 0 || out.StreamsPurged == 0 || out.CacheKeysDeleted == 0 || out.ObjectsDeleted == 0 {
		t.Errorf("out = %+v; every seeded surface must be reported cleared", out)
	}
	if out.DrainTimedOut {
		t.Error("drain_timed_out = true with no worker running a job")
	}
	if n := countJobRows(t, e, ws); n != 0 {
		t.Errorf("%d job rows survived; they would run against wiped data", n)
	}
	if n := streamLen(t, rdb, probeStream); n != 0 {
		t.Errorf("%d bus entries survived on %s", n, probeStream)
	}
	if n := dedupeMarkCount(t, rdb); n != 0 {
		t.Errorf("%d dedupe marks survived; a stale mark swallows the reseeded install's first redelivery", n)
	}
	if _, _, err := store.Get(t.Context(), objectKey); !errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("the object outlived the row that named it: %v", err)
	}
	// A reset clears its own tenant's objects and no one else's: the other
	// workspace's object must read back exactly as seeded.
	if r, obj, err := store.Get(t.Context(), otherKey); err != nil {
		t.Errorf("reset crossed a tenant boundary: another workspace's object was deleted: %v", err)
	} else {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("closing the other workspace's object: %v", cerr)
		}
		if obj.Size != 5 {
			t.Errorf("reset crossed a tenant boundary: another workspace's object changed size to %d", obj.Size)
		}
	}
	// The groups must be back, or every subscriber in the process is wedged on
	// NOGROUP — the failure a stream DEL causes and nothing else would report.
	for _, g := range kevents.Groups() {
		for _, stream := range g.Streams {
			if !groupExists(t, rdb, stream, g.Name) {
				t.Errorf("group %s missing from %s after the reset", g.Name, stream)
			}
		}
	}
	assertResetAuditEvidence(t, e, out)
}

// resetTestDrainWindow bounds the proof's quiesce. Short on purpose: no worker
// is running here, so a drain that needs longer than this is polling something
// other than the running-job count.
const resetTestDrainWindow = 5 * time.Second

// resetTestDrainPoll is the drain's re-read cadence. Positive by construction —
// jobs.Quiescer tickers on it.
const resetTestDrainPoll = 10 * time.Millisecond

// withResetRuntime wires the reset's non-Postgres purges exactly as the api
// role does (newResetRuntime in cmd/api, which lives in package main and so
// cannot be called from here), from inside a compose.Option.
//
// The option IS the answer to this proof's ordering problem: the runtime needs
// the harness's app pool, which setupWithOptions opens only after its caller
// has already spelled the option list. compose.Option is handed that same pool
// at apply time, so the assembly moves there — no second pool, no second River
// client, and no change to the setup every other suite in this package shares.
func withResetRuntime(t *testing.T, rdb *redis.Client) compose.Option {
	t.Helper()
	return func(s *compose.Server, pool *pgxpool.Pool) {
		runner, err := jobs.NewInserter(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("jobs.NewInserter: %v", err)
		}
		q := jobs.Quiescer{
			Runner: runner, Pool: pool,
			Timeout: resetTestDrainWindow, Interval: resetTestDrainPoll, Now: time.Now,
		}
		compose.WithResetRuntime(compose.ResetRuntime{
			QuiesceQueues: q.Quiesce,
			ResumeQueues:  q.Resume,
			PurgeQueue: func(ctx context.Context, ws ids.UUID) (int, error) {
				return jobs.PurgeWorkspace(ctx, pool, ws)
			},
			PurgeBus: func(ctx context.Context) (int, int, error) {
				streams, err := events.PurgeStreams(ctx, rdb, kevents.Groups())
				if err != nil {
					return 0, 0, err
				}
				keys, err := events.PurgeDedupe(ctx, rdb)
				if err != nil {
					return streams, 0, err
				}
				return streams, keys, nil
			},
			SignalReset: func(ctx context.Context, ws ids.UUID) error {
				return events.PublishReset(ctx, rdb, ws)
			},
		})(s, pool)
	}
}

// resetTestRedis gives this lane a real Redis on its own logical db, flushed
// first so the purge counts describe what THIS test seeded rather than what a
// previous run left. Never db 0 — a running `make dev` owns that one; the
// parallel runner hands each package a distinct index in 1..15 through
// MARGINCE_TEST_REDIS_DB.
func resetTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("MARGINCE_TEST_REDIS")
	if addr == "" {
		t.Fatal("MARGINCE_TEST_REDIS not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: resetTestRedisDB(t)})
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Errorf("closing the redis client: %v", err)
		}
	})
	// FlushDB doubles as the reachability check: a purge proof that ran against
	// an unreachable Redis would report zeros and read as a passing test.
	if err := rdb.FlushDB(t.Context()).Err(); err != nil {
		t.Fatalf("redis at %s unusable — run `make db-up`: %v", addr, err)
	}
	return rdb
}

// resetTestRedisDB reads the logical db this package was assigned. An
// out-of-range or non-numeric value fails loudly rather than silently eating
// the wrong db; absent the env (a bare `go test`), db 15 is the default.
func resetTestRedisDB(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("MARGINCE_TEST_REDIS_DB")
	if raw == "" {
		return 15
	}
	db, err := strconv.Atoi(raw)
	if err != nil || db < 1 || db > 15 {
		t.Fatalf("MARGINCE_TEST_REDIS_DB=%q is not a Redis db index in 1..15", raw)
	}
	return db
}

// workspaceIDOf answers the bootstrapped workspace as a typed id — what the
// object-key derivation takes, where callerWorkspace's raw string is enough for
// a SQL predicate.
func workspaceIDOf(t *testing.T, e *env) ids.WorkspaceID {
	t.Helper()
	ws, err := ids.ParseAs[ids.WorkspaceKind](callerWorkspace(t, e))
	if err != nil {
		t.Fatalf("the bootstrapped workspace id is not a UUID: %v", err)
	}
	return ws
}

// seedQueuedJob puts one of this workspace's own jobs on the queue. Available
// rather than running, so the drain reports a clean quiesce and the deletion is
// still required: queued work that outlives the reset runs against wiped data.
func seedQueuedJob(t *testing.T, e *env, ws ids.WorkspaceID) {
	t.Helper()
	seedRiverRow(t, e, riverRow{kind: "reset_probe", state: "available", workspace: ws.String()})
}

// countJobRows reports how many river_job rows still name this workspace.
func countJobRows(t *testing.T, e *env, ws ids.WorkspaceID) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(t.Context(),
		`SELECT count(*)::int FROM river_job WHERE args->>'workspace_id' = $1`,
		ws.String()).Scan(&n); err != nil {
		t.Fatalf("counting this workspace's job rows: %v", err)
	}
	return n
}

// seedBusEntry XADDs one entry onto a catalog stream — an event the reset must
// not leave pointing at rows it is about to delete.
func seedBusEntry(t *testing.T, rdb *redis.Client, stream string) {
	t.Helper()
	if err := rdb.XAdd(t.Context(), &redis.XAddArgs{
		Stream: stream, Values: map[string]any{"envelope": "{}"},
	}).Err(); err != nil {
		t.Fatalf("seeding an entry on %s: %v", stream, err)
	}
}

// seedDedupeMark writes one processed-event mark. Without it cache_keys_deleted
// would be zero for an honest reason, and a zero cannot tell a purge that ran
// from one that was never reached.
func seedDedupeMark(t *testing.T, rdb *redis.Client) {
	t.Helper()
	if err := rdb.Set(t.Context(), events.DedupeKeyPrefix+"cg:reset:probe", "1", time.Minute).Err(); err != nil {
		t.Fatalf("seeding a dedupe mark: %v", err)
	}
}

// dedupeMarkCount reports how many processed-event marks remain.
func dedupeMarkCount(t *testing.T, rdb *redis.Client) int {
	t.Helper()
	keys, err := rdb.Keys(t.Context(), events.DedupeKeyPrefix+"*").Result()
	if err != nil {
		t.Fatalf("KEYS %s*: %v", events.DedupeKeyPrefix, err)
	}
	return len(keys)
}

// streamLen reports how many entries a stream still holds.
func streamLen(t *testing.T, rdb *redis.Client, stream string) int64 {
	t.Helper()
	n, err := rdb.XLen(t.Context(), stream).Result()
	if err != nil {
		t.Fatalf("XLEN %s: %v", stream, err)
	}
	return n
}

// groupExists answers whether a consumer group is still declared on a stream.
func groupExists(t *testing.T, rdb *redis.Client, stream, group string) bool {
	t.Helper()
	groups, err := rdb.XInfoGroups(t.Context(), stream).Result()
	if err != nil {
		t.Fatalf("XINFO GROUPS %s: %v", stream, err)
	}
	for _, g := range groups {
		if g.Name == group {
			return true
		}
	}
	return false
}

// assertResetAuditEvidence holds the permanent record to the same numbers the
// response gave the operator.
//
// cache_keys_deleted is the one that can drift, and the reason this assertion
// exists: every Redis purge feeding it runs BEFORE the sweep's transaction
// precisely so the audit row written inside that transaction can name it. A
// purge moved after the commit would leave one key name meaning two totals,
// and only a comparison against the response would notice.
//
// objects_deleted is asserted ABSENT for the mirror-image reason: a blob store
// cannot join that transaction, so the bytes go after the row is committed and
// any number here would be a guess.
func assertResetAuditEvidence(t *testing.T, e *env, out resetResponse) {
	t.Helper()
	var raw []byte
	if err := e.owner.QueryRow(t.Context(),
		`SELECT evidence FROM audit_log WHERE action = 'reset_data'`).Scan(&raw); err != nil {
		t.Fatalf("reading the reset audit evidence: %v", err)
	}
	// Pointers, so a key the evidence never carried is distinguishable from one
	// carrying zero — the two mean very different things about the purge.
	var evidence struct {
		TablesCleared    *int  `json:"tables_cleared"`
		JobsCancelled    *int  `json:"jobs_cancelled"`
		StreamsPurged    *int  `json:"streams_purged"`
		CacheKeysDeleted *int  `json:"cache_keys_deleted"`
		ObjectsDeleted   *int  `json:"objects_deleted"`
		DrainTimedOut    *bool `json:"drain_timed_out"`
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatalf("decoding the reset audit evidence %q: %v", raw, err)
	}

	for _, c := range []struct {
		field string
		got   *int
		want  int
	}{
		{"tables_cleared", evidence.TablesCleared, out.TablesCleared},
		{"jobs_cancelled", evidence.JobsCancelled, out.JobsCancelled},
		{"streams_purged", evidence.StreamsPurged, out.StreamsPurged},
		{"cache_keys_deleted", evidence.CacheKeysDeleted, out.CacheKeysDeleted},
	} {
		if c.got == nil {
			t.Errorf("the reset audit evidence carries no %s; the permanent record names every count the response reported", c.field)
			continue
		}
		if *c.got != c.want {
			t.Errorf("audit %s = %d, response reported %d", c.field, *c.got, c.want)
		}
	}
	if evidence.DrainTimedOut == nil {
		t.Error("the reset audit evidence carries no drain_timed_out; an operator reading it back cannot tell a clean drain from a timed-out one")
	} else if *evidence.DrainTimedOut != out.DrainTimedOut {
		t.Errorf("audit drain_timed_out = %v, response reported %v", *evidence.DrainTimedOut, out.DrainTimedOut)
	}
	if evidence.ObjectsDeleted != nil {
		t.Errorf("audit evidence carries objects_deleted = %d, written before the bytes were purged", *evidence.ObjectsDeleted)
	}
}
