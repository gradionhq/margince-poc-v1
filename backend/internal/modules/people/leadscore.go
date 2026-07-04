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

// HIGH_INTENT_SOURCES / LOW_INTENT_SOURCES defaults.
var (
	highIntentSources = map[string]bool{"inbound": true, "webform": true, "referral": true}
	lowIntentSources  = map[string]bool{"import": true, "crawl": true}
)

// BehavioralSignal is one lead-linked engagement event; Kind names the
// §3.1 base-point row.
type BehavioralSignal struct {
	Kind       string // reply | meeting_held | meeting_booked | link_click | email_open
	OccurredAt time.Time
	ActivityID ids.UUID
}

// ScoreFactor is one Explain-This-Score row; the breakdown sums to the
// returned score before clamping and rounding at the end.
type ScoreFactor struct {
	Factor            string     `json:"factor"`
	Points            float64    `json:"points"`
	SourceActivityIDs []ids.UUID `json:"source_activity_ids,omitempty"`
}

var behavioralBasePoints = map[string]float64{
	"reply":          behavioralReplyPoints,
	"meeting_held":   behavioralMeetingHeld,
	"meeting_booked": behavioralMeetingBooked,
	"link_click":     behavioralLinkClickPoints,
	"email_open":     behavioralEmailOpenPoints,
}

// ScoreLead computes §3.1 at one instant. An unknown signal kind
// contributes zero — column-readiness degradation, not an error.
func ScoreLead(title, source string, signals []BehavioralSignal, now time.Time) (int, []ScoreFactor) {
	var factors []ScoreFactor

	if title != "" && decisionMakerTitle.MatchString(title) {
		factors = append(factors, ScoreFactor{Factor: "decision_maker_title", Points: fitDecisionMakerPoints})
	}
	switch {
	case highIntentSources[source]:
		factors = append(factors, ScoreFactor{Factor: "high_intent_source", Points: fitHighIntentSourcePoints})
	case lowIntentSources[source]:
		factors = append(factors, ScoreFactor{Factor: "low_intent_source", Points: fitLowIntentSourcePenalty})
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
			factors = append(factors, ScoreFactor{Factor: signal.Kind})
		}
		factors[ix].Points += decayed
		factors[ix].SourceActivityIDs = append(factors[ix].SourceActivityIDs, signal.ActivityID)
	}

	var sum float64
	for _, f := range factors {
		sum += f.Points
	}
	score := int(math.Floor(sum + 0.5)) // round half-up per the worked example
	if score < 0 {
		score = 0
	}
	if score > leadScoreMax {
		score = leadScoreMax
	}
	return score, factors
}
