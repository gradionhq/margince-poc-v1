// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// "Check now": the member's own way out of a quiet-period wait.
//
// WHAT IT ACTUALLY DOES IS SMALLER THAN ITS NAME, and every line below depends on
// that being said plainly. It clears the backoff on the caller's own connection row
// and returns. It opens no socket, spends no credential and fetches no message; the
// scheduled dispatcher — which runs at least once a minute (saveVisibleWithin) — is
// what looks. There is no honest way for this operation to look itself: an extension
// cannot enqueue a job, and a drain from inside a request is refused because the
// credential's drain is an unattended act. So the whole mechanism is one UPDATE, and
// the copy on the member's screen promises exactly that and nothing more.
//
// WHY IT EXISTS AS ITS OWN VERB rather than being folded into the save. A member who
// has been quiet sits inside a capped wait of up to maxPollBackoff, and their own
// screen names it. The remedy it named was "save your list again" — and a member who
// has already chosen the right list has nothing to save, so the control carrying that
// remedy was inert at exactly the moment they reached for it. Twice in live testing a
// working connector was read as a dead one for that reason. Relaxing the save's own
// "nothing to save" rule was the other route and is the worse one: a save button that
// saves nothing is a second confusion, and consent is not a thing to re-state as a
// side effect of asking for a refresh.
//
// AS EVERYWHERE IN THIS UNIT, the member is rt.Caller().UserID. It declares no member
// argument and the strict decoder would refuse one — the schedule of somebody else's
// personal account is not this caller's to move.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// checkNow makes the calling member's next check due, and reports whether that
// ended a wait.
func checkNow(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	member, err := connectingMember(rt)
	if err != nil {
		return nil, err
	}
	var waiting bool
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		waiting, err = bringCheckForward(ctx, tx, string(member))
		return err
	}); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		WasWaiting bool `json:"was_waiting"`
	}{WasWaiting: waiting})
}

// bringCheckForward clears the member's backoff and reports whether one was
// running, using `duePromptly` — the ONE spelling of "poll this member on the next
// tick", whose whole reason for being shared is that a second spelling of it drifts.
//
// ALREADY DUE IS A SUCCESS. A member whose next check is already coming has asked for
// something that is already true, which is not a mistake and not a state worth an
// error: the boolean says which of the two happened and the write is idempotent
// either way. That also makes a double press — or a retry of a request whose response
// was lost — harmless.
//
// IT REFUSES TWO STATES, and both refusals exist to avoid reporting a check that
// provably will not happen:
//
//   - A session Zalo no longer accepts, or an account that has been withdrawn. The
//     fleet read admits only `connected`, so clearing the backoff on either would
//     leave the member watching for a check no tick will ever make. The remedy is a
//     human with a phone, and saying so is more use than a cheerful boolean.
//   - AN ACCOUNT WHOSE CONVERSATIONS NOBODY HAS CHOSEN YET, which is the case worth
//     arguing rather than asserting. The tick enumerates only members with capture
//     armed, so "check now" for an unarmed one is a promise of a check that cannot
//     occur — and what such a member actually needs to hear is that nothing is
//     captured until they choose, which is a different sentence entirely. The
//     refusal also denies nothing that would have worked: only a turn writes a
//     backoff and only an armed member takes a turn, so an unarmed member is never
//     inside one in the first place.
func bringCheckForward(ctx context.Context, tx extension.Tx, member string) (bool, error) {
	before, err := connectionOf(ctx, tx, member)
	switch {
	case err != nil:
		return false, err
	case before == nil:
		// ErrNotFound, the same answer choosing conversations gives a member with
		// no connection: there is no schedule to bring forward, and existence is
		// not something this operation confirms or denies for anybody else.
		return false, fmt.Errorf("%w: this person has not connected a Zalo account, so there is no check to bring forward", extension.ErrNotFound)
	case before.Status != statusConnected:
		return false, fmt.Errorf("%w: this person's Zalo connection is %q, and no scheduled check visits one — it has to be connected again with a QR scan first",
			extension.ErrInvalid, before.Status)
	case !before.CaptureEnabled:
		return false, fmt.Errorf("%w: nothing from this person's Zalo is being captured yet, so there is no check to bring forward — they choose which conversations go in first",
			extension.ErrInvalid)
	}
	// NextCheckAfter is the DATABASE's answer to "is this member waiting, and until
	// when" — a poll_after already in the past reads as absent — so the boolean is
	// the state the row was in rather than this process comparing a timestamp
	// against its own clock.
	waiting := before.NextCheckAfter != ""
	// NO LEDGER ROW AND NO EVENT, for the reason recordTurn declines one on a turn
	// that moved nothing: this changes when a schedule runs and not what this
	// installation may read. A row per press would put one field-history entry over
	// a scheduling counter that is deliberately off the audit image already
	// (connection.IdleStreak), and say nothing a later reader could act on.
	//
	// The VERSION still moves, because the row did. A turn's own bookkeeping is
	// written under `version = <what the fleet read saw>`, so bumping it is what
	// makes a drain that finished after this press decline to re-apply the backoff
	// the member just cleared.
	if _, err := tx.Exec(ctx,
		`UPDATE `+connectionTable+`
		    SET `+duePromptly+`, version = version + 1, updated_at = now()
		  WHERE user_id = $1::uuid`, member); err != nil {
		return false, err
	}
	return waiting, nil
}
