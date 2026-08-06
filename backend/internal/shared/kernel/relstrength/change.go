// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relstrength

// What CHANGED about a relationship, as opposed to what it currently is.
//
// The score is a pure function recomputed on every read, and the interaction
// projection stores only the present state. So the system can say "warm, 73"
// and cannot say "it went warm on Tuesday" — nothing anywhere remembers
// yesterday's number. That missing sentence is what a person view actually
// opens on: "replied after 41 quiet days" is a reason to act, and "warm" is a
// description.
//
// Nothing is stored to fix that. Every change below is recovered from data the
// system keeps permanently — the person's own interactions — by folding the
// SAME §4 curve over a window that ends in the past. Two consequences worth
// stating, because both are why this is a computation and not a table:
// erasing a source activity makes the derived change disappear with it, and a
// threshold retuned here applies to history rather than only to what happens
// next.
//
// What it deliberately cannot do is answer "everyone who went cold this week"
// without walking every person. That feed is ADR-0078's GRAPH-RISK-1 and has
// its own design; if it is built, this is worth revisiting with a real
// workload behind it.

import "time"

// The thresholds a change clears before it is worth putting in front of
// somebody. They are product decisions and live beside the §4 tunables so
// every surface that reports a change agrees on what counts as one.
const (
	// ReplyGapDays is how long a conversation has to have been silent before
	// a reply is news rather than a normal turn in an ongoing thread.
	ReplyGapDays = 30
	// QuietDays is how long since the last touch before an active
	// relationship is worth flagging as having gone quiet. Longer than
	// ReplyGapDays on purpose: a gap that has already ended is evidence, and
	// one that is still running is only a suspicion.
	QuietDays = 45
	// ComparisonDays is how far back the "and what was it before?" window
	// ends. One half-life, so a change big enough to cross a band over that
	// span is a real move rather than decay arithmetic.
	ComparisonDays = HalfLifeDays
)

// Change kinds. Closed, because each one is a sentence the interface writes
// and an unknown kind would render as nothing.
const (
	// ChangeRepliedAfterGap: they answered after a long silence. The
	// strongest buy-signal this data can produce without any external source.
	ChangeRepliedAfterGap = "replied_after_gap"
	// ChangeWentQuiet: an established relationship has stopped.
	ChangeWentQuiet = "went_quiet"
	// ChangeWarmed / ChangeCooled: the §4 band moved across the comparison
	// window in one direction or the other.
	ChangeWarmed = "warmed"
	ChangeCooled = "cooled"
)

// Change is one thing that happened to a relationship, with the evidence for
// it. Every field is a fact the caller can render without asking a second
// question — a change a reader has to go and verify is not worth showing.
type Change struct {
	Kind string
	// At is when the change happened: the reply's own timestamp for a reply
	// after a gap, the last touch for a relationship that went quiet, and the
	// present for a band that moved (a band move is observed, not dated).
	At time.Time
	// Days is the span the change is about — the silence a reply broke, or
	// how long a quiet relationship has been quiet. Zero for a band move.
	Days int
	// FromBucket and ToBucket are set only for a band move, and name §4 bands.
	FromBucket string
	ToBucket   string
}

// ChangeInputs are the counted facts the derivation needs. As with Inputs, the
// caller decides which interactions counted; this package does not re-ask.
type ChangeInputs struct {
	// Current is the same input set the present score folds, so a change and
	// the number it explains cannot disagree.
	Current Inputs
	// Previous is that same fold over the window ending ComparisonDays ago.
	// Its LastInteraction must be the last interaction BEFORE that instant,
	// not the overall one, or the earlier score inherits today's recency and
	// no band ever appears to move.
	Previous Inputs
	// LatestInbound is when they last wrote to us; nil if they never have.
	LatestInbound *time.Time
	// PrecedingInteraction is the last interaction of any direction strictly
	// before LatestInbound — the far side of the silence that reply broke.
	// Nil when their reply is the first interaction on record, which is a
	// first contact rather than a return.
	PrecedingInteraction *time.Time
}

// Changes returns what has happened to this relationship, most consequential
// first.
//
// It is pure for the same reason Compute is: the caller passes `now`, so a
// test states an instant instead of arranging for one.
func Changes(in ChangeInputs, now time.Time) []Change {
	var out []Change
	if c, ok := repliedAfterGap(in, now); ok {
		out = append(out, c)
	}
	if c, ok := wentQuiet(in.Current.LastInteraction, now); ok {
		out = append(out, c)
	}
	if c, ok := bandMoved(in, now); ok {
		out = append(out, c)
	}
	return out
}

// repliedAfterGap reports a reply that ended a long silence.
//
// The gap is measured to the interaction BEFORE the reply, not to the reply
// itself: what makes it news is the silence it broke, and a reply two months
// ago that ended a two-day lull is not news however old it is.
func repliedAfterGap(in ChangeInputs, now time.Time) (Change, bool) {
	if in.LatestInbound == nil || in.PrecedingInteraction == nil {
		return Change{}, false
	}
	gap := int(in.LatestInbound.Sub(*in.PrecedingInteraction).Hours() / 24)
	if gap < ReplyGapDays {
		return Change{}, false
	}
	// A reply that itself went quiet afterwards is not the headline any more —
	// the silence since is. Bounding it by the same window the score folds
	// keeps the two telling one story.
	if now.Sub(*in.LatestInbound).Hours()/24 > WindowDays {
		return Change{}, false
	}
	return Change{Kind: ChangeRepliedAfterGap, At: *in.LatestInbound, Days: gap}, true
}

// wentQuiet reports an established relationship that has stopped.
//
// "Established" is doing the work: a contact nobody has ever spoken to has not
// gone quiet, they were never loud, and saying otherwise turns every dormant
// record into an alert.
func wentQuiet(lastInteraction *time.Time, now time.Time) (Change, bool) {
	if lastInteraction == nil {
		return Change{}, false
	}
	days := int(now.Sub(*lastInteraction).Hours() / 24)
	if days < QuietDays {
		return Change{}, false
	}
	return Change{Kind: ChangeWentQuiet, At: *lastInteraction, Days: days}, true
}

// bandMoved reports the §4 band crossing between the comparison window and
// now.
//
// Only a BAND change is reported, never a point difference. The score decays
// continuously, so "73 became 71" is arithmetic rather than news, and a
// surface that reported it would cry wolf on every read.
func bandMoved(in ChangeInputs, now time.Time) (Change, bool) {
	if in.Current.LastInteraction == nil || in.Previous.LastInteraction == nil {
		return Change{}, false
	}
	then := Compute(in.Previous, now.AddDate(0, 0, -ComparisonDays))
	current := Compute(in.Current, now)
	if then.Bucket == current.Bucket {
		return Change{}, false
	}
	kind := ChangeCooled
	if current.Strength > then.Strength {
		kind = ChangeWarmed
	}
	return Change{Kind: kind, At: now, FromBucket: then.Bucket, ToBucket: current.Bucket}, true
}
