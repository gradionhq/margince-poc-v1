// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"os"
	"strconv"
	"testing"
)

// RedisDBs is the highest logical db the lane may assign, which is also how many
// it may use, because db 0 is reserved for a running `make dev`.
//
// Two other places carry it and they are NOT all the same number. REDIS_DBS in
// scripts/test-integration-parallel.sh is this same highest index; --databases in
// infra/docker-compose.dev.yml is a TOTAL and so is one greater, since Redis
// counts the reserved db 0. Raising the three to one value would take the lane's
// top db away without saying so, which is why
// TestRedisDBCountAgreesWithTheLaneAndTheServer asserts the offset rather than
// equality — and asks the running server too, since a container started before a
// raise keeps serving the old count whatever the file says.
const RedisDBs = 63

// defaultRedisDB is what a bare `go test` gets, outside the lane. Any db but 0
// would do — 0 is the one a running `make dev` owns.
const defaultRedisDB = 15

// RedisDB is the ONE spelling of which Redis logical database an integration
// test may touch. Every fixture that talks to Redis flushes its db between
// tests, so this is an isolation boundary, not a preference: db 0 is excluded
// because a running `make dev` owns it, and a value the lane never assigns is
// refused rather than quietly redirected — a fixture that flushed the wrong db
// would take out a package that has no idea it exists.
func RedisDB(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("MARGINCE_TEST_REDIS_DB")
	if raw == "" {
		return defaultRedisDB
	}
	db, err := strconv.Atoi(raw)
	if err != nil || db < 1 || db > RedisDBs {
		t.Fatalf("MARGINCE_TEST_REDIS_DB=%q is not a Redis db index in 1..%d — the parallel runner assigns one per package, so a value outside that range means the runner and this fixture disagree about how many there are",
			raw, RedisDBs)
	}
	return db
}
