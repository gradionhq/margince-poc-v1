// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The scheduled tick: the only thing in this unit that happens without a person,
// and the half that makes the member's choice mean something.
//
// FOUR RULES SHAPE THIS FILE AND EACH ONE IS A DEFECT SOMEBODY ELSE ALREADY PAID
// FOR:
//
//  1. A MEMBER WHO HAS NOT CHOSEN IS SKIPPED ENTIRELY — no socket is opened for
//     them at all. Zalo's own queue holds their messages and this installation
//     holds nothing. That is the ARCHITECTURAL half of the consent story: not a
//     filter that discards what it read, but a read that never happens.
//  2. READ, CLOSE, INGEST, THEN MOVE THE CURSOR. Runtime.Ingest opens its own
//     transaction, so calling it inside one of ours takes a second connection
//     while holding one — which on a small pool does not fail, it HANGS. The core
//     refuses it (ErrNestedIngest) and this unit's own fake refuses it too, or
//     the suite would agree with the bug.
//  3. THE CURSOR ADVANCES ONLY AFTER AN INGEST RETURNS, and only past messages
//     the filters admitted. A cursor not advanced past a message that landed
//     costs one deduplicated retry; a cursor advanced past one that did not land
//     costs the message.
//  4. last_polled_at IS WRITTEN EVEN ON A FAILED TURN. It is what the fairness
//     order reads, so a member whose session is dead would otherwise sit at the
//     front of the queue forever and starve everybody behind them.
//
// COST, STATED RATHER THAN DISCOVERED: no acknowledgement has been identified on
// this protocol, so nothing this unit does removes a message from Zalo's queue.
// Every tick therefore re-reads the member's whole undelivered backlog, bounded
// by the server's retention window. That is CORRECT under capture's natural-key
// dedupe and it is WASTEFUL; finding the ack is a cost optimisation, not a
// correctness fix.

import (
	"context"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// inbox is one member's resumed session, as the tick uses it: the account it
// belongs to, the messages waiting for it, and the roster that names the people
// in them.
//
// It is an interface so the whole tick — the fairness order, the three filters,
// the record mapping, the cursor — is driven end to end without a socket. Not to
// abstract over two implementations, of which there is one, but because the
// alternative is a consent filter proven only in production.
type inbox interface {
	// UID is which Zalo account this session belongs to, as the provider states
	// it. It namespaces both keys of every record the turn lands.
	UID() string
	// drainInbox opens the socket, requests the undelivered backlog, collects
	// what arrives until the socket has been quiet for the given period, and
	// closes. There is no held socket and no per-member goroutine: a tick that
	// leaves nothing open is what lets this unit be scheduled rather than run as
	// a fleet of daemons.
	drainInbox(ctx context.Context, quiet time.Duration) ([]zaloInbound, error)
	// friends is the member's own contact list, used to put a better name on a
	// person the frame already identifies.
	friends(ctx context.Context) ([]zaloFriend, error)
}

// openFunc turns a sealed credential into a usable session. It is the seam every
// test in this unit drives instead of the wire.
type openFunc func(ctx context.Context, sealed zaloSealed) (inbox, error)

// openSession is the production one. The options are EMPTY for the reason send's
// are: the user agent and the device id travel inside the sealed document, and a
// second copy here could disagree with them — at which point Zalo sees a
// different device and the session stops being the one the member scanned in.
func openSession(ctx context.Context, sealed zaloSealed) (inbox, error) {
	resumed, err := zaloResume(ctx, sealed, zaloOptions{})
	if err != nil {
		// Returned explicitly rather than as (resumed, err): a typed nil in an
		// interface is non-nil to every caller that checks it.
		return nil, err
	}
	return resumed, nil
}

// perMemberBudget bounds one member's turn. The job's own wall clock
// (api/jobs.yaml) bounds the whole tick; this is what keeps the first stalled
// session in the list from spending it, so a stall costs that member's turn
// rather than everybody after them.
const perMemberBudget = 60 * time.Second

// drainQuiet is how long the socket must be silent before the drain calls the
// backlog delivered. It is a property of a push protocol with no end-of-queue
// marker: the only signal that the queue is empty is that nothing more arrives.
// The per-member deadline above is what bounds it if the socket never goes quiet.
const drainQuiet = 3 * time.Second

// pollInbox is one workspace's tick.
func pollInbox(ctx context.Context, rt extension.Runtime) error {
	return pollFleet(ctx, rt, openSession)
}

// pollFleet is the tick's whole behaviour, with the provider seam injected.
func pollFleet(ctx context.Context, rt extension.Runtime, open openFunc) error {
	members, err := capturingMembers(ctx, rt)
	if err != nil {
		return err
	}
	var failures int
	for _, conn := range members {
		memberCtx, done := context.WithTimeout(ctx, perMemberBudget)
		turn, pollErr := pollMember(memberCtx, rt, conn, open)
		done()
		if pollErr != nil {
			failures++
		}
		// On the TICK's context, not the member's: the member's is exactly what
		// may have just expired, and this write must not be lost — it carries
		// last_polled_at, which is what stops the next tick starting at the same
		// member with no record of why.
		if noted := recordTurn(ctx, rt, conn, turn); noted != nil {
			return noted
		}
	}
	if failures > 0 && failures == len(members) {
		// Every member failing is not one person's problem: it is this
		// installation's egress or Zalo being down, and a tick that answered
		// success would leave a fleet-wide outage with no signal anywhere but
		// the rows.
		//
		// REPORTED BEFORE THE SWEEP BELOW, so a housekeeping failure cannot stand
		// in front of a fleet-wide outage and make it look like a database
		// problem.
		return fmt.Errorf("zalo-personal: all %d connection(s) failed this tick", failures)
	}
	// The markers are swept on the tick because the only thing that makes one
	// worth keeping is a drain that might still read the message it names, and the
	// drain is here. It is LAST for the reason above, and a failure is reported
	// rather than dropped: the records have already landed and the cursors have
	// already moved, so what a retry re-does is a DELETE that was idempotent
	// anyway — while a table nobody can prune is the one thing in this unit that
	// grows forever.
	return forgetOldSentMarkers(ctx, rt)
}

// capturingMembers reads the connections this tick may act on, and CLOSES the
// transaction before anything is ingested.
//
// THREE PREDICATES, AND capture_enabled IS THE LOAD-BEARING ONE: a member who has
// not chosen which conversations to allow is not enumerated here at all, so no
// credential of theirs is unsealed and no socket of theirs is opened.
//
// THE DUE PREDICATE IS ENFORCED HERE, IN THE DATABASE, and that placement is the
// point rather than an implementation detail: a member inside their backoff is not
// returned, so the tick cannot open a socket for them even by mistake. A check in
// Go after the read would be a filter somebody can forget past, over rows that had
// already been fetched — and the whole cost this feature exists to avoid is the
// handshake that a forgotten filter would still pay.
//
// LEAST RECENTLY POLLED FIRST AMONG THOSE THAT ARE DUE, which is fairness rather
// than tidiness: a fixed order plus a bounded tick means the members at the end of
// a stable list are the ones a busy installation never reaches, tick after tick. A
// connection that has never polled sorts first.
func capturingMembers(ctx context.Context, rt extension.Runtime) ([]connection, error) {
	var found []connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+connectionColumns+` FROM `+connectionTable+`
			  WHERE status = $1 AND capture_enabled
			    AND (poll_after IS NULL OR poll_after <= now())
			  ORDER BY last_polled_at ASC NULLS FIRST, created_at`, statusConnected)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			conn, err := scanConnection(rows.Scan)
			if err != nil {
				return err
			}
			found = append(found, conn)
		}
		return rows.Err()
	})
	return found, err
}

// pollOutcome is what one member's turn learned, and it is always usable — a
// failed turn still carries whatever the cursor reached and the class the row
// should now show, because recordTurn writes it either way.
type pollOutcome struct {
	status     string
	errorClass string
	// landed reports whether this turn produced at least one record. It is what
	// decides the member's next cadence, and it is deliberately NOT "did the drain
	// return frames": a drain full of already-seen messages, echoes of the CRM's own
	// replies, and conversations the member never chose has told this installation
	// nothing, and asking again in five minutes would spend a handshake to be told
	// the same nothing.
	//
	// COUNTED BY THE LANDING PASS rather than inferred from a cursor moving. It used
	// to be `cursor != conn.LastMsgID`, which stopped being expressible when the
	// cursor became one per counterparty — and was always the weaker statement, since
	// it read a bookmark to answer a question about work.
	landed bool
	// failed reports that the turn did not finish. A failure is a THIRD answer about
	// cadence, distinct from "nothing to say": see cadenceAfter.
	failed bool
}

// refused marks the turn. The cursors it already earned are written by
// advanceVerdictCursors before this is ever reached, so a systemic failure halfway
// through a drain does not un-land the messages before it.
func (o pollOutcome) refused(status string, cause error) pollOutcome {
	o.status, o.errorClass, o.failed = status, failureClass(cause), true
	return o
}

// pollMember takes one member's turn.
//
// The error it answers is for the FLEET's bookkeeping — how many members failed
// — and never for the row: what the row learns is in the outcome, which the
// caller writes whether or not the turn worked.
func pollMember(ctx context.Context, rt extension.Runtime, conn connection, open openFunc) (pollOutcome, error) {
	turn := pollOutcome{status: conn.Status}
	entries, err := allowedFor(ctx, rt, conn.UserID)
	if err != nil {
		return turn.refused(conn.Status, err), err
	}
	if !consentOf(conn, entries).captures() {
		// Armed with a mode that admits nothing — only_allowed whose inclusion list
		// is empty, or a mode this unit does not recognise. Nothing this drain
		// returned could be kept, so no socket is opened for the same reason an
		// unarmed member's is not.
		return turn, nil
	}
	opened, err := openFor(ctx, rt, conn, open)
	if err != nil {
		// TWO FAILURES WITH OPPOSITE RECOVERIES, told apart by the error rather
		// than by a string: a credential Zalo has stopped accepting needs that
		// human to scan a QR with their phone, and nothing this unit retries will
		// fix it — while a request that never reached Zalo says nothing about the
		// credential at all. Parking the second as needs_reconnect takes the member
		// out of the fleet read until they re-scan, so one tick of Zalo being
		// unreachable would demand that of every rep on the installation at once.
		if unreachedTheProvider(err) {
			return turn.refused(conn.Status, err), err
		}
		return turn.refused(statusNeedsReconnect, err), err
	}
	frames, err := opened.drainInbox(ctx, drainQuiet)
	if err != nil {
		return turn.refused(conn.Status, err), err
	}
	// The socket is closed by the drain, so every ingest below happens with
	// nothing of this unit's open — neither a transaction nor a connection.
	against, err := decideAgainst(ctx, rt, conn, entries, frames, opened)
	if err != nil {
		return turn.refused(conn.Status, err), err
	}
	if against == nil {
		// The member withdrew, or disarmed capture, while this drain was running.
		// Nothing is landed and nothing is recorded about a position they no longer
		// consent to be at.
		return turn, nil
	}
	got, err := landAll(ctx, rt, conn, frames, *against)
	turn.landed = got.landed > 0
	// The cursors move in a commit of their own, AFTER every ingest has returned,
	// and before the row write below — so a failure here costs a deduplicated replay
	// rather than the messages.
	if saved := advanceCursors(ctx, rt, conn.UserID, got.reached); saved != nil {
		return turn.refused(conn.Status, saved), saved
	}
	if err != nil {
		return turn.refused(conn.Status, err), err
	}
	return turn, nil
}

// decideAgainst gathers what the drained frames are judged against, RE-READING the
// member's consent after the drain rather than trusting what it was before.
//
// WHY THE RE-READ, and it is the narrow hole it closes rather than a tidy-up: the
// verdicts that opened the socket were read seconds earlier, and in between the
// member can disconnect or block that very counterparty. Landing on the stale set
// means this installation captures a private conversation somebody had just
// explicitly withdrawn from — in the one unit where that is the entire point. The
// connection row's version guard does not help: it protects the row write at the end
// of the turn, not the records crossing into capture before it.
//
// WHAT IT DOES NOT CLOSE, stated rather than implied. The window shrinks from "the
// whole drain, including the network" to "the ingest loop", and it cannot reach zero
// without re-reading the verdicts per record, which is a query per message. The
// residual window is NOT instantaneous either: the loop is one sequential Ingest per
// surviving frame, so a large backlog spends seconds to minutes inside it. A member
// who blocks a counterparty mid-loop may still have one message of theirs captured;
// the core's own erasure path is what removes it, exactly as for every other
// connector. The trade stands on the size of the window it CLOSED, not on the one it
// leaves.
//
// It answers nil when there is nothing left to judge — the member is gone, or capture
// is no longer armed — which the caller treats as an abandoned turn rather than a
// failure, because nothing failed.
func decideAgainst(ctx context.Context, rt extension.Runtime, conn connection,
	entries []allowEntry, frames []zaloInbound, opened inbox,
) (*filters, error) {
	// ONE transaction for both reads, so the verdicts and the consent they hang off
	// are the same instant rather than two.
	var (
		still   *connection
		fresh   []allowEntry
		reached map[string]bookmark
	)
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		if still, err = connectionOf(ctx, tx, conn.UserID); err != nil || still == nil {
			return err
		}
		if fresh, err = verdictsOf(ctx, tx, conn.UserID); err != nil {
			return err
		}
		reached, err = cursorsOf(ctx, tx, conn.UserID)
		return err
	}); err != nil {
		return nil, err
	}
	if still == nil || !still.CaptureEnabled || still.Status != statusConnected {
		return nil, nil
	}
	// WHICH ECHOES ARE OURS, read before the first ingest and in a transaction that
	// closes with this call. It is what tells a reply the CRM staged from a reply the
	// rep typed on their phone; see sentmessage.go.
	ours, err := sentByThisCRM(ctx, rt, conn.UserID, echoedIDs(frames))
	if err != nil {
		return nil, err
	}
	// The names come from the roster and from the verdicts as they were read at the
	// START of the turn as well as now: a display name is not consent, and losing one
	// because a member edited their list mid-drain would leave a record unnamed for
	// no reason.
	return &filters{
		// THE MODE COMES FROM THE RE-READ ROW TOO, not from the one the fleet read:
		// a member who switched mode while the socket was open must be judged on the
		// mode they are in now, exactly as with their list.
		by:      consentOf(*still, fresh),
		cursors: reached,
		names:   namesByCounterparty(append(entries, fresh...), rosterFor(ctx, opened)),
		ours:    ours,
	}, nil
}

// consentOf gathers one member's whole answer to "which conversations go into the
// CRM?" out of the row and their list, so the mode, its floor and the verdicts cannot
// be assembled from two different reads at two different moments.
func consentOf(conn connection, entries []allowEntry) consent {
	return consent{
		mode:     conn.CaptureMode,
		since:    conn.modeFloor,
		verdicts: verdictsByCounterparty(entries),
	}
}

// allowedFor reads this member's verdicts and closes the transaction. It is read
// ONCE per turn rather than per message: a query per frame would be a round trip
// per message on the one path that must hold nothing open.
//
// The ENTRIES rather than the verdict map, because the turn needs two things out
// of them — the decision, and the display name the member's screen showed for that
// person, which is the only name this unit has for the counterparty of a message
// the member SENT.
func allowedFor(ctx context.Context, rt extension.Runtime, member string) ([]allowEntry, error) {
	var entries []allowEntry
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		entries, err = verdictsOf(ctx, tx, member)
		return err
	})
	return entries, err
}

// openFor unseals this member's own credential and resumes it.
func openFor(ctx context.Context, rt extension.Runtime, conn connection, open openFunc) (inbox, error) {
	sealed, err := unsealSession(ctx, rt, extension.UserID(conn.UserID))
	if err != nil {
		return nil, err
	}
	opened, err := open(ctx, sealed)
	if err != nil {
		return nil, fmt.Errorf("zalo-personal: this member's session could not be resumed: %w", err)
	}
	return opened, nil
}
