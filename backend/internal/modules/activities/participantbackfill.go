// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Recovering the participants of activities captured before ACT-DDL-3 existed
// (ADR-0078).
//
// Capture stamps participants going forward, but every message already in the
// timeline predates the table. Without this, the whole who-knows-whom surface
// reads empty on a workspace with years of history until new mail happens to
// arrive — which is indistinguishable, to the person looking at it, from the
// feature not working.
//
// It runs as a resumable job rather than an UPDATE inside migration 0157. A
// migration holds a lock on the table for its whole duration, and a workspace
// with a real mailbox has hundreds of thousands of activity rows; a slow
// backfill inside the migration turns a deploy into an outage.
//
// Two classes are recovered here, and the second is the one that matters:
//
//	Class 1 — an activity a human logged. `captured_by` reads `human:<uuid>`,
//	so the user is stated on the row. Exact, no inference.
//
//	Class 2 — an activity a connector captured. `captured_by` reads
//	`connector:gmail` and names no human, but capture_connection is
//	per-user-per-provider, so the provider identifies the mailbox owner — as
//	long as the workspace has exactly ONE connection for that provider. With
//	two, the activity could belong to either mailbox and no evidence on the
//	row distinguishes them, so it stays unattributed rather than being
//	attributed to a coin flip. A wrong edge is worse than a missing one: it
//	tells someone to ask a colleague who has never met the contact.
//
// Deliberately NOT recovered here: parsing raw From/To/attendee headers out of
// the stored originals. That is the pass that would recover calendar history,
// and it is a different kind of work — it reads message bodies, needs its own
// address-matching rules, and its failure mode is silently mis-attributing a
// meeting. It gets its own slice.
//
// A note on connection rebinding, because the obvious worry does not apply:
// capture_connection upserts on (workspace, user, provider), so re-authorizing
// a DIFFERENT Google account re-points the same row and `account_bound_at`
// records when. That changes which MAILBOX the row holds — never which human.
// Since class 2 attributes an activity to a user and not to an address, an
// activity captured before a rebind still belongs to the same person, and the
// rebind is irrelevant to it.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// BackfillParticipantsBatch attributes up to limit activities that have no
// participant rows yet, and reports how many it wrote.
//
// It carries no cursor. The selection predicate is "an activity with no
// participant rows from which at least one participant is derivable", and
// every selected activity gains a row, so the remaining set strictly shrinks
// and the caller simply runs it until it returns zero. That makes the job
// resumable and crash-safe for free: there is no position to lose, and a batch
// that dies half-committed is re-selected on the next pass. It also makes the
// pass idempotent, which a cursor would not — a re-run writes nothing new.
//
// The zero-progress guarantee is load-bearing. If the predicate could select
// an activity that yields no rows, the caller's "run until zero" loop would
// never terminate, so the predicate and the insert below are written from the
// same two derivation arms and must stay that way.
func (s *Store) BackfillParticipantsBatch(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("activities: participant backfill needs a positive batch limit, got %d", limit)
	}
	// Gated like every other store entry point, on the grant that matches what
	// it does: it rewrites rows belonging to activities. There is no HTTP route
	// to it — the periodic job is the only caller, running as the system
	// principal — but an entry point that trusts its caller because "only the
	// job calls it today" is exactly the assumption the next caller breaks.
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		return 0, err
	}
	var written int
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		written, err = backfillParticipants(ctx, tx, limit)
		return err
	})
	return written, err
}

// backfillParticipants is the statement itself, split out so the integration
// tests can drive it inside their own transaction.
//
// One statement, not a read-then-write loop: the attribution is a join, and
// pulling rows into Go to insert them one at a time would only add a window in
// which capture writes the same participant concurrently.
func backfillParticipants(ctx context.Context, tx pgx.Tx, limit int) (int, error) {
	tag, err := tx.Exec(ctx, `
		WITH candidate AS (
		    SELECT a.id, a.direction, a.counterparty_email, o.user_id
		      FROM activity a
		      JOIN LATERAL (
		           -- Class 1 wins over class 2: a human-logged activity states
		           -- its user, and a connector guess must never override that.
		           SELECT u.id AS user_id
		             FROM app_user u
		            WHERE a.captured_by = 'human:' || u.id::text
		            UNION ALL
		           -- Class 2a: connector provenance that NAMES its mailbox owner
		           -- (connector:gmail:<user>). Exact, no inference — every
		           -- row captured since that provenance shipped.
		           SELECT u.id
		             FROM app_user u
		            WHERE a.captured_by LIKE 'connector:%:' || u.id::text
		            UNION ALL
		           -- Class 2b: older rows stamped with the connector alone,
		           -- from before the owner was recorded. Attributable only
		           -- when the workspace has exactly ONE connection for that
		           -- provider; with two, the row could belong to either
		           -- mailbox and nothing on it separates them, so it stays
		           -- unattributed rather than attributed to a coin flip.
		           SELECT c.user_id
		             FROM capture_connection c
		            WHERE a.captured_by = 'connector:' || c.provider
		              AND NOT EXISTS (
		                  SELECT 1 FROM capture_connection other
		                   WHERE other.provider = c.provider AND other.id <> c.id)
		            LIMIT 1
		      ) o ON true
		     WHERE a.archived_at IS NULL
		       		       -- The same set live stamping and hand-logging accept, rendered
		       -- from one definition so the three cannot drift apart.
		       AND a.kind IN (`+relstrength.InteractionKindSQLList()+`)
		       AND NOT EXISTS (
		           SELECT 1 FROM activity_participant p WHERE p.activity_id = a.id)
		     ORDER BY a.id
		     LIMIT $1
		)
		INSERT INTO activity_participant (workspace_id, activity_id, user_id, person_id, address, role)
		SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		       c.id, r.user_id, r.person_id, r.address, r.role
		  FROM candidate c
		  CROSS JOIN LATERAL (
		       -- Our side: the role follows the direction, exactly as capture
		       -- stamps it live, so a backfilled row and a captured one are
		       -- indistinguishable to the derivation that reads them.
		       SELECT c.user_id AS user_id, NULL::uuid AS person_id, NULL::text AS address,
		              CASE WHEN c.direction = 'inbound' THEN 'to' ELSE 'from' END AS role
		        UNION ALL
		       -- Their side: the person if the timeline already links one,
		       -- else the address the message carried. A link is stronger
		       -- evidence than the header, and it is what capture's own
		       -- promotion would have produced.
		       SELECT NULL::uuid,
		              (SELECT l.person_id FROM activity_link l
		                WHERE l.activity_id = c.id AND l.entity_type = 'person'
		                ORDER BY l.created_at LIMIT 1),
		              lower(nullif(trim(c.counterparty_email), '')),
		              CASE WHEN c.direction = 'inbound' THEN 'from' ELSE 'to' END
  ) r
		 WHERE r.user_id IS NOT NULL OR r.person_id IS NOT NULL OR r.address IS NOT NULL
		ON CONFLICT DO NOTHING`, limit)
	if err != nil {
		return 0, fmt.Errorf("activities: backfilling interaction participants: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
