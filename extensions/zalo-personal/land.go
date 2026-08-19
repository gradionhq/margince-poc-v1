// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What one turn does with the frames it drained: decide each one against the
// member's own choices, hand over what survives, and answer how far the cursor may
// move.
//
// SPLIT FROM poll.go, which owns whose turn it is and what the row learns, because
// these are two subjects a reader arrives with different questions about. The rules
// that live HERE are the ones about a single message — which of two kinds of echo it
// is, whether the member admitted the person at the other end, and whether it has
// already landed. record.go turns the survivors into records; this file decides
// which survivors there are.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// echoedIDs is every id in this drain that came back as one of this member's OWN
// outgoing messages. Only those need asking about — an inbound message was never
// sent by this CRM, so including it would widen the query for no answer.
func echoedIDs(frames []zaloInbound) []string {
	var ids []string
	for _, frame := range frames {
		if frame.selfSent() {
			ids = append(ids, frame.MsgID)
		}
	}
	return ids
}

// filters is everything one turn decides a frame against, gathered so landAll
// takes one argument that is obviously a set of decisions rather than four
// same-shaped maps a caller can transpose.
type filters struct {
	// by is the member's own choice — mode, when they chose it, and their list —
	// RE-READ after the drain. See the note on landAll.
	by consent
	// cursors is how far capture has already got with each conversation, and when
	// each position was written. A conversation absent from it has no bookmark,
	// which is what lets a just-included conversation through.
	cursors map[string]bookmark
	// names is the display name this unit knows for a counterparty — never the one
	// an outgoing frame carries, which is the member's own.
	names map[string]string
	// ours is the ids the CRM transmitted as this member.
	ours map[string]bool
}

// namesByCounterparty is every display name this unit can honestly put on a
// counterparty: the roster's, and the one the member's own screen showed when they
// chose that conversation.
//
// The SAVED name is the fallback rather than the roster, and it matters for the
// case this whole change is about: a rep replies from their phone to somebody Zalo
// no longer lists as a contact, and the only name anybody here has is the one that
// member saved. Preferring the roster where both exist keeps a renamed contact
// current.
func namesByCounterparty(entries []allowEntry, roster map[string]zaloFriend) map[string]string {
	names := make(map[string]string, len(entries)+len(roster))
	for _, entry := range entries {
		if entry.DisplayName != "" {
			names[entry.ChannelUserID] = entry.DisplayName
		}
	}
	for id, friend := range roster {
		if friend.DisplayName != "" {
			names[id] = friend.DisplayName
		}
	}
	return names
}

// rosterFor is the roster this turn will put names from, or nothing at all.
//
// A roster failure is DECIDED here rather than propagated, and the decision is
// the one the design states: the roster is ENRICHMENT. Every frame already
// carries the account id and the display name a record needs, so what a failed
// roster call costs is a possibly-better name, and what propagating it would cost
// is the message. The member's own chooser screen answers differently — see
// rosterOf, where "we could not ask Zalo" is not "you know nobody".
func rosterFor(ctx context.Context, opened inbox) map[string]zaloFriend {
	friends, err := opened.friends(ctx)
	if err != nil {
		return nil
	}
	return friendsByID(friends)
}

// friendsByID indexes a roster by account id, dropping an entry that names none:
// an entry with no id can match no message and would sit in a chooser as a person
// nobody can decide about.
func friendsByID(friends []zaloFriend) map[string]zaloFriend {
	byID := make(map[string]zaloFriend, len(friends))
	for _, friend := range friends {
		if friend.UserID != "" {
			byID[friend.UserID] = friend
		}
	}
	return byID
}

// turnLanding is what one turn's landing pass achieved: how far each conversation
// got, and whether anything new arrived at all.
type turnLanding struct {
	// reached is the highest id landed PER COUNTERPARTY. Only conversations this
	// turn actually landed something in appear, so a cursor is never written for a
	// conversation nothing happened in.
	reached map[string]string
	// landed counts the frames that passed every filter and were decided about. It
	// is the cadence signal, and it is counted rather than inferred from a cursor
	// because a per-counterparty cursor has no single value to compare against.
	landed int
}

// landAll filters the drain and hands what survives to capture, oldest first.
//
// IT ANSWERS A POSITION PER CONVERSATION, and that is the fix for the defect a single
// per-member cursor had: a maximum over every conversation buries every
// lower-numbered message dropped from a conversation the member had not chosen, so
// the conversation they include next starts EMPTY. Per conversation, a just-included
// one has no bookmark at all and everything Zalo still holds for it passes — the
// promise holds by construction instead of by argument, in BOTH modes.
//
// A message dropped by any filter advances NOTHING, here or elsewhere.
//
// Oldest first so that what was decided about is a contiguous run from the
// bottom: a turn that stops halfway leaves everything above it untouched and
// above its conversation's mark, where the next tick finds it again.
func landAll(ctx context.Context, rt extension.Runtime, conn connection, frames []zaloInbound,
	against filters,
) (turnLanding, error) {
	sort.Slice(frames, func(i, j int) bool { return earlier(frames[i].MsgID, frames[j].MsgID) })
	got := turnLanding{reached: map[string]string{}}
	for _, frame := range frames {
		other := frame.counterparty()
		if keep, _ := admits(frame, against.by, against.cursors[other], against.ours); !keep {
			continue
		}
		// A systemic failure — the port refused this unit, the member's
		// authority is gone, the role composed no capture — stops the turn here.
		// Nothing above this id was touched and every conversation's mark stays
		// where its last landed message put it, so the region is read again next
		// tick, where every record that already landed is a deduplicated no-op.
		landed, err := landOne(ctx, rt, conn, frame, against.names)
		if err != nil {
			return got, err
		}
		// A DROP STILL ADVANCES THE BOOKMARK — it will never land however many
		// times it is offered, so parking on it re-reads it forever — but only when
		// the cursor table can HOLD it. Both of the columns the advance writes are
		// CHECKed there: a decimal message id, and a counterparty that names
		// somebody. The advance is one multi-row statement, so a single entry the
		// CHECK refuses fails it whole and NO conversation's bookmark moves that
		// turn. The frame stays in Zalo's queue, so that is every tick.
		if bookmarkable(other, frame.MsgID) {
			got.reached[other] = higher(got.reached[other], frame.MsgID)
		}
		// COUNTED ONLY WHEN SOMETHING ACTUALLY LANDED. This is the cadence signal,
		// and counting a drop as work keeps a member at the maximum poll frequency
		// for as long as one undroppable frame sits in the queue.
		if landed {
			got.landed++
		}
	}
	return got, nil
}

// landOne hands one message to capture, and separates the two failures that look
// alike. It answers whether the message LANDED, so a caller can tell the two
// no-error outcomes apart — a record capture took, and a record that was dropped.
//
// A message this unit cannot represent, or one capture calls INVALID, will never
// land however many times it is offered — so it is dropped with a ledger row that
// says why. Every other refusal is about this unit's standing, and those must stop
// the turn rather than skip a message nobody has seen.
func landOne(ctx context.Context, rt extension.Runtime, conn connection, frame zaloInbound,
	names map[string]string,
) (bool, error) {
	rec, err := recordFor(frame, conn.ZaloUID, names)
	if err != nil {
		return false, noteDrop(ctx, rt, conn, frame, "unrepresentable")
	}
	// on is the CRM MEMBER whose credential produced this message — the
	// connection's own user id, never the Zalo account id. Capture checks that
	// this member holds a credential with this unit and resolves what they may
	// do right now, so what lands is bounded by their live authority rather than
	// by anything this unit asserts.
	if _, err := rt.Ingest(ctx, extension.UserID(conn.UserID), rec); err != nil {
		if errors.Is(err, extension.ErrInvalid) {
			return false, noteDrop(ctx, rt, conn, frame, "refused_by_the_core")
		}
		return false, err
	}
	return true, nil
}

// noteDrop records that one message will never land, and why.
//
// It writes this unit's own ledger row because there is no core record to hang a
// drop on — that is what a drop is. What it buys is that "this connector has been
// dropping everything since Tuesday" is a question somebody can answer, which a
// silent skip turns into a feed that merely looks quiet.
//
// IT NAMES THE MESSAGE ID AND NOT THE MESSAGE. A frame this unit refused to
// represent is still somebody's private conversation, and a diagnostic that
// copied the body into the audit trail would capture, under a different name,
// exactly what the filter declined to capture.
func noteDrop(ctx context.Context, rt extension.Runtime, conn connection, frame zaloInbound, class string) error {
	payload, err := json.Marshal(struct {
		MsgID string `json:"msg_id"`
		Class string `json:"class"`
	}{MsgID: frame.MsgID, Class: class})
	if err != nil {
		return err
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		return tx.Record(ctx,
			extension.Change{
				Action: extension.AuditUpdate,
				Entity: connectionEntity,
				ID:     conn.ID,
				Detail: payload,
			},
			extension.Event{Verb: eventMessageDropped, Payload: payload})
	})
}
