// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The identity race two simultaneous first messages create. Both resolvers
// are released on one barrier and both attempt the bind; Postgres blocks the
// loser on the speculative-insert lock until the winner commits, so the
// DATABASE is the synchronizer — no sleep, no polling, no timing guess.

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// resolveOrCreatePersonForIdentity is the channel-aware ensure contract in
// miniature: resolve through the ladder, and when nothing resolves, create the
// person and its identity satellite SPECULATIVELY inside a savepoint. A
// resolver that loses the bind adopts the winner's person and rolls its own
// speculative row back, which is the only way one human ends up on one record
// — a created person cannot be un-created any other way.
func (e *dedupeEnv) resolveOrCreatePersonForIdentity(ctx context.Context, name string, ci connector.ChannelIdentity) (ids.PersonID, error) {
	var resolved ids.PersonID
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		r, err := DedupePerson(ctx, tx, PersonCandidate{
			FullName: name, ChannelIdentities: []connector.ChannelIdentity{ci},
		})
		if err != nil {
			return err
		}
		if r.Decision == DecisionExactCollision {
			resolved = r.PersonID
			return nil
		}

		speculative, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		candidate := ids.New[ids.PersonKind]()
		if _, err := speculative.Exec(ctx, `
			INSERT INTO person (id, workspace_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, $4, $5)`,
			candidate, workspaceID(ctx), name, ci.Provider, "connector:"+ci.Provider); err != nil {
			return err
		}
		owner, err := ResolveOrCreateChannelIdentity(ctx, speculative, candidate, ci)
		if err != nil {
			return err
		}
		resolved = owner
		if owner != candidate {
			return speculative.Rollback(ctx)
		}
		return speculative.Commit(ctx)
	})
	return resolved, err
}

// The adopt branch, proved without depending on goroutine scheduling: an
// identity already bound to someone is never re-pointed, and the offered
// person is told whose conversation it actually is.
func TestChannelIdentityBindAdoptsTheIncumbentOwner(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	incumbent := e.seedPerson(ctx, t, "Incumbent Owner", []string{"incumbent@adopt.test"}, nil)
	latecomer := e.seedPerson(ctx, t, "Latecomer", []string{"late@adopt.test"}, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770501"}
	e.bindIdentity(ctx, t, incumbent, ci)

	if adopted := e.bindIdentity(ctx, t, latecomer, ci); adopted != incumbent {
		t.Fatalf("bind returned %s, want the incumbent %s", adopted, incumbent)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1`, latecomer); n != 0 {
		t.Fatalf("the latecomer gained %d identity rows; a bound identity is never re-pointed by a bind", n)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE channel_user_id = $1 AND person_id = $2 AND archived_at IS NULL`,
		ci.ChannelUserID, incumbent); n != 1 {
		t.Fatalf("the incumbent holds %d live bindings for %s, want 1", n, ci.ChannelUserID)
	}
}

func TestTwoConcurrentResolutionsConvergeOnOnePerson(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	const name = "Concurrent First Message"
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "770401", Username: "racer"}

	barrier := make(chan struct{})
	resolved := make([]ids.PersonID, 2)
	failures := make([]error, 2)
	var running sync.WaitGroup
	running.Add(len(resolved))
	for i := range resolved {
		go func(slot int) {
			defer running.Done()
			<-barrier
			resolved[slot], failures[slot] = e.resolveOrCreatePersonForIdentity(ctx, name, ci)
		}(i)
	}
	close(barrier)
	running.Wait()

	for slot, err := range failures {
		if err != nil {
			t.Fatalf("resolver %d: %v", slot, err)
		}
	}
	if resolved[0] != resolved[1] {
		t.Fatalf("resolvers converged on %s and %s; two first messages from one Telegram user must land on one person",
			resolved[0], resolved[1])
	}

	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person WHERE full_name = $1 AND archived_at IS NULL`, name); n != 1 {
		t.Fatalf("%d person rows, want exactly 1 — the loser's speculative person must leave no trace", n)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE channel_user_id = $1 AND archived_at IS NULL`,
		ci.ChannelUserID); n != 1 {
		t.Fatalf("%d channel identity rows, want exactly 1", n)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1 AND archived_at IS NULL`,
		resolved[0]); n != 1 {
		t.Fatalf("the surviving person carries %d identity rows, want 1", n)
	}
}

// countInWorkspace runs one count under the workspace GUC, so RLS scopes it
// to this test's own tenant.
func (e *dedupeEnv) countInWorkspace(ctx context.Context, t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	return n
}
