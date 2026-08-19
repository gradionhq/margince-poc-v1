// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// THE PER-CONVERSATION CONSENT FLOOR: when the member's decision about ONE
// conversation last narrowed, kept durably so that lifting the narrowing does not
// hand over the period it covered.
//
// THE DEFECT IT EXISTS FOR. A member leaves somebody out under everyone_except.
// Their messages arrive for a week and are correctly refused — which advances
// NOTHING, because a refusal lands no record and moves no bookmark. The member then
// takes them off the leave-out list, and on the next tick the entire blocked week,
// still inside Zalo's retention window, lands on the timeline. None of the
// timestamps that existed before this file can prevent it: capture_mode_since is
// from when the MODE was chosen, before the block; the cursor row's updated_at is
// from before the block too; and the verdict row that recorded the block is DELETED
// by the act of lifting it (dropVerdict), so the one fact that would have said "up
// to here you had said no" is destroyed at the moment it becomes load-bearing.
//
// THE RULE IS ASYMMETRIC, AND GETTING THE ASYMMETRY WRONG EITHER LEAKS OR LOSES:
//
//   - LIFTING AN EXCLUSION DOES NOT RETROACTIVELY ADMIT THE EXCLUDED PERIOD.
//     "Unblock Alice" means "capture Alice from now", not "capture the week I was
//     hiding". So the instant an exclusion is lifted BECOMES that conversation's
//     floor, and this file is where that instant is written and read.
//   - NAMING SOMEBODY INTO CAPTURE STILL GIVES THEM THEIR BACKLOG. Under
//     only_chosen, adding a conversation that was never excluded lands everything
//     Zalo is still holding for it — the promise the per-conversation cursor was
//     introduced to keep. That conversation has no row here, so nothing here
//     touches it.
//
// The principle both arms fall out of, and the one sentence to keep: AN EXPLICIT
// EXCLUSION LEAVES A MARK; A CONVERSATION THAT WAS NEVER EXCLUDED CARRIES NONE.
//
// WHAT AN EXPLICIT EXCLUSION IS, exactly, because the whole rule turns on it: a
// stored `block` verdict, which is the only thing in this unit that is a member
// naming ONE conversation to leave out. Two things that look adjacent are NOT
// explicit exclusions, deliberately:
//
//   - NOT BEING NAMED under only_chosen. That is the mode's default-deny, not a
//     decision about anybody, and treating it as one would put a floor on every
//     conversation a member has ever had — the exact opposite of the promise above.
//   - SWITCHING MODE, including everyone_except -> only_chosen, which does exclude
//     everyone not named. It is a global narrowing and the GLOBAL instrument
//     already records it: capture_mode_since is re-stamped by the switch and again
//     by the switch back, which is what makes the round trip safe today (see
//     admitsUnderMode and the bookmark's own timestamp). Writing per-conversation
//     floors for it would mean writing a row for every conversation the member has
//     ever had — a set this unit does not even know — to record a fact one column
//     already holds.
//
// WHY ITS OWN TABLE. Two cheaper homes were considered and each is wrong for a
// reason this unit already states about itself:
//
//   - THE VERDICT ROW cannot hold it, because lifting the exclusion is exactly the
//     act that deletes that row. Keeping a TOMBSTONE instead — not deleting, marking
//     — was the closest rival: it needs no new table and the write is already there.
//     It was rejected because the verdict table is the member's own list, read back
//     to them by the chooser and counted by their status screen, and a row that
//     survives their asking for it to be removed makes every reader of that list
//     responsible for telling consent from residue. The one place that gets it wrong
//     shows a person as still decided-about when the member removed them.
// NOTHING FORGETS A FLOOR, INCLUDING CONNECTING A DIFFERENT ZALO ACCOUNT — which
// drops this member's cursors and send markers, because those name ids in the old
// account's space (see forgetCursorsOf). A floor is not dropped alongside them, and
// the asymmetry is deliberate: the VERDICTS survive an account change too, so a floor
// derived from one has to survive with them, and the two directions of being wrong are
// not equal. Keeping a stale floor costs a named conversation the history above it in
// the new account. Dropping one hands over a period a member excluded. The safe
// direction for a consent boundary is to keep it.
//
//   - THE CURSOR ROW is where a per-conversation fact would naturally go, and it is
//     the wrong shelf twice over. It would put a consent decision in the table this
//     unit declares to be SCHEDULING state — "losing one costs a deduplicated
//     replay", said in cursor.go to justify that it may be lost. A floor may not be
//     lost: losing one hands over the excluded period. And last_msg_id is NOT NULL
//     under a numeric CHECK, so a floor for a conversation nothing ever landed from
//     would need an invented sentinel bookmark — which admitsUnderMode reads as
//     evidence that capture has been reading that conversation, silencing the very
//     floor being written.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// floorEntity is what the LEDGER calls this table, and floorTable is what SQL calls
// it — the same derivation as this unit's other tables', for the same reason:
// audit_log.entity_type names a kind of record and takes no schema, while a
// statement resolves through a search_path the ext schema is not on.
const floorEntity = "ext_zalo_personal_conversation_floor"

const floorTable = "ext." + floorEntity

// floorColumns is the projection every read and every write of a floor returns, in
// one place so a column added to the table is one edit.
const floorColumns = `id::text, channel_user_id, not_before, version`

// conversationFloor is one conversation's floor as this unit reads and records it.
type conversationFloor struct {
	ID            string
	ChannelUserID string
	// NotBefore is the instant the last explicit exclusion of this conversation was
	// lifted. Nothing that occurred at or before it may be captured.
	NotBefore time.Time
	Version   int
}

// scanFloor reads floorColumns off one row.
func scanFloor(scan func(...any) error) (conversationFloor, error) {
	var floor conversationFloor
	if err := scan(&floor.ID, &floor.ChannelUserID, &floor.NotBefore, &floor.Version); err != nil {
		return conversationFloor{}, err
	}
	return floor, nil
}

// floorsOf reads every conversation floor this member holds.
//
// The whole set at once, exactly as the verdicts and the cursors are read: one drain
// can touch every conversation a member has, and a query per frame would be a round
// trip per message on the one path that must hold nothing open while it ingests.
//
// A conversation ABSENT from the answer has no floor, which is the load-bearing
// state — it is what lets a newly named conversation collect its whole backlog.
func floorsOf(ctx context.Context, tx extension.Tx, member string) (map[string]time.Time, error) {
	rows, err := tx.Query(ctx,
		`SELECT channel_user_id, not_before FROM `+floorTable+`
		  WHERE user_id = $1::uuid`, member)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]time.Time{}
	for rows.Next() {
		var (
			counterparty string
			notBefore    time.Time
		)
		if err := rows.Scan(&counterparty, &notBefore); err != nil {
			return nil, err
		}
		found[counterparty] = notBefore
	}
	return found, rows.Err()
}

// exclusionLifted reports that one verdict write ENDS an explicit exclusion: the
// member had this conversation on their leave-out list, and after this write they do
// not.
//
// It is asked of the BEFORE-IMAGE the database returned rather than of what the
// screen believed was stored, because a save arrives as a whole list and a member
// may be re-sending a decision that has not changed. Only a `block` that stops being
// one is a lift.
func exclusionLifted(before *allowEntry, after verdict) bool {
	return before != nil && before.Mode == verdictBlock && after != verdictBlock
}

// raiseFloor records that this conversation's exclusion ended NOW, in the caller's
// transaction — the same one the verdict change commits in, so a floor cannot be
// missing for a lift that happened or present for one that rolled back.
//
// now() is the DATABASE's, never this process's: every timestamp the filter compares
// this against — capture_mode_since, the cursor's updated_at — is written from the
// server's clock, and a floor written from a drifting application clock is a consent
// boundary in the wrong place.
//
// IT ONLY EVER MOVES FORWARD, which is a property of the statement rather than a
// rule somebody has to keep: every write is now(), and now() is later than whatever
// was there. A floor that could move backwards would re-open a period the member had
// closed.
func raiseFloor(ctx context.Context, tx extension.Tx, member, counterparty string) error {
	before, err := floorFor(ctx, tx, member, counterparty)
	if err != nil {
		return err
	}
	after, err := scanFloor(tx.QueryRow(ctx,
		`INSERT INTO `+floorTable+` (workspace_id, user_id, channel_user_id, not_before)
		 VALUES (`+callerWorkspace+`, $1::uuid, $2, now())
		 ON CONFLICT (workspace_id, user_id, channel_user_id) DO UPDATE
		    SET not_before = now(),
		        version = `+floorTable+`.version + 1,
		        updated_at = now()
		 RETURNING `+floorColumns, member, counterparty).Scan)
	if err != nil {
		return err
	}
	return recordFloor(ctx, tx, before, &after)
}

// floorFor reads one stored floor, or nothing. It is the before-image of the write
// above: what is recorded is the row the database held rather than what this code
// believed was there.
func floorFor(ctx context.Context, tx extension.Tx, member, counterparty string) (*conversationFloor, error) {
	found, err := scanFloor(tx.QueryRow(ctx,
		`SELECT `+floorColumns+` FROM `+floorTable+`
		  WHERE user_id = $1::uuid AND channel_user_id = $2`, member, counterparty).Scan)
	if err != nil {
		if errors.Is(err, extension.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &found, nil
}

// recordFloor writes the ledger row and the event for one lifted exclusion.
//
// IT IS RECORDED SEPARATELY FROM THE VERDICT that caused it, and the separation is
// the point rather than duplication. `verdict_dropped` says a member removed a
// decision; this says from WHEN this installation may read that conversation — which
// is the question somebody actually arrives with ("why is there a week missing from
// this thread?", "were we ever reading this person in March?"), and the only place it
// can be answered from, because the verdict row that carried the exclusion is gone.
//
// It names the counterparty and the instant, and nothing about the conversation
// itself: this unit records who was decided about, never what they said.
func recordFloor(ctx context.Context, tx extension.Tx, before, after *conversationFloor) error {
	payload, err := json.Marshal(struct {
		ChannelUserID string `json:"channel_user_id"`
		NotBefore     string `json:"not_before"`
	}{ChannelUserID: after.ChannelUserID, NotBefore: after.NotBefore.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	beforeImage, err := floorImage(before)
	if err != nil {
		return err
	}
	afterImage, err := floorImage(after)
	if err != nil {
		return err
	}
	return tx.Record(ctx,
		extension.Change{
			Action: floorActionFor(before),
			Entity: floorEntity,
			ID:     after.ID,
			Before: beforeImage,
			After:  afterImage,
		},
		extension.Event{Verb: eventExclusionLifted, Payload: payload})
}

// floorActionFor reports whether this write created the floor or moved one. A member
// who excludes the same person a second time and lifts it again moves theirs, and
// recording that as a create would put a ledger row with no before-image over a state
// that existed.
func floorActionFor(before *conversationFloor) extension.AuditAction {
	if before == nil {
		return extension.AuditCreate
	}
	return extension.AuditUpdate
}

// floorImage renders one side of a floor change, or nothing at all. A missing image
// is nil rather than `null`, exactly as verdictImage's is: a create has no before,
// and the ledger reads "there was no such state" as an absent column rather than a
// JSON null sitting in one.
func floorImage(floor *conversationFloor) (json.RawMessage, error) {
	if floor == nil {
		return nil, nil
	}
	return json.Marshal(struct {
		ChannelUserID string `json:"channel_user_id"`
		NotBefore     string `json:"not_before"`
		Version       int    `json:"version"`
	}{
		ChannelUserID: floor.ChannelUserID,
		NotBefore:     floor.NotBefore.UTC().Format(time.RFC3339),
		Version:       floor.Version,
	})
}
