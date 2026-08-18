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

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// verdict is what this member has decided about one counterparty. The absence of
// a decision is a value here rather than a missing map entry, because "nobody
// has said anything about this person" is the case the default-deny rule turns
// on and a caller must be able to name it.
type verdict string

const (
	// verdictAllow is the member having chosen this conversation.
	verdictAllow verdict = "allow"
	// verdictBlock is the member having ruled it out deliberately.
	verdictBlock verdict = "block"
	// verdictNone is no decision at all, which the filter treats exactly as
	// block — the difference matters only to the member's own screen, where it
	// is what "not chosen yet" looks like next to "chosen against".
	verdictNone verdict = "none"
)

// admits reports whether one drained frame may be handed to capture, and names
// the reason when it may not.
//
// THE ORDER IS THE ORDER IN THE DESIGN, and each step is cheaper and more
// certain than the one after it: whose message this is comes from the frame and
// this unit's own send markers, consent from this member's own table, and only
// then does the cursor decide whether this particular message has already
// landed.
//
// ours is the ids the CRM itself transmitted as this member (sentmessage.go).
func admits(frame zaloInbound, allowed map[string]verdict, cursor string, ours map[string]bool) (bool, string) {
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
	// 2. Consent. DEFAULT DENY: a counterparty this member has said nothing
	// about is dropped exactly as one they blocked is. The default is what makes
	// "capture nothing until the member chooses" a mechanism rather than a
	// preference, so the absent case must not fall through to allow.
	//
	// IT APPLIES TO THE OUTGOING DIRECTION TOO, keyed on the same counterparty —
	// frame.counterparty() reads idTo for a message this member sent. A rep
	// messaging somebody they never allowed must not pull that person into the
	// CRM through the outbound door, which would be a hole straight through the
	// consent story opened from the side nobody was watching.
	case allowed[frame.counterparty()] != verdictAllow:
		return false, "not_allowed"
	// 3. Already landed. The cursor holds the highest msgId ingested for this
	// member, so anything at or below it has been decided about.
	case atOrBelow(frame.MsgID, cursor):
		return false, "already_landed"
	}
	return true, ""
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
