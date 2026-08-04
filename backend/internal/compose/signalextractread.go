// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Which conversations the extractor asks about, and where it got to.
//
// The queue is derived, not maintained: a thread is due when something has
// arrived on it since the watermark says it was last read. There is no work
// table to drift, and a thread nobody has written on costs nothing forever.
//
// A thread is asked about only once it has SETTLED. Reading a conversation
// mid-exchange produces events that the next message contradicts — "they are
// ending the contract" written while the sentence it came from was still being
// negotiated. Six hours is the pin (SIG-PARAM-7); it is the same posture the
// capture passes take toward mail that is still arriving.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const (
	// extractSettleHours is how long a thread must be quiet before it is read.
	extractSettleHours = 6
	// extractThreadMessages is how far back one call looks. A material event
	// is stated in the exchange it belongs to, not six months earlier, and a
	// longer window buys older text at the price of the recent text's weight.
	extractThreadMessages = 6
	// extractBodyLimit truncates each body for the prompt, as capture-classify
	// truncates its own (AIRT-PARAM-35).
	extractBodyLimit = 1500
	// extractThreadCap bounds one pass, so a workspace that has just connected
	// a mailbox does not spend its whole model budget on the first tick.
	extractThreadCap = 200
	// extractRefusalCap is how many times ONE conversation state may be refused
	// before it is parked. It exists because the cap above is a scarce resource:
	// a thread that stays due forever holds a slot in every pass, and enough of
	// them read nothing but themselves while the backlog behind them starves.
	// Three, because the model lane already retries and escalates internally —
	// this counts whole readings, not attempts, so three is three escalated
	// disagreements about the same text.
	extractRefusalCap = 3
	// extractParkFor is how long a conversation stays parked before it is
	// offered again.
	//
	// Parking has to expire. What refuses a reading is the pair of a text and a
	// model, and only one of those is fixed: a routing change, a new model
	// version, a corrected prompt or a raised cap can all make a conversation
	// readable that was not readable last week. Parking it until new mail
	// happens to arrive would drop, permanently and silently, the material
	// events in every thread that never receives another message — which is
	// most finished conversations, and a finished conversation is exactly where
	// "the contract ended" is stated.
	//
	// A week: long enough that a poisoned thread costs one reading a week
	// instead of one an hour, short enough that a fix reaches the backlog
	// without anybody replaying anything by hand.
	extractParkFor = 7 * 24 * time.Hour
)

// threadMessage is one message of a conversation as the prompt sees it.
type threadMessage struct {
	ID        ids.UUID
	Direction string
	Subject   string
	Body      string
	At        time.Time
}

// settledThread is one conversation due for a read, with the account it
// belongs to already resolved — an event about nobody is not an event this
// product can file.
type settledThread struct {
	Key            string
	OrganizationID ids.UUID
	// Newest is the instant the watermark advances to, read at the same time
	// as the messages so a message arriving mid-pass is not skipped: it is
	// newer than what this pass records, so the next pass picks the thread up.
	Newest time.Time
	// Count is how many messages the conversation held when this pass read it.
	// The timestamp alone cannot see a message inserted at the same instant, or
	// a backfill filling in older ones; the count changes for both.
	Count int
	// PrivateTo is the one reader this conversation answers to, or the zero
	// value when every message on it is workspace-readable.
	//
	// What the model writes about a conversation is only as shareable as the
	// conversation. Capture auto-creates contacts owner-private, and a message
	// filed against nobody else is readable by its capturing user alone — so a
	// summary of it, filed on an account the whole workspace can see, would
	// disclose exactly what the private contact protects.
	PrivateTo ids.UUID
	Messages  []threadMessage
}

// dueThreadsQuery is the queue itself. It is a package-level constant rather
// than a literal inside dueThreads because the query IS the rule — settled,
// resolvable to one account, moved since it was last read, not parked — and a
// hundred lines of it wrapped around twenty lines of scanning made the Go read
// like a footnote to the SQL.
//
// $1 settled instant, $2 per-pass cap, $3 refusal cap, $4 park cutoff.
var dueThreadsQuery = `
		WITH conversation AS (
			SELECT a.thread_key,
			       max(a.occurred_at) AS newest,
			       count(DISTINCT a.id) AS message_count,
			       min(ro.organization_id::text) AS one_org,
			       count(DISTINCT ro.organization_id) AS org_count,
			       -- Shared only when EVERY message is: the model is shown the
			       -- whole conversation, so what it writes is as private as the
			       -- most private thing it read.
			       --
			       -- A message with no links at all counts as shared, which is
			       -- the link-less note rule auth.ActivityScopeClause already
			       -- applies — its empty link set reads as visible. Calling it
			       -- private here would disagree with the gate the reader
			       -- actually faces, and withhold a finding about mail anyone
			       -- may open.
			       bool_and(coalesce(vis.shared, true)) AS shared,
			       min(vis.private_owner::text) AS private_owner
			  FROM activity a
			  LEFT JOIN (` + activities.OrgReachSet() + `) ro ON ro.activity_id = a.id
			  -- Who may read this message, asked of the records it is filed
			  -- against. A message is readable when ANY of its links is
			  -- (auth.ActivityScopeClause), so one workspace-visible link
			  -- shares it; only an activity whose every link is capture-private
			  -- belongs to one person, and then that person is its owner.
			  LEFT JOIN LATERAL (
			    SELECT bool_or(coalesce(vp.visibility, vo.visibility, 'workspace') <> 'owner') AS shared,
			           min(coalesce(vp.owner_id, vo.owner_id)::text)
			             FILTER (WHERE coalesce(vp.visibility, vo.visibility) = 'owner')
			             AS private_owner
			      FROM activity_link vl
			      LEFT JOIN person vp ON vp.id = vl.person_id
			      LEFT JOIN organization vo ON vo.id = vl.organization_id
			     WHERE vl.activity_id = a.id
			  ) vis ON true
			 WHERE a.thread_key IS NOT NULL AND a.kind = 'email'
			   AND a.archived_at IS NULL AND a.captured_by LIKE 'connector:%'
			 GROUP BY a.thread_key
		)
		SELECT c.thread_key, c.one_org::uuid, c.newest, c.message_count,
		       CASE WHEN c.shared THEN NULL ELSE c.private_owner::uuid END
		  FROM conversation c
		  LEFT JOIN signal_thread_scan s ON s.thread_key = c.thread_key
		 WHERE c.org_count = 1
		   -- A conversation nobody else may read, whose reader cannot be named,
		   -- is not offered at all. Reading it would produce a finding with no
		   -- owner to answer to, and a signal that names no owner is a shared
		   -- one — so the unattributable case would resolve, silently, to the
		   -- widest possible audience. Refusing it is the only answer that
		   -- fails the safe way.
		   AND (c.shared OR c.private_owner IS NOT NULL)
		   AND c.newest <= $1
		   -- Parked: this exact conversation state has been refused as often as
		   -- it may be, recently. Two things release it. A message added to the
		   -- thread changes newest or the count, so the pin stops matching and
		   -- the text is no longer the text that was refused. And the park
		   -- itself expires, because what refused the reading was a text AND a
		   -- model, and the model changes.
		   AND NOT (coalesce(s.refusals, 0) >= $3
		            AND s.refused_activity_at IS NOT DISTINCT FROM c.newest
		            AND s.refused_message_count IS NOT DISTINCT FROM c.message_count
		            AND s.scanned_at > $4)
		   -- Due when the conversation has MOVED in any way it can. The
		   -- timestamp misses a message inserted at the same instant and a
		   -- backfill that adds older ones; the count sees both. And the
		   -- ACCOUNT moves without the conversation moving at all — two of the
		   -- three arms are live relationships, so a contact changing employer
		   -- re-points every quiet thread they are on. Read for one account is
		   -- not read for another.
		   AND (s.thread_key IS NULL
		        OR s.last_activity_at < c.newest
		        OR s.message_count <> c.message_count
		        OR s.resolved_org_id IS DISTINCT FROM c.one_org::uuid)
		 ORDER BY c.newest DESC
		 LIMIT $2`

// dueThreads lists the conversations that have settled and have moved since
// they were last read, newest first.
//
// A conversation's account comes from the three-arm walk (the message's own
// link, its deal's account, the employer of the contact it is about) rather
// than a direct organization link. Capture files mail against the PERSON it was
// with, so a direct match resolves nothing on real correspondence — an account
// is reached through its people, or not at all.
//
// The walk is joined rather than applied as a predicate because the question
// here is which account a thread belongs to, not whether it belongs to a known
// one — and it is a LEFT join because the two things the aggregate computes are
// different questions over different rows:
//
//   - WHEN the conversation last moved, and how many messages it holds, is
//     asked of EVERY message on the thread. Counting only the resolvable ones
//     would let a thread look settled while a message nobody can place arrived
//     minutes ago, and the settle window exists precisely to keep the model out
//     of a conversation still in progress.
//   - WHICH account it belongs to is asked only of the messages that reach one;
//     count(DISTINCT) ignores the NULLs a LEFT join leaves behind.
//
// The org resolution is deliberately strict: exactly one organization across
// the whole thread. A conversation touching two accounts would have its events
// filed against whichever the join happened to pick, and a signal on the wrong
// account is worse than no signal — it is a claim the reader cannot trace back.
// A contact with two live employers makes their threads ambiguous by the same
// rule and skips them; filtering the walk down to a primary employer would buy
// those threads back by guessing, which is the thing being refused.
//
// A message whose contact reaches no account at all still reaches the PROMPT —
// threadMessages reads the conversation by thread_key alone. Resolution decides
// whose conversation this is, not which of its messages the model may read.
func dueThreads(ctx context.Context, tx pgx.Tx, now time.Time, limit int) ([]settledThread, error) {
	settled := now.Add(-extractSettleHours * time.Hour)
	rows, err := tx.Query(ctx, dueThreadsQuery,
		settled, limit, extractRefusalCap, now.Add(-extractParkFor))
	if err != nil {
		return nil, fmt.Errorf("list the threads due for a read: %w", err)
	}
	defer rows.Close()
	var due []settledThread
	for rows.Next() {
		var thread settledThread
		var privateTo *ids.UUID
		if err := rows.Scan(&thread.Key, &thread.OrganizationID,
			&thread.Newest, &thread.Count, &privateTo); err != nil {
			return nil, err
		}
		if privateTo != nil {
			thread.PrivateTo = *privateTo
		}
		due = append(due, thread)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range due {
		messages, err := threadMessages(ctx, tx, due[i].Key)
		if err != nil {
			return nil, err
		}
		due[i].Messages = messages
	}
	return due, nil
}

// threadMessages reads the tail of one conversation, oldest first, so the
// prompt reads in the order the exchange happened.
func threadMessages(ctx context.Context, tx pgx.Tx, key string) ([]threadMessage, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, coalesce(direction, ''), coalesce(subject, ''),
		       coalesce(left(body, $1), ''), occurred_at
		  FROM (SELECT id, direction, subject, body, occurred_at
		          FROM activity
		         WHERE thread_key = $2 AND kind = 'email' AND archived_at IS NULL
		         ORDER BY occurred_at DESC, id DESC
		         LIMIT $3) tail
		 ORDER BY occurred_at, id`, extractBodyLimit, key, extractThreadMessages)
	if err != nil {
		return nil, fmt.Errorf("read the conversation: %w", err)
	}
	defer rows.Close()
	var out []threadMessage
	for rows.Next() {
		var message threadMessage
		if err := rows.Scan(&message.ID, &message.Direction, &message.Subject,
			&message.Body, &message.At); err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

// recordThreadRefusal counts one refused reading of this exact conversation
// state, and does NOT advance the watermark.
//
// The two are different facts and the row keeps them apart. last_activity_at
// and message_count say what has been READ; refusals says how often the model
// failed to read it. Advancing the watermark here would retire the thread and
// lose whatever it says; counting without pinning would let a growing
// conversation inherit the refusals of text it no longer contains.
func recordThreadRefusal(
	ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, thread settledThread, now time.Time,
) (int, error) {
	var refusals int
	if err := tx.QueryRow(ctx, `
		INSERT INTO signal_thread_scan
		  (workspace_id, thread_key, last_activity_at, message_count, scanned_at,
		   refusals, refused_activity_at, refused_message_count)
		VALUES ($1, $2, '-infinity', 0, $5, 1, $3, $4)
		ON CONFLICT (workspace_id, thread_key) DO UPDATE
		   SET refusals = CASE
		         WHEN signal_thread_scan.refused_activity_at IS NOT DISTINCT FROM excluded.refused_activity_at
		          AND signal_thread_scan.refused_message_count IS NOT DISTINCT FROM excluded.refused_message_count
		         THEN signal_thread_scan.refusals + 1
		         -- A different state: the count starts again, because these are
		         -- refusals of text the model has not been shown before.
		         ELSE 1 END,
		       refused_activity_at = excluded.refused_activity_at,
		       refused_message_count = excluded.refused_message_count,
		       -- scanned_at is when this conversation was last LOOKED at, which
		       -- is what the park window is measured from.
		       scanned_at = excluded.scanned_at
		RETURNING refusals`,
		wsID, thread.Key, thread.Newest, thread.Count, now).Scan(&refusals); err != nil {
		return 0, fmt.Errorf("count the refused reading: %w", err)
	}
	return refusals, nil
}

// markThreadScanned records what THIS pass read — the newest instant and the
// message count it saw — never now() and never a fresh count. A thread that
// grew while the model was answering is left looking unread, so the next pass
// reads it again: a repeat read writes nothing new (the fingerprint holds),
// while a skipped one loses the event for good.
//
// last_activity_at takes greatest() because it must never go backwards; the
// count is overwritten, because a backfill that adds older messages legitimately
// lowers nothing and raises the count, and clamping it would hide exactly the
// change it exists to notice.
func markThreadScanned(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, thread settledThread, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO signal_thread_scan
		  (workspace_id, thread_key, last_activity_at, message_count, scanned_at,
		   resolved_org_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, thread_key) DO UPDATE
		   SET last_activity_at = greatest(signal_thread_scan.last_activity_at, excluded.last_activity_at),
		       message_count = excluded.message_count,
		       scanned_at = excluded.scanned_at,
		       -- WHICH account this reading was for. Overwritten, never
		       -- greatest()-style clamped: the account is not a high-water mark,
		       -- it is what the walk resolves to now.
		       resolved_org_id = excluded.resolved_org_id,
		       -- A reading landed, so the earlier refusals were about a model
		       -- that could not do it then, not a conversation that cannot be
		       -- done. Left standing they would park the thread on its next
		       -- refusal however long ago the others were.
		       refusals = 0,
		       refused_activity_at = NULL,
		       refused_message_count = NULL`,
		wsID, thread.Key, thread.Newest, thread.Count, now, thread.OrganizationID); err != nil {
		return fmt.Errorf("record where the read got to: %w", err)
	}
	return nil
}
