// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb_test

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

// How many logical databases the lane may use is stated in three places that
// cannot check each other: this package's RedisDBs, REDIS_DBS in the parallel
// runner, and --databases in the compose file. Drift between them is silent in
// the direction that matters — the runner assigns a db the server does not
// serve, and the suite that drew it fails with `ERR DB index is out of range`,
// naming Redis rather than the mismatch that caused it.
//
// Paths are relative to this package, which is three levels under backend/.
const (
	laneRunnerPath = "../../../../scripts/test-integration-parallel.sh"
	composePath    = "../../../../infra/docker-compose.dev.yml"
)

// The declarations this reads. Anchored to the assignment rather than any
// mention, so a comment naming the number cannot satisfy the gate.
var (
	laneRunnerDecl = regexp.MustCompile(`(?m)^REDIS_DBS=(\d+)$`)
	composeDecl    = regexp.MustCompile(`--databases",\s*"(\d+)"`)
)

func TestRedisDBCountAgreesWithTheLaneAndTheServer(t *testing.T) {
	t.Run("the parallel runner assigns from the same range", func(t *testing.T) {
		if got := declaredInt(t, laneRunnerPath, laneRunnerDecl); got != testdb.RedisDBs {
			t.Errorf("REDIS_DBS=%d in %s but testdb.RedisDBs=%d — the runner would assign a db this package rejects, or reject one it would accept",
				got, laneRunnerPath, testdb.RedisDBs)
		}
	})

	// The server serves indices 0..databases-1, and db 0 is reserved for a
	// running `make dev`, so the lane's usable count is one less than declared.
	t.Run("the compose file provisions one more than the lane uses", func(t *testing.T) {
		if got := declaredInt(t, composePath, composeDecl); got != testdb.RedisDBs+1 {
			t.Errorf("--databases %d in %s but testdb.RedisDBs=%d — Redis must serve %d so that db 1..%d exist alongside the reserved db 0",
				got, composePath, testdb.RedisDBs, testdb.RedisDBs+1, testdb.RedisDBs)
		}
	})

	// The file is what the container was asked for; this is what it actually
	// serves. They differ whenever a container outlives a change to the file,
	// which is the case no amount of agreement between the two files can catch.
	t.Run("the running server serves that many", func(t *testing.T) {
		addr := os.Getenv("MARGINCE_TEST_REDIS")
		if addr == "" {
			t.Fatal("MARGINCE_TEST_REDIS not set — run `make db-up` (integration tests fail loudly, they never skip)")
		}
		// Db 0, which every server has, rather than the db the lane assigned:
		// go-redis SELECTs on the first command, so asking an under-provisioned
		// server about a db it does not serve fails the SELECT and reports a
		// Redis read error — naming Redis instead of the drift this test exists
		// to name. Db 0 is the one a running `make dev` owns and no test may
		// flush; CONFIG GET reads a server setting and touches no key in it.
		rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 0})
		t.Cleanup(func() {
			if err := rdb.Close(); err != nil {
				t.Errorf("closing redis: %v", err)
			}
		})
		ctx := context.Background()
		res, err := rdb.ConfigGet(ctx, "databases").Result()
		if err != nil {
			t.Fatalf("reading the server's database count: %v", err)
		}
		raw, ok := res["databases"]
		if !ok {
			t.Fatalf("the server reported no `databases` setting (got %v)", res)
		}
		served, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("the server reported databases=%q, which is not a number", raw)
		}
		if served != testdb.RedisDBs+1 {
			t.Errorf("the server at %s serves %d databases but the lane assigns up to db %d — recreate the container (`make db-down && make db-up`); a container started before the count was raised keeps the old one",
				addr, served, testdb.RedisDBs)
		}
	})
}

// declaredInt reads the single number want matches in the file at path, failing
// when the file does not exist or the declaration has moved — a gate that
// silently matches nothing proves nothing.
func declaredInt(t *testing.T, path string, want *regexp.Regexp) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	m := want.FindSubmatch(body)
	if m == nil {
		t.Fatalf("%s no longer declares %s — this gate reads that declaration, so it must be updated with the file", path, want)
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("%s declares %q, which is not a number", path, m[1])
	}
	return n
}
