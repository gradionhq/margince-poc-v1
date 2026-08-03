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
	Count    int
	Messages []threadMessage
}

// dueThreads lists the conversations that have settled and have moved since
// they were last read, newest first.
//
// The org resolution is deliberately strict: exactly one organization across
// the whole thread. A conversation touching two accounts would have its events
// filed against whichever the join happened to pick, and a signal on the wrong
// account is worse than no signal — it is a claim the reader cannot trace back.
func dueThreads(ctx context.Context, tx pgx.Tx, now time.Time, limit int) ([]settledThread, error) {
	settled := now.Add(-extractSettleHours * time.Hour)
	rows, err := tx.Query(ctx, `
		WITH conversation AS (
			SELECT a.thread_key,
			       max(a.occurred_at) AS newest,
			       count(DISTINCT a.id) AS message_count,
			       min(l.organization_id::text) AS one_org,
			       count(DISTINCT l.organization_id) AS org_count
			  FROM activity a
			  JOIN activity_link l ON l.activity_id = a.id AND l.entity_type = 'organization'
			 WHERE a.thread_key IS NOT NULL AND a.kind = 'email'
			   AND a.archived_at IS NULL AND a.captured_by LIKE 'connector:%'
			 GROUP BY a.thread_key
		)
		SELECT c.thread_key, c.one_org::uuid, c.newest, c.message_count
		  FROM conversation c
		  LEFT JOIN signal_thread_scan s ON s.thread_key = c.thread_key
		 WHERE c.org_count = 1
		   AND c.newest <= $1
		   -- Due when the conversation has MOVED in either way it can. The
		   -- timestamp misses a message inserted at the same instant and a
		   -- backfill that adds older ones; the count sees both.
		   AND (s.thread_key IS NULL
		        OR s.last_activity_at < c.newest
		        OR s.message_count <> c.message_count)
		 ORDER BY c.newest DESC
		 LIMIT $2`, settled, limit)
	if err != nil {
		return nil, fmt.Errorf("list the threads due for a read: %w", err)
	}
	defer rows.Close()
	var due []settledThread
	for rows.Next() {
		var thread settledThread
		if err := rows.Scan(&thread.Key, &thread.OrganizationID,
			&thread.Newest, &thread.Count); err != nil {
			return nil, err
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
		  (workspace_id, thread_key, last_activity_at, message_count, scanned_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, thread_key) DO UPDATE
		   SET last_activity_at = greatest(signal_thread_scan.last_activity_at, excluded.last_activity_at),
		       message_count = excluded.message_count,
		       scanned_at = excluded.scanned_at`,
		wsID, thread.Key, thread.Newest, thread.Count, now); err != nil {
		return fmt.Errorf("record where the read got to: %w", err)
	}
	return nil
}
