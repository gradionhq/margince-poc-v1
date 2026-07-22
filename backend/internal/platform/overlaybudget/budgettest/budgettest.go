// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package budgettest is the shared Redis-backed OVB meter fixture for the
// integration lanes. It lives in the platform tier (the only tier the arch
// rules let import a raw Redis client), so the overlay module and the
// compose suites get a real meter WITHOUT themselves importing go-redis —
// they depend on this helper instead, keeping the raw-Redis dependency out
// of the module and composition tiers.
package budgettest

import (
	"os"
	"strconv"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

// Client returns a flushed Redis client on the isolated integration db
// (MARGINCE_TEST_REDIS_DB, default 15; the parallel runner assigns each
// package its own). It fails loudly (never skips) when Redis is not
// provisioned — the same posture the DB fixtures take.
func Client(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("MARGINCE_TEST_REDIS")
	if addr == "" {
		t.Fatal("MARGINCE_TEST_REDIS not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	db := 15
	if raw := os.Getenv("MARGINCE_TEST_REDIS_DB"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 15 {
			t.Fatalf("MARGINCE_TEST_REDIS_DB=%q is not a Redis db index in 1..15", raw)
		}
		db = n
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: db})
	if err := rdb.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("redis at %s unreachable — run `make db-up`: %v", addr, err)
	}
	if err := rdb.FlushDB(t.Context()).Err(); err != nil {
		t.Fatalf("flushing test redis db: %v", err)
	}
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Errorf("closing redis: %v", err)
		}
	})
	return rdb
}

// Meter builds a flushed-Redis-backed OVB meter over cfg — the caller
// supplies its own per-incumbent windows/thresholds so each suite tunes
// the budget to what it asserts.
func Meter(t *testing.T, cfg overlaybudget.Config) *overlaybudget.Meter {
	t.Helper()
	return overlaybudget.New(Client(t), cfg)
}

// SmallConfig is a fast-to-exhaust budget for the named incumbents: REST
// cap 10 (warn at 5, shed at 8) and a generous search cap so a small sweep
// never paces to a stop before it means to. It matches the thresholds the
// overlay/compose budget proofs assert ("1 spend under threshold, 8 sheds").
func SmallConfig(incumbents ...string) overlaybudget.Config {
	cfg := overlaybudget.Config{}
	for _, name := range incumbents {
		cfg[name] = overlaybudget.IncumbentConfig{
			Search:       overlaybudget.WindowConfig{Ceiling: 1000, Cap: 500},
			REST:         overlaybudget.WindowConfig{Ceiling: 100, Cap: 10},
			WarnFraction: 0.5,
			ShedFraction: 0.8,
		}
	}
	return cfg
}
