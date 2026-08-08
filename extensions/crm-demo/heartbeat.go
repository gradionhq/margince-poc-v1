// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmdemo

// The scheduled tick: the only thing on the demo screen that happens without a
// user. api/jobs.yaml declares the cadence, the two wall clocks, the queue and
// the attempt cap; this is the whole of the behavior.

import (
	"context"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// heartbeat writes one row naming the workspace this tick is for, and prunes
// its own history to the newest keptHeartbeats.
//
// Naming the workspace is the point of the row, not decoration. A scheduled
// extension job is a FAN-OUT: the cadenced dispatcher enqueues one workspace
// child per live tenant, and on a single-workspace dev install that is one
// child. A row naming no tenant would demonstrate the single-tenant case and
// leave the multi-tenant guarantee untested, so the tenant the runner pinned is
// written into the row a human reads.
//
// THERE IS NO TICK NUMBER, and its absence is a correction. The row used to
// carry `tick #N` counted as `count(*) + 1` over surviving rows — which, with
// the prune holding the population at keptHeartbeats, meant the counter climbed
// to 11 and then every subsequent tick wrote the identical string. The comment
// here described "renumbering from the oldest kept tick"; nothing renumbered,
// it simply saturated, and consecutive rows read the same. Raising the kept
// count only moves the ceiling.
//
// created_at carries the sequence instead. The screen already renders it in
// front of every row, so an ordering the database maintains is displayed rather
// than a counter this unit would have to maintain — and a monotonic counter
// would need a sequence the unit has no reason to own.
//
// THE PRUNE IS NOT HOUSEKEEPING. At a 60s cadence this writes 1,440 rows per
// workspace per day, forever, into the same table the screen reads with
// LIMIT 200 — so after about 3.3 hours of uptime every note a human typed is
// below the read window, and "add a note, restart the stack, it is still
// there" stops being observable. The demo would crowd itself off its own
// screen, and the acceptance step that proves the migrations layer works would
// fail for a reason that has nothing to do with migrations.
//
// Pruning was chosen over filtering the ticks out of the notes read, because
// the tick is meant to be SEEN in the list — it is the one row that appears
// with no user action, and moving it to a separate strip would make the jobs
// surface something a viewer has to be told about rather than something they
// watch happen. A bounded history keeps both properties, and it also bounds
// the table, which a filtered read would not.
//
// An error fails the attempt, which the dispatcher's next tick retries. There
// is no result — nobody is waiting for one.
func heartbeat(ctx context.Context, rt extension.Runtime) error {
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		// kind, not a body prefix. What makes a row the tick's is a column the
		// tick sets and a human's note cannot carry — the previous version
		// matched on the body's leading glyph, which meant a note a person
		// typed starting with the same characters was counted as a tick and
		// then DELETED by the prune below. Neither statement carries a tenant
		// predicate; the policy is what makes both of them this workspace's.
		if _, err := tx.Exec(ctx,
			`INSERT INTO `+noteTable+` (workspace_id, kind, body)
			 VALUES (`+callerWorkspace+`, $1, $2 || `+callerWorkspace+`::text)`,
			kindHeartbeat, heartbeatPrefix); err != nil {
			return err
		}
		// Same transaction as the insert, so a tick either writes and prunes or
		// does neither.
		_, err := tx.Exec(ctx,
			`DELETE FROM `+noteTable+`
			  WHERE kind = $1
			    AND id NOT IN (
			      SELECT id FROM `+noteTable+`
			       WHERE kind = $1
			       ORDER BY created_at DESC, id DESC
			       LIMIT $2)`, kindHeartbeat, keptHeartbeats)
		return err
	})
}

// kindNote and kindHeartbeat are the two values the table's `kind` column
// admits (0002_note_kind.up.sql pins the pair in a CHECK constraint, so a third
// value is refused by the database rather than by convention).
//
// kindNote is the column's DEFAULT, so nothing has to write it; it is named
// here because the notes read filters on nothing and a reader needs to know
// what the other value is.
const (
	kindNote      = "note"
	kindHeartbeat = "heartbeat"
)

// heartbeatPrefix is the display text a tick's row carries, and it is display
// text only — no query matches on it any more. The workspace id is appended by
// the insert above.
const heartbeatPrefix = "⟳ heartbeat — workspace "

// keptHeartbeats bounds the tick history. Ten is enough to see a sequence
// arrive on screen and small enough that the notes read (LIMIT 200) stays
// almost entirely notes.
const keptHeartbeats = 10
