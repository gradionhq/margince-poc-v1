// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The member's own verdicts about their own counterparties, and the two
// operations they drive from the screen: read the roster with the current
// verdicts against it, and save a new set.
//
// WHY THIS IS THE CENTRE OF THE UNIT RATHER THAN A PREFERENCE SCREEN. The
// credential a member deposits reaches their entire personal chat life, so the
// only honest default is to capture nothing and the only honest authority for
// changing that is the member themselves. This file is where that decision is
// recorded, and connection.capture_enabled — which the scheduled capture refuses
// to open a socket without — is turned on HERE and nowhere else. One writer, so
// there is one place to look when asking how an installation came to be reading
// somebody's messages.
//
// AS EVERYWHERE IN THIS UNIT, the member is rt.Caller().UserID. Neither
// operation declares a member argument and the strict decoder would refuse one:
// a holder of this unit's RBAC object must not be able to choose which of a
// colleague's conversations this installation reads.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// maxSavedEntries bounds one save.
//
// It is a guard on what ONE request may cause this installation to write, not a
// statement about how many people a rep may know: every entry commits a ledger
// row, because which counterparties a member allowed is the record of their
// consent and not bookkeeping to be summarised. A roster far past this is a
// screen that should page, and a save far past it is more likely a client bug
// than a person's decision.
const maxSavedEntries = 500

// contactView is one row of the chooser: a person, and what this member has
// decided about them.
type contactView struct {
	ChannelUserID string `json:"channel_user_id"`
	DisplayName   string `json:"display_name,omitempty"`
	AvatarURL     string `json:"avatar_url,omitempty"`
	Mode          string `json:"mode"`
}

// contacts answers the calling member's own Zalo roster with their current
// verdicts against it.
//
// THE ROSTER IS ENRICHMENT AND THE STORED VERDICTS ARE THE TRUTH. A roster call
// that fails degrades to the entries already saved rather than failing the
// screen, because a member must always be able to see and change what they
// already chose — a chooser that goes blank when the provider is unreachable
// takes away the one control this unit promises them, at exactly the moment they
// may be reaching for it.
func contacts(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	member, err := connectingMember(rt)
	if err != nil {
		return nil, err
	}
	return contactsVia(ctx, rt, member, openSession)
}

func contactsVia(ctx context.Context, rt extension.Runtime, member extension.UserID,
	open openFunc,
) (json.RawMessage, error) {
	var stored []allowEntry
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		stored, err = verdictsOf(ctx, tx, string(member))
		return err
	}); err != nil {
		return nil, err
	}
	// The transaction above is CLOSED before the provider is reached: a socket
	// handshake inside one holds a pooled connection for the length of somebody
	// else's network.
	roster, reachable := rosterOf(ctx, rt, member, open)
	return json.Marshal(struct {
		Contacts []contactView `json:"contacts"`
		Roster   bool          `json:"roster_available"`
	}{
		Contacts: mergeContacts(roster, stored),
		Roster:   reachable,
	})
}

// rosterOf reads the member's own contact list, and reports whether it could.
//
// The boolean is returned instead of an error because the CALLER's answer is the
// same either way — show the stored list — and only the screen's wording differs.
// A roster this unit could not read must not read as an empty roster: "you know
// nobody" and "we could not ask" are different sentences to put in front of
// somebody deciding what to share.
func rosterOf(ctx context.Context, rt extension.Runtime, member extension.UserID,
	open openFunc,
) (map[string]zaloFriend, bool) {
	sealed, err := unsealSession(ctx, rt, member)
	if err != nil {
		return nil, false
	}
	opened, err := open(ctx, sealed)
	if err != nil {
		return nil, false
	}
	friends, err := opened.friends(ctx)
	if err != nil {
		return nil, false
	}
	return friendsByID(friends), true
}

// mergeContacts is the roster joined with the verdicts, and it carries EVERY
// stored verdict whether or not the roster still knows that person.
//
// A counterparty a member allowed and then removed from their Zalo contacts is
// still a counterparty this installation is capturing, so leaving them off the
// screen would hide a live decision behind a provider's own bookkeeping.
func mergeContacts(roster map[string]zaloFriend, stored []allowEntry) []contactView {
	views := make(map[string]contactView, len(roster)+len(stored))
	for id, friend := range roster {
		views[id] = contactView{
			ChannelUserID: id,
			DisplayName:   friend.DisplayName,
			AvatarURL:     friend.Avatar,
			Mode:          string(verdictNone),
		}
	}
	for _, entry := range stored {
		view := views[entry.ChannelUserID]
		view.ChannelUserID = entry.ChannelUserID
		view.Mode = string(entry.Mode)
		if view.DisplayName == "" {
			view.DisplayName = entry.DisplayName
		}
		views[entry.ChannelUserID] = view
	}
	return sortedContacts(views)
}

// sortedContacts puts the list in a STABLE order a screen can page: by the name
// a human reads, then by account id so two people with the same name — or none —
// do not swap places between two reads of the same data.
func sortedContacts(views map[string]contactView) []contactView {
	out := make([]contactView, 0, len(views))
	for _, view := range views {
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := strings.ToLower(out[i].DisplayName), strings.ToLower(out[j].DisplayName)
		if left != right {
			return left < right
		}
		return out[i].ChannelUserID < out[j].ChannelUserID
	})
	return out
}

// savedEntry is one verdict as the member's screen submits it.
type savedEntry struct {
	ChannelUserID string `json:"channel_user_id"`
	Mode          string `json:"mode"`
	// DisplayName is what the screen was showing for this person when the member
	// decided about them, stored so the list still reads as PEOPLE when the
	// provider is unreachable or has forgotten them. It is untrusted remote text
	// that arrived through a client, so it is bounded here and it routes nothing
	// — matching is on the account id.
	DisplayName string `json:"display_name,omitempty"`
}

// saveAllowlist records the member's verdicts and ARMS CAPTURE.
//
// It is the only writer of connection.capture_enabled, and that is the design
// rather than an implementation detail: "capture nothing until the member
// chooses" is kept by there being exactly one act that turns capture on, and it
// being the act of choosing.
func saveAllowlist(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	args, err := extension.DecodeArgs[struct {
		// capture_mode, not `mode`: an entry carries its OWN `mode`, and one
		// document with two differently-scoped fields of the same name is a
		// document somebody reads wrong.
		CaptureMode string       `json:"capture_mode"`
		Entries     []savedEntry `json:"entries"`
	}](in)
	if err != nil {
		return nil, err
	}
	member, err := connectingMember(rt)
	if err != nil {
		return nil, err
	}
	if err := checkSavedMode(args.CaptureMode); err != nil {
		return nil, err
	}
	if err := checkSavedEntries(args.Entries); err != nil {
		return nil, err
	}
	var armed bool
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		if armed, err = armCaptureAndBringForward(ctx, tx, string(member), args.CaptureMode); err != nil {
			return err
		}
		return writeVerdicts(ctx, tx, string(member), args.Entries)
	}); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Saved        int  `json:"saved"`
		CaptureArmed bool `json:"capture_armed"`
	}{Saved: len(args.Entries), CaptureArmed: armed})
}

// checkSavedMode refuses anything that is not one of the two answers.
//
// AN EMPTY MODE IS REFUSED HERE rather than stored, even though the column is
// nullable: NULL is the state of a member who has never saved, and a save is by
// definition somebody answering the question. Letting a save clear the mode would
// give the surface a way to leave capture armed with no rule — which the database
// refuses anyway, so it would be a 500 where a refusal belongs.
func checkSavedMode(mode string) error {
	if mode != captureEveryoneExcept && mode != captureOnlyChosen {
		return fmt.Errorf("%w: %q is not one of the two answers — %q captures every conversation except the ones named here, %q captures only the ones named here",
			extension.ErrInvalid, mode, captureEveryoneExcept, captureOnlyChosen)
	}
	return nil
}

// checkSavedEntries refuses a document the contract's schema describes but a
// verdict table cannot hold, and it refuses a REPEATED counterparty rather than
// letting the last one win: two verdicts for one person in one document is a
// screen bug, and resolving it silently would have this unit pick which of the
// member's two answers about the same human it acted on.
func checkSavedEntries(entries []savedEntry) error {
	// AN EMPTY LIST IS LEGAL, and under everyone_except it is the commonest first
	// save there is: "everything, and I have nobody to leave out yet". The mode is
	// the answer; the list only refines it.
	if len(entries) > maxSavedEntries {
		return fmt.Errorf("%w: this save carries %d entries, over the %d one request may write", extension.ErrInvalid, len(entries), maxSavedEntries)
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := checkSavedEntry(entry); err != nil {
			return err
		}
		if seen[entry.ChannelUserID] {
			return fmt.Errorf("%w: this save names the same Zalo account twice, so it states two different decisions about one person", extension.ErrInvalid)
		}
		seen[entry.ChannelUserID] = true
	}
	return nil
}

func checkSavedEntry(entry savedEntry) error {
	switch {
	case strings.TrimSpace(entry.ChannelUserID) == "":
		return fmt.Errorf("%w: an entry names no Zalo account, so there is nobody it decides about", extension.ErrInvalid)
	case len(entry.ChannelUserID) > extension.MaxChannelUserIDLength:
		return fmt.Errorf("%w: a Zalo account id is %d bytes, over the %d capture will bind", extension.ErrInvalid, len(entry.ChannelUserID), extension.MaxChannelUserIDLength)
	case entry.Mode != string(verdictAllow) && entry.Mode != string(verdictBlock) &&
		entry.Mode != string(verdictNone):
		return fmt.Errorf("%w: %q is not a decision — an entry is %q, %q, or %q to take this person off the list entirely",
			extension.ErrInvalid, entry.Mode, verdictAllow, verdictBlock, verdictNone)
	case utf8.RuneCountInString(entry.DisplayName) > extension.MaxDisplayNameRunes:
		return fmt.Errorf("%w: a display name is over the %d-character cap", extension.ErrInvalid, extension.MaxDisplayNameRunes)
	}
	return nil
}

// armCaptureAndBringForward turns capture on for this member and puts them at the
// front of the next tick, and reports whether this save is what turned capture on.
//
// TWO THINGS, ALWAYS BOTH, and the second is the one it would be easy to make
// conditional. A member who has been quiet for a week is sitting inside a capped
// polling backoff; the moment they arm a conversation they must not wait that
// backoff out before anything appears on the timeline. They would read it as the
// feature not working, and they would be right to. So EVERY save clears the backoff
// — including one that only edits an already-armed list, which is exactly the case
// (a week of blocks, then one allow) where the wait would be longest and the member
// least able to explain it. `duePromptly` is the shared spelling of that clause.
//
// The LEDGER ROW is the conditional part, because it is a claim about consent rather
// than about scheduling: capture being switched on happens once per connection, and
// recording it on every edit would say it happened every time somebody changed their
// mind.
//
// It refuses a member with no connection, and one who has disconnected: choosing
// conversations for an account this installation holds no credential for would
// record a consent that nothing can act on and that the member's own screen
// cannot explain.
func armCaptureAndBringForward(ctx context.Context, tx extension.Tx, member, mode string) (bool, error) {
	before, err := connectionOf(ctx, tx, member)
	switch {
	case err != nil:
		return false, err
	case before == nil:
		return false, fmt.Errorf("%w: this person has not connected a Zalo account, so there are no conversations to choose between", extension.ErrNotFound)
	case before.Status == statusDisconnected:
		return false, fmt.Errorf("%w: this person's Zalo account is disconnected — it has to be connected again before capture can be armed", extension.ErrInvalid)
	}
	after, err := scanConnection(tx.QueryRow(ctx,
		`UPDATE `+connectionTable+`
		    SET capture_enabled = true, capture_mode = $3, `+duePromptly+`,
		        -- THE FLOOR MOVES ONLY WHEN THE MODE CHANGES. Stamping it on every
		        -- save would push it forward each time somebody edited a list, and
		        -- under everyone_except that silently loses the messages between the
		        -- two saves for every conversation with no bookmark yet.
		        --
		        -- Spelled with coalesce and equality rather than IS DISTINCT FROM,
		        -- which reads identically and carries the token FROM: the tree's
		        -- SQL-scope gate parses that as introducing a table name and reports
		        -- this statement as touching one it cannot resolve. The empty string
		        -- stands in for "no mode yet" and no real mode is empty, so a first
		        -- save always stamps.
		        capture_mode_since = CASE WHEN coalesce(`+connectionTable+`.capture_mode, '') = $3
		                                  THEN `+connectionTable+`.capture_mode_since ELSE now() END,
		        version = version + 1, updated_at = now()
		  WHERE user_id = $1::uuid AND version = $2
		 RETURNING `+connectionColumns, member, before.Version, mode).Scan)
	if err != nil {
		if errors.Is(err, extension.ErrNoRows) {
			// The member disconnected — or reconnected a different account —
			// between the read above and this write. Arming capture on the row
			// as it is NOW would undo whatever they just did.
			return false, fmt.Errorf("%w: this connection changed while the list was being saved", extension.ErrConflict)
		}
		return false, err
	}
	if before.CaptureEnabled {
		// Already armed. A ledger row here would say capture was turned on every
		// time a member edited their list.
		return false, nil
	}
	return true, recordConnection(ctx, tx, extension.AuditUpdate, eventCaptureArmed, before, &after)
}

// dropVerdict takes one person off the member's list entirely, which is what a
// search-as-you-type picker does when somebody is removed from it.
//
// IT DOES NOT TOUCH THE READING POSITION, and that is the property the separate
// cursor table buys: a person removed from a list and put back on it resumes where
// capture actually got to, rather than re-offering their whole conversation. When the
// bookmark lived on this row, deleting a verdict silently reset it.
//
// IT DOES RAISE THE CONVERSATION'S FLOOR WHEN THE ROW BEING DELETED IS AN EXCLUSION,
// in this same transaction, and that write is what makes the deletion safe. Removing a
// `block` is a member saying "capture this person from now" — and the row that
// recorded the period they were hidden for is the very row this statement destroys, so
// the instant of the lift has to be recorded before it goes. Without it the whole
// excluded period lands on the next tick, from precisely the window the member had
// decided against. Removing an `allow` raises nothing: that is an inclusion ending,
// not an exclusion lifting, and a conversation that was never excluded carries no
// mark.
//
// A person who was never on the list is not an error: "they are off it" is the
// outcome asked for, and a picker that removes somebody twice has done no harm.
func dropVerdict(ctx context.Context, tx extension.Tx, member string, before *allowEntry) error {
	if before == nil {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM `+allowlistTable+`
		  WHERE user_id = $1::uuid AND channel_user_id = $2`, member, before.ChannelUserID); err != nil {
		return err
	}
	if err := recordVerdictDropped(ctx, tx, before); err != nil {
		return err
	}
	if !exclusionLifted(before, verdictNone) {
		return nil
	}
	return raiseFloor(ctx, tx, member, before.ChannelUserID)
}
