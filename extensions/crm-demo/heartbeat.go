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

// heartbeat writes one row naming the workspace this tick is for.
//
// Naming the workspace is the point, not decoration. A scheduled extension job
// is a FAN-OUT: the cadenced dispatcher enqueues one workspace child per live
// tenant, and on a single-workspace dev install that is one child. A row that
// said only "tick #7" would demonstrate the single-tenant case and leave the
// multi-tenant guarantee untested, so the tenant the runner pinned is written
// into the row a human reads.
//
// It writes one row and returns. There is no result — nobody is waiting for one
// — and an error fails the attempt, which the dispatcher's next tick retries.
func heartbeat(ctx context.Context, rt extension.Runtime) error {
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		// ONE statement, so the tick number and the row it labels cannot
		// disagree: a count in one statement and an insert in another would
		// race two concurrent ticks of the same workspace onto one number. The
		// count is over this workspace's rows because the policy makes it so —
		// there is no tenant predicate to write.
		_, err := tx.Exec(ctx,
			`INSERT INTO `+noteTable+` (workspace_id, body)
			 SELECT `+callerWorkspace+`,
			        '⟳ heartbeat — tick #' || (count(*) + 1)::text ||
			        ' (workspace ' || `+callerWorkspace+`::text || ')'
			   FROM `+noteTable+`
			  WHERE body LIKE $1`, heartbeatPrefix+"%")
		return err
	})
}

// heartbeatPrefix is what a tick's row starts with, and therefore what counts
// as a previous tick. It is a LIKE pattern prefix, so it must hold no % or _;
// the glyph and the words below hold neither.
const heartbeatPrefix = "⟳ heartbeat — tick #"
