// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The flip's mutual exclusion: one cutover at a time per workspace, and
// the liveness signal Disconnect reads off the same lock.
//
// Why a lock rather than a status column: the import pages the frozen
// mirror with a positional cursor, so any concurrent request that
// unseals mid-run would let the estate drift under it and silently drop
// a row per insert. And liveness has to die with the session — a run
// record left at `running` by a cancelled request would otherwise block
// the only path that revokes the incumbent credential, forever.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// flipOperator is the human running the cutover — the owner an
// unmapped-owner estate record is imported under (see flipWriters.operator).
func flipOperator(ctx context.Context) (*ids.UserID, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return nil, errors.New("compose: flip execute called with no actor bound")
	}
	if actor.UserID == (ids.UUID{}) {
		return nil, errors.New("compose: the flip needs a human operator to inherit unmapped-owner records")
	}
	operator := ids.From[ids.UserKind](actor.UserID)
	return &operator, nil
}

// claimFlip takes a workspace-scoped advisory lock for the duration of
// one execute. A second concurrent flip is refused outright rather than
// queued: it would be running against the same sealed snapshot, so
// waiting for the first to finish only to import an already-imported
// estate helps nobody. The returned release is safe to call once.
func (f *flipRunner) claimFlip(ctx context.Context) (func(), error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, errors.New("compose: the flip claim was taken outside a workspace context")
	}
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("flip execute: acquiring the claim connection: %w", err)
	}
	// A 64-bit key derived from the workspace id, XORed with the flip's
	// own namespace constant: the same workspace always maps to the same
	// lock, distinct workspaces effectively never collide, and the
	// namespace keeps the flip clear of any other advisory-lock user.
	// The single-argument bigint form: the two-argument one takes int4s,
	// which a workspace-derived key overflows.
	key := flipLockKey(ws)
	var claimed bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&claimed); err != nil {
		conn.Release()
		return nil, fmt.Errorf("flip execute: claiming the flip: %w", err)
	}
	if !claimed {
		conn.Release()
		return nil, fmt.Errorf("another flip is already running for this workspace: %w", apperrors.ErrConflict)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, key); err != nil {
				// A session-level lock lives exactly as long as its
				// session, and Release hands this connection back to the
				// pool with that session intact — so a failed unlock would
				// leave the claim held by an idle pooled connection and
				// wedge every later flip in this workspace. Destroy the
				// session instead; the lock cannot outlive it. Logged, not
				// returned, so it never masks the flip's own outcome.
				f.log.Warn("overlay flip: releasing the flip claim failed; closing the connection so the claim cannot outlive it", "err", err)
				if hijacked := conn.Hijack(); hijacked != nil {
					if cerr := hijacked.Close(context.WithoutCancel(ctx)); cerr != nil {
						f.log.Warn("overlay flip: closing the hijacked flip-claim connection failed", "err", cerr)
					}
				}
				return
			}
			conn.Release()
		})
	}, nil
}

// flipAdvisoryLockNamespace keeps the flip's advisory-lock keys clear of
// any other user of the shared 64-bit advisory-lock space.
const flipAdvisoryLockNamespace int64 = 0x464C4950 // "FLIP"

// flipLockKey is the workspace's advisory-lock key — the ONE spelling,
// shared by the claim and by the liveness probe, so the two can never
// key on different values.
func flipLockKey(ws ids.UUID) int64 {
	// Masked to 63 bits before the signed conversion: the key only has
	// to be stable and collision-resistant per workspace, and wrapping
	// into the negative range would be an overflow the linter is right
	// to refuse.
	return int64(binary.BigEndian.Uint64(ws[:8])&math.MaxInt64) ^ flipAdvisoryLockNamespace
}

// FlipImportProbe is the Disconnect predicate: is a flip IMPORT in
// flight for this workspace right now?
//
// Two conditions, and both are needed. The advisory lock alone would
// also be held by a preflight — whose parity dry-run runs for minutes
// on a large estate — and refusing the credential-revoke path for a
// readiness check is the latch this probe exists to avoid. A `running`
// run row alone is not enough either: a cancelled request leaves one
// behind forever (its failure write rides the same dead context). Held
// lock AND a running run is true only while an import is really moving.
func FlipImportProbe(ctx context.Context, tx pgx.Tx) (bool, error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return false, errors.New("compose: flip liveness probed outside a workspace context")
	}
	held, err := migration.FlipImportLiveness(ctx, tx, flipLockKey(ws))
	if err != nil || !held {
		return false, err
	}
	return migration.MirrorRunInFlight(ctx, tx)
}
