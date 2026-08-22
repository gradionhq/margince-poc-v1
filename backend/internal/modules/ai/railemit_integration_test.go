// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package ai

// The occurrence lock's effect on the transaction it runs in.
//
// It lives here rather than in compose/integration because lockOccurrence is
// unexported and the property is about the SETTING it leaves behind, not about
// anything the projection can see — a test one package away would have to export
// a hook to reach it, and the hook would be the thing under test.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The lock puts back the lock_timeout it FOUND, not the server's default.
//
// `SET LOCAL lock_timeout = DEFAULT` means the configured default, so a caller
// that had chosen its own bound — a pool carrying it on its DSN makes it the
// session value — would silently lose it for the rest of the transaction,
// including the ledger and outbox writes that follow. Bounding the wait for one
// advisory lock is this code's business; deciding that somebody else's bound no
// longer applies is not.
func TestTheOccurrenceLockRestoresTheTimeoutItFound(t *testing.T) {
	env := setupRateStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		prior string
		want  string
	}{
		// A caller's own bound survives the announcement.
		{"a caller that chose its own bound", "7s", "7s"},
		// And with nothing chosen, the transaction goes back to the server's
		// value rather than keeping the one this code imposed for the lock.
		{"a caller that chose none", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before, after string
			err := pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
				if tc.prior != "" {
					if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, tc.prior); err != nil {
						return err
					}
				}
				if err := tx.QueryRow(ctx, `SELECT current_setting('lock_timeout')`).Scan(&before); err != nil {
					return err
				}
				if err := lockOccurrence(ctx, tx, "restores-what-it-found:"+tc.name); err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT current_setting('lock_timeout')`).Scan(&after)
			})
			if err != nil {
				t.Fatalf("locking the occurrence: %v", err)
			}
			if after != before {
				t.Errorf("lock_timeout went from %q to %q across the lock — the announcement's own bound now applies to every write that follows it", before, after)
			}
			if tc.want != "" && after != tc.want {
				t.Errorf("lock_timeout = %q, want the caller's own %q", after, tc.want)
			}
			// The bound must really have been imposed WHILE the lock was taken,
			// or this test would pass against a lockOccurrence that never set it
			// and therefore never had to restore it.
			if before == railLockTimeout {
				t.Fatalf("the prior timeout is already %q, so this case cannot tell a restore from a no-op", railLockTimeout)
			}
		})
	}
}
