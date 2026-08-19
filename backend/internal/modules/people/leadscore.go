// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Lead scoring (formulas-and-rules §3): a transparent weighted-signal
// model — never trained ML (P6). The score decomposes to factors that
// sum exactly to it ("Explain This Score", AC-S1), decays behavioral
// signals on the one 2^(−t/halflife) primitive, and reads ONLY
// lead-local signals — the contact graph never leaks in.

import (
	"math"
	"regexp"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// §3 tunables (spec parameter registry names in comments).
const (
	leadScoreMax              = 100 // LEADSCORE_MAX
	behavioralHalflifeDays    = 14  // LEADSCORE_BEHAVIORAL_HALFLIFE_DAYS
	fitDecisionMakerPoints    = 15
	fitHighIntentSourcePoints = 8
	fitLowIntentSourcePenalty = -5
	behavioralReplyPoints     = 25
	behavioralMeetingHeld     = 30
	behavioralMeetingBooked   = 20
	behavioralLinkClickPoints = 4
	behavioralEmailOpenPoints = 2
)

var decisionMakerTitle = regexp.MustCompile(`(?i)(chief|vp|head|director|founder|owner|c[a-z]o)\b`)

// BehavioralSignal is one lead-linked engagement event; Kind names the
// §3.1 base-point row.
type BehavioralSignal struct {
	Kind       string // reply | meeting_held | meeting_booked | link_click | email_open
	OccurredAt time.Time
	ActivityID ids.ActivityID
}

// ScoreFactor is one Explain-This-Score row. Points is the contribution
// AFTER decay and BEFORE the rounding and clamping that produce the
// stored score, so factors sum to LeadScoring.RawSum — never, on their
// own, to the score itself.
type ScoreFactor struct {
	Factor            string           `json:"factor"`
	Points            float64          `json:"points"`
	BasePoints        float64          `json:"base_points,omitempty"`
	SourceActivityIDs []ids.ActivityID `json:"source_activity_ids,omitempty"`
}

// LeadScoring is one run of §3.1 with its arithmetic left visible.
// Rounding and clamping are SEPARATE steps and a reader that cannot tell
// them apart misreports the second: raw 45.6 stores 46 with no clamp
// involved, and raw 100.6 rounds to 101 before the cap acts on it, so
// naming 100.6 as what was clamped states something that never happened
// (ADR-0105 §2).
type LeadScoring struct {
	Score      int // RoundedSum bounded to 0..100 — what the record carries
	RoundedSum int // RawSum rounded half-up, before the clamp
	RawSum     float64
	Factors    []ScoreFactor
}

// withManual folds a rep's own inputs into the run and re-derives the
// arithmetic, so the two intermediate values keep describing the total the
// score actually came from. Rounding and clamping run once, over the whole
// breakdown — applying them twice would round a rounded number.
func (s LeadScoring) withManual(manual []ScoreFactor) LeadScoring {
	if len(manual) == 0 {
		return s
	}
	factors := append(append([]ScoreFactor{}, s.Factors...), manual...)
	var sum float64
	for _, f := range factors {
		sum += f.Points
	}
	rounded := int(math.Floor(sum + 0.5))
	score := rounded
	if score < 0 {
		score = 0
	}
	if score > leadScoreMax {
		score = leadScoreMax
	}
	return LeadScoring{Score: score, RoundedSum: rounded, RawSum: sum, Factors: factors}
}

var behavioralBasePoints = map[string]float64{
	"reply":          behavioralReplyPoints,
	"meeting_held":   behavioralMeetingHeld,
	"meeting_booked": behavioralMeetingBooked,
	"link_click":     behavioralLinkClickPoints,
	"email_open":     behavioralEmailOpenPoints,
}

// ScoreLeadDetail computes §3.1 at one instant and reports the score with
// its breakdown and the intermediate arithmetic, for the caller that has to
// persist an explanation rather than a number. An unknown signal kind
// contributes zero — column-readiness degradation, not an error. The
// source's weight arrives resolved (SourceIntents.Of) because the weighting
// is the installation's lead_source table, not a literal here.
func ScoreLeadDetail(title string, intent SourceIntent, signals []BehavioralSignal, now time.Time) LeadScoring {
	var factors []ScoreFactor

	if title != "" && decisionMakerTitle.MatchString(title) {
		factors = append(factors, ScoreFactor{Factor: "decision_maker_title", Points: fitDecisionMakerPoints})
	}
	switch intent {
	case SourceIntentHigh:
		factors = append(factors, ScoreFactor{Factor: "high_intent_source", Points: fitHighIntentSourcePoints})
	case SourceIntentLow:
		factors = append(factors, ScoreFactor{Factor: "low_intent_source", Points: fitLowIntentSourcePenalty})
	case SourceIntentNeutral:
	}

	// One decayed factor per signal KIND, sources aggregated — the
	// breakdown stays readable when a lead has fifty opens. Order is
	// first-seen, so a fixed fixture yields a fixed breakdown.
	perKind := map[string]int{}
	for _, signal := range signals {
		base, known := behavioralBasePoints[signal.Kind]
		if !known {
			continue
		}
		days := now.Sub(signal.OccurredAt).Hours() / 24
		decayed := base * math.Exp2(-days/behavioralHalflifeDays)
		ix, seen := perKind[signal.Kind]
		if !seen {
			ix = len(factors)
			perKind[signal.Kind] = ix
			// BasePoints is the undecayed weight, so a reader can render the
			// decay as arithmetic (`raw · 2^(−days/14)`) rather than being
			// handed a number to take on faith (AC-leads-4/10).
			factors = append(factors, ScoreFactor{Factor: signal.Kind, BasePoints: base})
		}
		factors[ix].Points += decayed
		factors[ix].SourceActivityIDs = append(factors[ix].SourceActivityIDs, signal.ActivityID)
	}

	var sum float64
	for _, f := range factors {
		sum += f.Points
	}
	rounded := int(math.Floor(sum + 0.5)) // round half-up per the worked example
	score := rounded
	if score < 0 {
		score = 0
	}
	if score > leadScoreMax {
		score = leadScoreMax
	}
	return LeadScoring{Score: score, RoundedSum: rounded, RawSum: sum, Factors: factors}
}
