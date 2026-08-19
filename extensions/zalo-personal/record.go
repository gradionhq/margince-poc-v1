// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// One drained frame becomes one record the core can land — and, before that,
// the three decisions about whether it may be landed at all.
//
// THE FILTERS ARE HERE, ABOVE THE MAPPING, AND THEY RUN BEFORE ANY INGEST. That
// ordering is the difference between a consent story that is ARCHITECTURAL and
// one that is a cleanup job: a conversation the member did not allow is dropped
// at the wire and never becomes a row somebody has to find and delete. Nothing
// downstream can restore that property — once a record is handed to capture it
// is on a shared timeline, in an audit trail, and in an outbox event.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// verdict is what this member has decided about one counterparty. The absence of
// a decision is a value here rather than a missing map entry, because "nobody
// has said anything about this person" is the case the default-deny rule turns
// on and a caller must be able to name it.
type verdict string

const (
	// verdictAllow is a row of the INCLUSION list: the member named this
	// conversation. Read under captureOnlyChosen and inert under the other mode.
	verdictAllow verdict = "allow"
	// verdictBlock is a row of the EXCLUSION list: the member left this
	// conversation out. Read under captureEveryoneExcept and inert under the other.
	verdictBlock verdict = "block"
	// verdictNone is no decision at all. What it MEANS depends on the mode — not
	// included, or not excluded — and that is exactly what the mode is for.
	verdictNone verdict = "none"
)

// The two ways a member can answer "which of my Zalo conversations go into the CRM?".
//
// THE VALUES ARE THE MEMBER'S OWN WORDS, not the column's. `all`/`custom` would have
// told a reader — human or model — nothing about what either does; `everyone_except`
// and `only_chosen` say it in the same language the screen asks the question in
// ("everyone I talk to, except the people I leave out" / "only the people I choose"),
// which is the right anchor for a contract a person reads before granting this. Which
// stored list each one consults is stated on the constants below, so the mapping onto
// the `allow`/`block` rows is never in doubt.
//
// THERE IS NO DEFAULT, and that is the line between consent and an accident. A member
// with no mode captures NOTHING: the column is NULL until they save one, capture_enabled
// stays false alongside it, and the database refuses to hold "armed with no mode"
// (migration 0002). Choosing captureEveryoneExcept is an informed act by that human —
// a default of it would mean an installation reading somebody's entire personal chat
// life because nobody touched a screen.
const (
	// captureEveryoneExcept captures every conversation except the ones the member
	// left out. Its list is the `block` rows.
	captureEveryoneExcept = "everyone_except"
	// captureOnlyChosen captures only the conversations the member named. Its list
	// is the `allow` rows.
	captureOnlyChosen = "only_chosen"
)

// consent is what one member has decided about capture: which mode they chose, when
// they chose it, and the verdicts that mode reads.
type consent struct {
	// mode is captureEveryoneExcept, captureOnlyChosen, or empty for a member who
	// has not chosen. Empty captures nothing.
	mode string
	// since is when this mode was chosen. It is the MODE's floor under
	// everyone_except — see admitsUnderMode.
	since time.Time
	// verdicts is the member's own list, keyed by counterparty.
	verdicts map[string]verdict
	// floors is the member's PER-CONVERSATION floor: for a conversation they once
	// explicitly excluded, the instant that exclusion was lifted. A conversation
	// absent from it was never excluded and carries no floor, which is what lets a
	// newly named one collect its whole backlog. See floor.go for why an exclusion
	// leaves a mark and a mode switch does not.
	floors map[string]time.Time
}

// captures reports whether this consent could admit anything at all, which is what
// lets the tick decide not to open a socket. Under everyone_except the answer is
// always yes; under only_chosen it is yes only if some conversation is included.
func (c consent) captures() bool {
	switch c.mode {
	case captureEveryoneExcept:
		return true
	case captureOnlyChosen:
		for _, mode := range c.verdicts {
			if mode == verdictAllow {
				return true
			}
		}
	}
	return false
}

// admits reports whether one drained frame may be handed to capture, and names
// the reason when it may not.
//
// THE ORDER IS THE ORDER IN THE DESIGN, and each step is cheaper and more
// certain than the one after it: whose message this is comes from the frame and
// this unit's own send markers, consent from this member's own choice and list, and
// only then does the bookmark decide whether this particular message has already
// landed.
//
// ours is the ids the CRM itself transmitted as this member (sentmessage.go).
func admits(frame zaloInbound, by consent, mark bookmark, ours map[string]bool) (bool, string) {
	switch {
	// 1. WHOSE OUTGOING MESSAGE IS THIS. A message this member sent comes back
	// as an ordinary inbound frame carrying the SAME msgId, so this cannot be a
	// dedupe-on-id job either way — it is a direction test, and then a question
	// about which of two very different things the echo is.
	//
	// A REPLY THE CRM STAGED is already on the timeline as an activity the core
	// wrote, so capturing its echo would post the rep's words to the customer
	// twice. A REPLY THE REP TYPED ON THEIR PHONE has been seen by nothing here,
	// and dropping it leaves the customer's half of the conversation on the
	// timeline and the rep's half nowhere — for the usage this unit's own consent
	// copy recommends. Only the marker separates them.
	case frame.selfSent() && ours[frame.MsgID]:
		return false, "own_send_already_recorded"
	// 2. THE MEMBER'S OWN CHOICE, read through the mode they chose. This is the
	// consent boundary of the whole unit and inverting either arm of it is the worst
	// defect this code can have, which is why it is one function with one caller and
	// its own adversarial tests.
	//
	// IT APPLIES TO THE OUTGOING DIRECTION TOO, keyed on the same counterparty —
	// frame.counterparty() reads idTo for a message this member sent. A rep
	// messaging somebody must not pull that person into the CRM through the outbound
	// door under only_chosen, and must under everyone_except unless they are
	// excluded. That is a hole that would otherwise open from the side nobody
	// watches.
	case !admitsUnderMode(frame, by, mark):
		return false, refusalUnder(by.mode)
	// 3. Already landed. The bookmark holds the highest msgId ingested for THIS
	// conversation, so anything at or below it has been decided about.
	case atOrBelow(frame.MsgID, mark.at):
		return false, "already_landed"
	}
	return true, ""
}

// admitsUnderMode is the mode's own arm of the filter, and the two arms are NOT
// mirror images — the asymmetry is deliberate and is the mode-switch decision.
//
// UNDER only_chosen the member named this conversation, so its whole queued history
// is what they asked for: THE MODE CARRIES NO FLOOR, and a conversation included
// today collects the messages Zalo is still holding for it. That is the promise the
// inclusion list exists to keep.
//
// UNDER everyone_except the member named NOBODY. Reaching back through Zalo's queue
// would sweep in conversations they had looked at and deliberately left out under a
// previous mode, and "the CRM captured my doctor" is exactly the outcome this unit is
// built to prevent. So a conversation with NO BOOKMARK — one nothing has ever been
// captured from — is captured from the moment the mode was chosen FORWARD, and older
// messages still sitting in the queue are not reached for.
//
// The cost of that floor is at most the retention window's worth of history for
// conversations nobody has ever mentioned, and only on the tick after the switch.
// The cost of not having it is capturing a conversation the member had already
// decided against. A member who wants one of those conversations back can name it,
// which is the inclusion list — and naming it is what removes the MODE's floor.
//
// WHAT NAMING SOMEBODY DOES NOT REMOVE IS THEIR OWN CONVERSATION'S FLOOR, and that is
// the second, per-conversation floor both arms consult. The mode's floor is a guess
// about conversations nobody ever decided about; a conversation's own floor is the
// residue of a decision the member DID make about that one person, and no later act
// retroactively admits the period it covers. An explicit exclusion leaves a mark; a
// conversation that was never excluded carries none — floor.go argues both halves.
//
// A conversation whose bookmark WAS WRITTEN UNDER THE FLOOR IN FORCE is past the
// question: capture has been reading it, and where it got to is what the bookmark
// says. A bookmark written EARLIER is not, and the difference is what stops a round
// trip from walking through the floor — everyone_except reads a conversation to some
// id, only_chosen refuses everything above it while leaving the bookmark standing,
// and coming back to everyone_except re-stamps the floor onto a conversation that
// still has one. Reading its mere presence would then hand over the whole excluded
// period.
func admitsUnderMode(frame zaloInbound, by consent, mark bookmark) bool {
	other := frame.counterparty()
	switch by.mode {
	case captureOnlyChosen:
		if by.verdicts[other] != verdictAllow {
			return false
		}
		// The MODE has no floor here; the conversation may still have one of its own.
		return above(frame, mark, by.floors[other])
	case captureEveryoneExcept:
		if by.verdicts[other] == verdictBlock {
			return false
		}
		// WHICHEVER FLOOR IS LATER. They answer different questions — "when did this
		// member last widen the rule for everybody" and "when did they stop hiding
		// this one person" — and a message has to clear both, so taking the earlier
		// of the two would let one answer overrule the other.
		return above(frame, mark, later(by.since, by.floors[other]))
	}
	// A mode this unit does not recognise — including none at all — captures nothing.
	// The database refuses "armed with no mode", so this is unreachable through any
	// writer; it is here because the safe direction for an unreachable branch in a
	// consent filter is deny, not allow.
	return false
}

// above is the floor test both modes share, once the mode has decided that this
// conversation is admissible at all.
//
// A ZERO FLOOR ADMITS EVERYTHING, and that is the state of a conversation nothing has
// ever narrowed: no mode floor, no exclusion ever lifted. It is checked first so the
// ordinary only_chosen case never asks a question about time.
func above(frame zaloInbound, mark bookmark, floor time.Time) bool {
	if floor.IsZero() {
		return true
	}
	if mark.postdates(floor) {
		// Capture has been reading this conversation SINCE the floor was set, so the
		// bookmark — not the floor — is what says where it got to.
		return true
	}
	// No bookmark from after the floor: nothing has been captured from this
	// conversation since the member last narrowed their answer about it, so the floor
	// applies. A frame with no readable time is refused later by representable, and
	// treating it as below the floor here keeps this arm from being the one that
	// decides a malformed frame's fate.
	return frame.OccurredAt.After(floor)
}

// later is whichever of two instants is the later one, and a zero time loses to any
// real one — which is what makes "this conversation has no floor of its own" and "this
// member has no mode floor" the same absent value rather than two special cases.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// refusalUnder names why the mode declined, in the mode's own vocabulary, so a
// diagnostic says "not included" or "excluded" rather than one word for both.
func refusalUnder(mode string) string {
	switch mode {
	case captureEveryoneExcept:
		return "excluded_or_before_the_mode"
	case captureOnlyChosen:
		return "not_included"
	}
	return "no_mode_chosen"
}

// orderable reads one provider message id as the NUMBER it is, and reports whether
// it is one at all.
//
// The ids arrive as decimal strings and are stored as text, because their shape is
// the provider's to change. Comparing the text directly would order "999" above
// "1000", which on a rolling id would park a conversation's cursor and drop
// everything after it.
func orderable(msgID string) (uint64, bool) {
	at, err := strconv.ParseUint(msgID, 10, 64)
	return at, err == nil
}

// atOrBelow reports that this message is at or below a conversation's bookmark, and
// therefore already decided about.
//
// AN ID NEITHER SIDE CAN ORDER IS TREATED AS UNSEEN, which is the safe direction
// for a COMPARISON: read as seen it loses the message permanently, read as unseen it
// costs one replay that capture deduplicates on the natural key. It is NOT a safe
// value to STORE, which is why representable refuses such a frame before it can ever
// become a cursor and why higher below will not return one.
func atOrBelow(msgID, cursor string) bool {
	if cursor == "" {
		return false
	}
	at, ok := orderable(msgID)
	if !ok {
		return false
	}
	mark, ok := orderable(cursor)
	if !ok {
		return false
	}
	return at <= mark
}

// earlier is a STRICT ordering on two provider ids, for sorting a drain into the
// order the messages happened.
//
// Strict because sort.Slice's `less` must be: `at <= mark` answers true for a value
// against itself, which is not a valid ordering and is the kind of thing that works
// until two frames share an id.
func earlier(a, b string) bool {
	left, leftOK := orderable(a)
	right, rightOK := orderable(b)
	if !leftOK || !rightOK {
		// Nothing to order by. Both answers are false, which sort reads as "these
		// are equivalent" and leaves their relative order alone.
		return false
	}
	return left < right
}

// higher answers whichever of two provider ids is the later message, so a turn can
// advance a cursor without assuming the drain handed frames back in order.
//
// IT NEVER RETURNS A VALUE IT COULD NOT ORDER, and that guard is what stops a
// single novel or hostile id from poisoning a cursor permanently. Stored, such a
// value sits above and below nothing: every numeric id then reads as unseen, so the
// whole conversation is re-offered on every tick forever and the bookmark stops
// working. When the candidate will not parse the existing cursor is kept, and when
// the existing cursor will not parse — which nothing in this unit can now write, but
// a hand-edited row could — the parseable candidate replaces it.
func higher(cursor, candidate string) string {
	_, candidateOK := orderable(candidate)
	if !candidateOK {
		return cursor
	}
	if _, cursorOK := orderable(cursor); !cursorOK {
		return candidate
	}
	if atOrBelow(candidate, cursor) {
		return cursor
	}
	return candidate
}

// recordFor maps one frame onto the record capture lands, in EITHER direction.
//
// memberUID is the connected account's own Zalo id. It namespaces both keys, and
// that is not tidiness: this unit's records share ONE provenance namespace across
// every member, so two reps whose counterparties number a message identically
// would collide on the natural key and land one conversation as the other's.
//
// names is the display name this unit knows for a counterparty, from the roster
// and from the member's own saved verdicts. It exists as a separate map rather
// than being read off the frame because of the trap documented on
// counterpartyOf — on an outgoing frame the frame's own name is the MEMBER's.
func recordFor(frame zaloInbound, memberUID string, names map[string]string) (extension.Record, error) {
	if err := representable(frame, memberUID); err != nil {
		return extension.Record{}, err
	}
	other := frame.counterparty()
	return extension.Record{
		System: ingressSystem,
		// The provider's own id, which is what makes a replay a no-op.
		Key: memberUID + ":" + frame.MsgID,
		Activity: extension.ActivityFields{
			// A message, on this unit's own transport — the two axes stated
			// separately (ADR-0107/A158). The provider is a LITERAL rather than
			// the `provider` constant, for the reason the declaration in New()
			// is one: the manifest is read statically from the AST without
			// compiling the unit, and a test holds the two equal.
			Kind:            extension.ActivityKindMessage,
			ChannelProvider: "zalo_personal",
			// NO SUBJECT. A chat message has none, and inventing one from the
			// first line of the body would put this unit's guess where a
			// timeline reader expects the sender's own words.
			Body: strings.TrimSpace(frame.Content),
			// The provider's time, never the poll's discovery time.
			OccurredAt: frame.OccurredAt,
			// Read from the frame's own direction fields, so a reply the rep
			// typed on their phone lands as theirs rather than as the customer's.
			Direction: directionOf(frame),
		},
		// Namespaced by provider AND by the member's own account. A bare Zalo id
		// would share activity.thread_key with every other source, where two of
		// them can collide and join a stranger's conversation onto this one.
		// The SAME thread in both directions, because counterparty() resolves the
		// other end per direction. Two halves of one conversation on two thread
		// keys is two monologues, and neither of them reads as a conversation.
		ThreadKey:    threadKeyFor(memberUID, other),
		Counterparty: counterpartyOf(frame, other, names),
		// NO ADDRESSES, and it is legal precisely because the counterparty names
		// no address either: Zalo hands out an opaque account id and a display
		// name and no mail anywhere. The seam admits the empty set for exactly
		// this shape — see the note on Counterparty below for what an empty
		// Domain then means.
		Raw: frame.Raw,
	}, nil
}

// threadKeyFor is the conversation key, spelled once so the poll and the mapping
// cannot disagree about which conversation a message belongs to.
func threadKeyFor(memberUID, counterpartyUID string) string {
	return provider + ":" + memberUID + ":" + counterpartyUID
}

// directionOf reads the frame's own direction fields. A message this member sent
// is outbound whichever device they sent it from.
func directionOf(frame zaloInbound) string {
	if frame.selfSent() {
		return extension.DirectionOutbound
	}
	return extension.DirectionInbound
}

// isOrderableMsgID reports whether a provider id is one this unit can bookmark.
func isOrderableMsgID(msgID string) bool {
	_, ok := orderable(msgID)
	return ok
}

// bookmarkable reports whether this turn could record a reading position for this
// frame's conversation at all.
//
// It is the two CHECKs the cursor table states, asked in Go before the batch is
// built: `last_msg_id ~ '^[0-9]+$'` and `length(channel_user_id) > 0`. Asking here
// rather than letting the database answer is not belt-and-braces — the advance is
// ONE multi-row statement, so a value the CHECK refuses does not lose that
// conversation's bookmark, it loses EVERY conversation's bookmark for that turn.
func bookmarkable(counterpartyUID, msgID string) bool {
	return strings.TrimSpace(counterpartyUID) != "" && isOrderableMsgID(msgID)
}

// representable refuses a frame this unit could never land, so the caller can
// tell "will never work" from "did not work this time" — the distinction that
// stops a tick parking forever on one malformed message.
func representable(frame zaloInbound, memberUID string) error {
	switch {
	case memberUID == "":
		return fmt.Errorf("%w: this connection does not say which Zalo account it is, so a record from it could not be keyed", extension.ErrInvalid)
	case strings.TrimSpace(frame.MsgID) == "":
		return fmt.Errorf("%w: a frame with no message id cannot be captured idempotently, so a replay would land a second copy", extension.ErrInvalid)
	case !isOrderableMsgID(frame.MsgID):
		// REFUSED LOUDLY rather than landed and then skipped by the cursor, because
		// this id would BECOME the cursor. A value that will not parse sits above
		// and below nothing, so once stored every numeric id reads as unseen and
		// the conversation is re-offered forever. The msgId is the provider's own
		// stable key and it has been a decimal integer in every frame anybody has
		// captured; one that is not is a shape this unit cannot order, and refusing
		// it with a ledger row beats guessing at it silently.
		return fmt.Errorf("%w: message id %q is not the decimal id this provider has always issued, and an id this unit cannot order cannot be a bookmark", extension.ErrInvalid, frame.MsgID)
	case strings.TrimSpace(frame.counterparty()) == "":
		return fmt.Errorf("%w: message %s names nobody at the other end, so there is no person to attach it to", extension.ErrInvalid, frame.MsgID)
	case frame.OccurredAt.IsZero():
		// Refused LOUDLY rather than defaulted to now(): the capture seam
		// rejects a zero time, and a frame whose own timestamp did not decode is
		// one this unit cannot place on a timeline honestly.
		return fmt.Errorf("%w: message %s carries no readable time of its own", extension.ErrInvalid, frame.MsgID)
	case len(frame.Raw) > extension.MaxRawBytes:
		return fmt.Errorf("%w: the frame for message %s is %d bytes of evidence, over the %d capture keeps per record", extension.ErrInvalid, frame.MsgID, len(frame.Raw), extension.MaxRawBytes)
	}
	return nil
}

// counterpartyOf names the human at the other end, BY CHANNEL ACCOUNT.
//
// The account is what NAMES them and what makes the captured message repliable:
// the core binds (provider, account id) to a person and the reply path resolves
// its recipient from that binding, so a record leaving it empty lands a message
// nobody can answer.
//
// THE TRAP, AND IT IS THE SHARPEST ONE IN THIS UNIT, verified against the real
// captured frames: on an OUTGOING frame `dName` is the MEMBER'S OWN display name,
// not the counterparty's. Passing it through would write the rep's name onto the
// customer's identity — and because the name rides on the ChannelIdentity, the
// core would BIND it there, so every future message from that customer would
// arrive under the rep's name. That is a corrupted person record and nothing
// downstream can tell it is wrong.
//
// So the name comes from what this unit knows about the counterparty — the roster,
// or the member's own saved verdict entry — and where it knows nothing the name is
// left EMPTY. An unnamed counterparty is honest and the core still resolves them
// by account; the alternative is a confident lie.
//
// EMAIL AND DOMAIN STAY EMPTY, and per the seam's own note an empty Domain is
// not opting out of the core's suppression gates — it is FAILING TO ANSWER,
// which those gates read as "keep". That is the correct outcome here: Zalo
// reports no address anywhere, so we genuinely do not know, and inventing a
// domain to satisfy a gate would be worse than silence.
func counterpartyOf(frame zaloInbound, other string, names map[string]string) extension.Counterparty {
	name := strings.TrimSpace(names[other])
	// The frame's own name is usable for an INBOUND message only, where it is the
	// sender's — which is also the case where this unit may know nothing else: a
	// prospect writing for the first time is on no roster and in no verdict list.
	if name == "" && !frame.selfSent() {
		name = strings.TrimSpace(frame.DName)
	}
	return extension.Counterparty{
		DisplayName: name,
		Direction:   directionOf(frame),
		ChannelIdentity: extension.ChannelIdentity{
			Provider:      "zalo_personal",
			ChannelUserID: other,
			DisplayName:   name,
		},
	}
}
