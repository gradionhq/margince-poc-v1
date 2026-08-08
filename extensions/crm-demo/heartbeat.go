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
// child. A row that said only "tick #7" would demonstrate the single-tenant
// case and leave the multi-tenant guarantee untested, so the tenant the runner
// pinned is written into the row a human reads.
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
		// ONE statement for the write, so the tick number and the row it
		// labels cannot disagree: a count in one statement and an insert in
		// another would race two concurrent ticks of the same workspace onto
		// one number. Neither statement carries a tenant predicate — the
		// policy is what makes both of them this workspace's.
		if _, err := tx.Exec(ctx,
			`INSERT INTO `+noteTable+` (workspace_id, body)
			 SELECT `+callerWorkspace+`,
			        '`+heartbeatPrefix+`' || (count(*) + 1)::text ||
			        ' (workspace ' || `+callerWorkspace+`::text || ')'
			   FROM `+noteTable+`
			  WHERE body LIKE $1`, heartbeatLike); err != nil {
			return err
		}
		// The counter keeps climbing while the history stays bounded: the
		// count above is over surviving rows, so a pruned run renumbers from
		// the oldest kept tick rather than continuing. That is honest for a
		// demo — the number says "this many ticks are on screen", not "this
		// many have ever run" — and a monotonic counter would need a sequence
		// this unit has no reason to own.
		//
		// Same transaction as the insert, so a tick either writes and prunes
		// or does neither.
		_, err := tx.Exec(ctx,
			`DELETE FROM `+noteTable+`
			  WHERE body LIKE $1
			    AND id NOT IN (
			      SELECT id FROM `+noteTable+`
			       WHERE body LIKE $1
			       ORDER BY created_at DESC, id DESC
			       LIMIT $2)`, heartbeatLike, keptHeartbeats)
		return err
	})
}

// heartbeatPrefix is what a tick's row starts with, and therefore what counts
// as a previous tick — both for the numbering and for what the prune is
// allowed to delete. A note a human typed must never match it.
const heartbeatPrefix = "⟳ heartbeat — tick #"

// heartbeatLike is the prefix as a LIKE pattern. The prefix must hold no %, _
// or backslash or this would match more than it names — including, at worst,
// notes the prune then deletes. heartbeat_test.go pins that it holds none.
const heartbeatLike = heartbeatPrefix + "%"

// keptHeartbeats bounds the tick history. Ten is enough to see a sequence
// arrive on screen and small enough that the notes read (LIMIT 200) stays
// almost entirely notes.
const keptHeartbeats = 10
