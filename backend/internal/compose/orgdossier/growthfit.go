// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// How well a company fits what we sell, and — first — whether we know enough to
// say (DOSS-FORM-2, DOSS-AC-12/13).
//
// The completeness figure is the load-bearing part, not a caveat printed beside
// a score. A fit read off three facts and a fit read off thirty are different
// claims, and a band alone renders them identically. So the band is computed
// only above a floor of known inputs; below it the answer is `unknown` with the
// missing inputs named, which is a worse-looking answer and a more useful one.

import (
	"context"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// bandRank orders the vocabulary (DOSS-PARAM-8) so a cap can be applied by
// comparison rather than by enumerating the pairs it lowers. `unknown` is
// deliberately the LOWEST: it is an abstention, so a cap can never turn a
// judgment into one, and capping an abstention leaves it alone.
var bandRank = map[crmcontracts.GrowthFitBand]int{
	crmcontracts.GrowthFitBandUnknown:  0,
	crmcontracts.GrowthFitBandWeak:     1,
	crmcontracts.GrowthFitBandModerate: 2,
	crmcontracts.GrowthFitBandStrong:   3,
}

// abstentionFloor is the share of required inputs an assembly must hold before
// it may name a band at all. Below it the answer is `unknown` (DOSS-AC-12).
//
// The spec fixes the floor's BEHAVIOUR and its two worked examples but never
// gives it a number: four of seven populated must judge normally, two of seven
// must abstain. One half is the roundest value satisfying both, and it states
// the rule a reader can hold — we do not grade a company we know less than half
// of. Any change here belongs upstream in DOSS-PARAM, not in this constant.
const abstentionFloor = 0.5

// freshness is how long a machine-read value counts as describing the company
// it was read from (DOSS-PARAM-6). Past it the value is still SHOWN — the
// dossier renders stale facts rather than hiding them — but it no longer counts
// toward the completeness a band is allowed to rest on.
const freshness = 30 * 24 * time.Hour

// The label on each required input is the reader's own words, because it
// becomes the next-step sentence. A person told to go and gather
// `buying_center` has been told nothing.
//
// Both field types are the CONTRACT's, so a required input naming a field the
// contract does not have fails to compile. Spelled as strings, such an input
// would simply never be found — and the completeness figure would then be
// permanently short by one, reported with total confidence.
type (
	requiredProfileInput struct {
		field crmcontracts.CompanyProfileFieldField
		label string
	}
	requiredFactInput struct {
		field crmcontracts.OrganizationFactField
		label string
	}
)

// The seven required inputs, covering the five things a fit is judged on: what
// they offer, the market they serve, their size, the technology they run, and
// who does the buying. The prose halves are profile fields; size and technology
// are extracted facts, because that is where this system records them.
var (
	requiredProfileInputs = []requiredProfileInput{
		{field: crmcontracts.CompanyProfileFieldFieldOfferSummary, label: "what they offer"},
		{field: crmcontracts.CompanyProfileFieldFieldIcp, label: "who they sell to"},
		{field: crmcontracts.CompanyProfileFieldFieldIndustry, label: "their industry"},
		{field: crmcontracts.CompanyProfileFieldFieldBuyingCenter, label: "who does the buying"},
		{field: crmcontracts.CompanyProfileFieldFieldBuyingIntents, label: "what they buy for"},
	}
	requiredFactInputs = []requiredFactInput{
		{field: crmcontracts.OrganizationFactFieldEmployeeRange, label: "how big they are"},
		{field: crmcontracts.OrganizationFactFieldTechnology, label: "the technology they run"},
	}
)

// SelfOffering answers whether this workspace has confirmed what IT sells.
//
// It answers a boolean and not a profile on purpose. The workspace's own
// offering must never reach a citation about another company, and a seam that
// cannot return the offering cannot leak it into one (DOSS-AC-6).
type SelfOffering func(ctx context.Context) (bool, error)

// Completeness counts how many required inputs this assembly actually holds,
// and names the ones it does not (DOSS-FORM-2).
func Completeness(in Input, now time.Time) crmcontracts.DataCompleteness {
	present := 0
	missing := []string{}
	for _, want := range requiredProfileInputs {
		if hasFreshProfileField(in, want.field, now) {
			present++
			continue
		}
		missing = append(missing, want.label)
	}
	for _, want := range requiredFactInputs {
		if hasFreshFact(in, want.field, now) {
			present++
			continue
		}
		missing = append(missing, want.label)
	}
	expected := len(requiredProfileInputs) + len(requiredFactInputs)
	return crmcontracts.DataCompleteness{
		Present:  present,
		Expected: expected,
		Missing:  &missing,
	}
}

// A profile field is unique per company, so the first match settles it. A fact
// is not — a company can carry several `technology` rows — so any one of them
// being present and fresh satisfies the input.
func hasFreshProfileField(in Input, field crmcontracts.CompanyProfileFieldField, now time.Time) bool {
	for _, have := range in.ProfileFields {
		if have.Field != field {
			continue
		}
		return countsAsPresent(have.Value, string(have.Source), have.RetrievedAt, have.UpdatedAt, now)
	}
	return false
}

func hasFreshFact(in Input, field crmcontracts.OrganizationFactField, now time.Time) bool {
	for _, have := range in.Facts {
		if have.Field != field {
			continue
		}
		if countsAsPresent(have.Value, string(have.Source), have.RetrievedAt, have.UpdatedAt, now) {
			return true
		}
	}
	return false
}

// countsAsPresent decides whether one recorded value may hold up a band.
//
// A value a HUMAN gave us never ages out. Staleness here means "the source may
// have changed since we read it", which is a claim about re-reading a website —
// a person who typed the answer did not read anything, and expiring their entry
// would ask them to retype it on a schedule.
//
// A machine-read value with no recorded read time falls back to when the row
// was last written. Rows predate the retrieved_at column, and treating an
// unknown read time as infinitely stale would empty the completeness figure for
// every company captured before it existed.
func countsAsPresent(value, source string, retrievedAt *time.Time, updatedAt, now time.Time) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	if source == sourceHuman {
		return true
	}
	read := updatedAt
	if retrievedAt != nil {
		read = *retrievedAt
	}
	return now.Sub(read) < freshness
}

// sourceHuman is the provenance a person's own answer carries (migration 0099).
const sourceHuman = "human"

// GrowthFitClaims is the reasoning behind a band, in the one claim vocabulary
// every generated surface here uses. Each claim carries the target-side records
// it was written from, so the reader can open the evidence rather than take the
// band on trust.
type GrowthFitClaims struct {
	PositiveFactors  []claims.Sentence `json:"positive_factors,omitempty"`
	NegativeFactors  []claims.Sentence `json:"negative_factors,omitempty"`
	Whitespace       []claims.Sentence `json:"whitespace,omitempty"`
	Objections       []claims.Sentence `json:"objections,omitempty"`
	RecommendedAngle *claims.Sentence  `json:"recommended_angle,omitempty"`
}

// empty reports whether nothing survived the grounding filter.
func (c GrowthFitClaims) empty() bool {
	return len(c.All()) == 0
}

// All flattens every claim into one list. The cert case asks what the whole
// assessment cited, and a question about the assessment should not have to
// know which of five buckets an answer landed in.
func (c GrowthFitClaims) All() []claims.Sentence {
	out := make([]claims.Sentence, 0,
		len(c.PositiveFactors)+len(c.NegativeFactors)+len(c.Whitespace)+len(c.Objections)+1)
	out = append(out, c.PositiveFactors...)
	out = append(out, c.NegativeFactors...)
	out = append(out, c.Whitespace...)
	out = append(out, c.Objections...)
	if c.RecommendedAngle != nil {
		out = append(out, *c.RecommendedAngle)
	}
	return out
}

// JudgeableBandCount is how many bands a model may actually propose —
// everything except the abstention. A cert scenario accepting all of them
// accepts every reply, which the corpus refuses.
const JudgeableBandCount = 3

// BandIsJudgeable reports whether a band is one the MODEL may propose.
// `unknown` is not: abstention is the counting step's verdict, so a scenario
// that accepted it would be certifying a decision the model does not make.
func BandIsJudgeable(band crmcontracts.GrowthFitBand) bool {
	rank, known := bandRank[band]
	return known && rank > bandRank[crmcontracts.GrowthFitBandUnknown]
}

// Assessment is the growth fit before it is dressed for the wire: the band that
// survived both gates, why it could not go higher, what to do next, and — when
// a band was actually reached — the claims behind it.
type Assessment struct {
	Band         crmcontracts.GrowthFitBand
	CappedReason string
	NextStep     string
	Completeness crmcontracts.DataCompleteness
	Claims       GrowthFitClaims
}

// nowFunc is the injected clock. The assessment is stamped and the freshness
// window is measured against it, so a test names the instant rather than racing
// the wall clock.
type nowFunc func() time.Time

// Assess applies DOSS-FORM-2 to one company: count the inputs, abstain below
// the floor, and cap what is left when we have not confirmed our own offering.
//
// `proposed` is the band a writer suggests. The deterministic floor proposes
// `unknown` and therefore always abstains (DOSS-PARAM-7) — it restates recorded
// values and grading is not a restatement. A model lane proposes a real band
// and meets the same two gates, which is the point of them living here rather
// than in either writer.
func Assess(in Input, proposed crmcontracts.GrowthFitBand, selfConfirmed bool, now time.Time) Assessment {
	completeness := Completeness(in, now)
	out := Assessment{Completeness: completeness}

	if !aboveFloor(completeness) {
		// The floor overrides whatever the facts suggested. Nothing was
		// "capped" — the assembly declined to judge — so the reader is given
		// the gap to close instead of a reason a number is lower than it looks.
		out.Band = crmcontracts.GrowthFitBandUnknown
		out.NextStep = gatherNextStep(completeness)
		return out
	}

	out.Band = proposed
	if proposed == crmcontracts.GrowthFitBandUnknown {
		// An abstention with the facts all present did not come from the
		// counting — nothing was missing. It came from having no writer: no
		// model lane is configured, or the one configured failed.
		//
		// It still needs a reason. `unknown` means "we could not tell", and a
		// reader given that with nothing beside it cannot distinguish it from
		// "a poor fit" — which is the opposite conclusion, and the single
		// misreading this band exists to prevent (DOSS-AC-12).
		out.NextStep = "the facts needed are all recorded, but nothing is configured to " +
			"judge them — ask an administrator to configure the AI model"
		return out
	}
	if !selfConfirmed && bandRank[proposed] > bandRank[crmcontracts.GrowthFitBandModerate] {
		out.Band = crmcontracts.GrowthFitBandModerate
		out.CappedReason = "we have not confirmed what this workspace itself sells, " +
			"so a stronger fit than moderate cannot be justified"
	}
	if !selfConfirmed {
		out.NextStep = "confirm your own company profile, so a fit is measured against " +
			"what you actually sell rather than a guess"
	}
	return out
}

// aboveFloor compares in integers. The proportion is a ratio of two counts, and
// evaluating it as one avoids a float comparison deciding a boundary case —
// four of eight is exactly the floor and passes, three of eight does not.
func aboveFloor(c crmcontracts.DataCompleteness) bool {
	if c.Expected <= 0 {
		// An assembly that wants nothing cannot be complete enough to judge on.
		// This is unreachable while the required set is a non-empty literal, and
		// it abstains rather than dividing by zero if that ever changes.
		return false
	}
	return float64(c.Present) >= abstentionFloor*float64(c.Expected)
}

// gatherNextStep turns the missing inputs into the one sentence DOSS-AC-12 asks
// for: a named thing to go and find, not a restatement that data is missing.
func gatherNextStep(c crmcontracts.DataCompleteness) string {
	if c.Missing == nil || len(*c.Missing) == 0 {
		// Below the floor with nothing named is a contradiction the counting
		// cannot produce, and a next step naming nothing would be worse than
		// none — so the reader is pointed at the record instead.
		return "record more of this company's profile before a fit can be judged"
	}
	return "find out " + joinReadably(*c.Missing) + " before this fit can be judged"
}

// joinReadably renders a list the way a person would say it, so the next step
// reads as a sentence rather than as a serialized array.
func joinReadably(items []string) string {
	switch len(items) {
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}
