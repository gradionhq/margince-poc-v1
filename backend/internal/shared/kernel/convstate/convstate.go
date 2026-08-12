// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package convstate answers where a correspondence stands, on one axis, at a
// stated moment.
//
// A drafter is handed message timestamps and nothing else, which is not enough
// to write a truthful sentence about time: two days and eight months look the
// same to a model reading two dates, and the difference decides whether "as
// discussed" is a courtesy or a fabrication. This package turns the raw
// timestamps into the one fact a draft actually needs — how long the silence
// has run, and therefore what may be assumed about shared memory.
//
// It lives in the shared tier because the surfaces that need it sit in
// different places: the timeline module renders a floor draft with no model
// available, the signals module renders a warm-intro draft, and the
// composition layer runs the model drafters. A module may not import a
// sibling, so shared is the only tier all three can reach
// (specs/subsystems/drafting.md, "Where it lives").
//
// The bands are pinned by DRAFT-AC-E-3 and DRAFT-AC-E-4: at BandNone a draft
// may imply no earlier contact, and at BandWeeks or BandMonths it may not
// assume the other party remembers an earlier exchange.
package convstate

import "time"

// Band is where a correspondence stands. Closed, because each value is a
// distinct set of sentences a draft may write, and an unrecognized band would
// have to fall back to one of these anyway — silently, and probably to the
// wrong one.
type Band string

const (
	// BandNone: no prior correspondence with this person at all. A first
	// touch. The band that most needs naming, because every "just following
	// up" a drafter reaches for by reflex is false here.
	BandNone Band = "none"
	// BandFresh: the exchange is live. Shared memory can be assumed and the
	// gap needs no acknowledgement.
	BandFresh Band = "fresh"
	// BandWeeks: long enough that the other party has been doing other things,
	// short enough that the thread is still recognizable to them.
	BandWeeks Band = "weeks"
	// BandMonths: long enough that assuming they remember the exchange is a
	// claim about them rather than a fact.
	BandMonths Band = "months"
)

// The boundaries between the bands, in days of silence. Product decisions, so
// they sit here beside the band they define rather than in a caller.
const (
	// FreshMaxDays: within a working rhythm - a reply this week is a turn in
	// an ongoing conversation.
	FreshMaxDays = 7
	// WeeksMaxDays: roughly a quarter of a year. Past this, an unacknowledged
	// gap reads as not having noticed one.
	WeeksMaxDays = 90
)

// Direction is who spoke last. It changes what a draft owes: after an inbound
// message the draft is an answer, after an outbound one it is a second
// approach into silence, which is a different thing to write.
type Direction string

const (
	// DirectionNone: nothing has been exchanged.
	DirectionNone Direction = "none"
	// DirectionInbound: they wrote last.
	DirectionInbound Direction = "inbound"
	// DirectionOutbound: we wrote last, and have not been answered.
	DirectionOutbound Direction = "outbound"
)

// State is the whole axis: the band, the silence that produced it, and who
// spoke last.
type State struct {
	Band Band
	// SilenceDays is whole days since the last message in either direction.
	// Zero at BandNone, where there is no last message to count from.
	SilenceDays int
	// LastDirection is who sent that last message.
	LastDirection Direction
}

// Classify folds the two timestamps into the axis.
//
// lastIn and lastOut are the most recent inbound and outbound messages, each
// zero when there is none. The clock is passed rather than read so callers
// inject their own; this package holds no clock of its own by design (the
// house pattern - see compose.Dispatcher).
//
// A timestamp in the future is treated as now rather than as negative silence.
// Clock skew between a mail host and this one is ordinary, and the honest
// reading of "the last message is dated tomorrow" is that it just arrived.
func Classify(now, lastIn, lastOut time.Time) State {
	last := lastIn
	direction := DirectionInbound
	if last.IsZero() || (!lastOut.IsZero() && lastOut.After(last)) {
		last = lastOut
		direction = DirectionOutbound
	}
	if last.IsZero() {
		return State{Band: BandNone, SilenceDays: 0, LastDirection: DirectionNone}
	}

	days := wholeDaysBetween(last, now)
	return State{Band: bandForDays(days), SilenceDays: days, LastDirection: direction}
}

// bandForDays maps a silence to its band. Separate from Classify so the
// boundary arithmetic is testable without constructing timestamps around it.
func bandForDays(days int) Band {
	switch {
	case days <= FreshMaxDays:
		return BandFresh
	case days <= WeeksMaxDays:
		return BandWeeks
	default:
		return BandMonths
	}
}

// wholeDaysBetween counts elapsed days, floored, and never returns a negative.
func wholeDaysBetween(from, to time.Time) int {
	if !to.After(from) {
		return 0
	}
	return int(to.Sub(from).Hours() / 24)
}

// AssumesSharedMemory reports whether a draft in this state may write as if
// the other party remembers the earlier exchange. False at BandNone because
// there is no exchange, and false at BandWeeks and BandMonths because enough
// has happened to them since (DRAFT-AC-E-4).
func (s State) AssumesSharedMemory() bool {
	return s.Band == BandFresh
}

// ImpliesPriorContact reports whether a draft may refer to any earlier contact
// with this person at all. False only at BandNone, which is exactly where a
// follow-up subject or a reply prefix would be a fabrication (DRAFT-AC-E-3).
func (s State) ImpliesPriorContact() bool {
	return s.Band != BandNone
}
