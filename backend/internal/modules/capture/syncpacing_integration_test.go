// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// How far out the scheduler actually paced the next sync, measured by the
// database against its own clock.
//
// Due/not-due cannot see this, and neither can the database-clock gate. Both
// answer "is the value derived from now()"; neither reads the UNIT the delay is
// handed over in. A ladder written as make_interval(mins => …) where the code
// means seconds is derived from now(), reads as "not due" exactly like a
// correct one, and would sit there as a 60x backoff nobody measured — which is
// the failure mode ADR-0063's ladder exists to avoid.
//
// The bounds below are deliberately wide. They are not a restatement of the
// ladder — that would only assert the constants back at themselves — but the
// order of magnitude the schedule has to land in, which is what a wrong unit
// destroys. Seconds-read-as-minutes overshoots by hours; seconds-read-as-
// milliseconds undershoots to nothing.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Each connector below registers under its OWN provider name: Register panics
// on a duplicate, so a test cannot swap the behaviour of one already wired. The
// names are drawn from the provider CHECK the connection table carries.
type refusingConnector struct{ fixtureConnector }

func (refusingConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{Name: "imap", Version: "fixture"}
}

func (refusingConnector) Sync(context.Context, connector.Auth, connector.Cursor, connector.Sink) (connector.Cursor, error) {
	return nil, connector.ErrUnreachable
}

// rateLimitedConnector refuses with the provider-supplied Retry-After the
// scheduler is meant to honour.
type rateLimitedConnector struct {
	fixtureConnector
	retryAfter time.Duration
}

func (rateLimitedConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{Name: "graph", Version: "fixture"}
}

func (c rateLimitedConnector) Sync(context.Context, connector.Auth, connector.Cursor, connector.Sink) (connector.Cursor, error) {
	return nil, &connector.RateLimitedError{RetryAfter: c.retryAfter}
}

// syncOnceOf connects provider through the real Connect path and drives one
// sync of it, returning whatever the sync reported. Going through Connect
// rather than inserting a connection row is what keeps the pacing assertion
// about production: the row it schedules against is the row production writes.
func syncOnceOf(ctx context.Context, t *testing.T, reg *capture.Registry, provider string) error {
	t.Helper()
	if _, err := reg.Connect(ctx, provider, connector.Auth("fixture-token")); err != nil {
		t.Fatalf("Connect(%s): %v", provider, err)
	}
	owner, _ := setupCaptureDB(t)
	var id ids.UUID
	if err := owner.QueryRow(ctx,
		`SELECT id FROM capture_connection WHERE provider = $1`, provider).Scan(&id); err != nil {
		t.Fatalf("reading back the connection Connect wrote: %v", err)
	}
	return reg.SyncOnce(ctx, id)
}

// pacedWithin reads how far next_sync_at sits from the DATABASE's now() and
// fails unless it lands inside [low, high]. Reading the remainder rather than
// the timestamp is what keeps the measurement on one clock: this process's own
// clock never enters it.
func pacedWithin(ctx context.Context, t *testing.T, provider string, low, high time.Duration) {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	var secs float64
	if err := owner.QueryRow(ctx, `
		SELECT EXTRACT(EPOCH FROM (s.next_sync_at - now()))
		  FROM capture_sync_state s
		  JOIN capture_connection c ON c.id = s.connection_id
		 WHERE c.provider = $1`, provider).Scan(&secs); err != nil {
		t.Fatalf("reading the paced next_sync_at for %s: %v", provider, err)
	}
	got := time.Duration(secs * float64(time.Second))
	if got < low || got > high {
		t.Fatalf("next sync paced %v out, want within [%v, %v] — a schedule outside that range is a unit, not a policy",
			got, low, high)
	}
}

// A refused sync paces the retry minutes out: far enough that the connection is
// not re-swept hot, near enough that a provider recovering within the hour is
// picked up without an operator.
func TestARefusedSyncPacesTheRetryOnTheLadder(t *testing.T) {
	ctx, reg, _, _ := newCaptureRegistryFixture(t)
	reg.Register(refusingConnector{})

	if err := syncOnceOf(ctx, t, reg, "imap"); !errors.Is(err, connector.ErrUnreachable) {
		t.Fatalf("SyncOnce = %v, want the connector's own failure", err)
	}
	pacedWithin(ctx, t, "imap", time.Minute, time.Hour)
}

// A healthy sync paces the next one at the interval the worker was configured
// with — the flag's value, honoured in the units the flag is written in.
func TestAHealthySyncPacesTheNextOneAtTheConfiguredInterval(t *testing.T) {
	ctx, reg, _, _ := newCaptureRegistryFixture(t)
	// Distinctive on purpose: the default is two minutes, so an interval that
	// never reached the write would still land inside any bound wide enough to
	// hold the default, and this test would pass on the wrong number.
	reg.WithSyncInterval(90 * time.Minute)

	if err := syncOnceOf(ctx, t, reg, "gmail"); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	pacedWithin(ctx, t, "gmail", 85*time.Minute, 90*time.Minute)
}

// A rate-limit honours the provider's Retry-After when it is longer than the
// ladder would have waited. That is the branch the app clock hurt worst: a
// process running behind the database under-honoured the very number the
// provider asked for, silently.
func TestARateLimitedSyncHonoursTheProvidersRetryAfter(t *testing.T) {
	ctx, reg, _, _ := newCaptureRegistryFixture(t)
	reg.Register(rateLimitedConnector{retryAfter: 3 * time.Hour})

	if err := syncOnceOf(ctx, t, reg, "graph"); !errors.Is(err, connector.ErrRateLimited) {
		t.Fatalf("SyncOnce = %v, want the connector's rate-limit", err)
	}
	// The ladder's early rungs are minutes, so a schedule near three hours can
	// only have come from the Retry-After.
	pacedWithin(ctx, t, "graph", 175*time.Minute, 3*time.Hour)
}
