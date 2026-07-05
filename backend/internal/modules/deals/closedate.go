// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// Close-date hygiene (formulas-and-rules §11, DECISIONS A6): the
// deterministic assessment of an open deal's expected_close_date —
// overdue / missing / unrealistic flags, the activity- and
// experience-informed replacement date, and the A6 hybrid risk tier
// that decides whether the replacement is final (🟢), provisional (🟡),
// or a downgrade of a deal that has gone quiet (🔻). Pure over its
// inputs + clock, so a fixed test clock reproduces every branch.

import "time"

// §11 tunables (spec parameter-registry names).
const (
	// CloseDateUnrealisticSoonDays is CLOSE_DATE_UNREALISTIC_SOON_DAYS:
	// an early-stage deal cannot honestly claim to close within it.
	CloseDateUnrealisticSoonDays = 7
	// CloseDateStageDays is CLOSE_DATE_STAGE_DAYS, the per-remaining-stage
	// velocity fallback while won-deal history is thin.
	CloseDateStageDays = 14
	// CloseDateMinHistory is CLOSE_DATE_MIN_HISTORY: won deals required
	// before the workspace's observed stage velocity outranks the fallback.
	CloseDateMinHistory = 20
	// CloseDateAutoApplyEnabled is CLOSE_DATE_AUTOAPPLY, the master enable
	// for the 🟢 tier; off routes every correction 🟡 provisional-confirm.
	CloseDateAutoApplyEnabled = true

	// unrealisticSoonMaxProb bounds the §11 unrealistic_soon flag: at or
	// above 40% the stage is close enough that a near date is credible.
	unrealisticSoonMaxProb = 40
	// lateStageMinProb is FORECAST_BESTCASE_MIN_PROB (§7): at or above it
	// the date is forecast-bearing, so a machine never finalizes it.
	lateStageMinProb = 50
	// forecastCommitMinProb is FORECAST_COMMIT_MIN_PROB (§7), the derived
	// Commit default when the rep set no explicit forecast_category.
	forecastCommitMinProb = 90
)

// CloseDateFlag is one §11 hygiene finding.
type CloseDateFlag string

const (
	CloseDateOverdue          CloseDateFlag = "overdue"
	CloseDateMissing          CloseDateFlag = "missing"
	CloseDateUnrealisticSoon  CloseDateFlag = "unrealistic_soon"
	CloseDateUnrealisticStale CloseDateFlag = "unrealistic_stale"
)

// CloseDateAction is the A6 hybrid tier chosen for a flagged deal.
type CloseDateAction string

const (
	CloseDateActionAutoApply          CloseDateAction = "auto_apply"
	CloseDateActionProvisionalConfirm CloseDateAction = "provisional_confirm"
	CloseDateActionDowngradeAndReview CloseDateAction = "downgrade_and_review"
)

// CloseDateInput is everything §11 reads about one deal.
type CloseDateInput struct {
	Status         string
	ExpectedClose  *time.Time // date-granular, like the column
	CreatedAt      time.Time
	LastActivityAt *time.Time
	WaitUntil      *time.Time

	// StageWinProbability drives the unrealistic_soon and late-stage
	// judgments; RemainingOpenStages the replacement-date horizon.
	StageWinProbability int
	RemainingOpenStages int

	// InForecastCommit: the deal counts in this period's Commit or
	// Best-case per §7 (explicit forecast_category override, else the
	// probability-derived default) — the number a leader is staking on.
	InForecastCommit bool

	// StageVelocityDays is the workspace-observed median days-per-stage
	// for won deals in the deal's pipeline; zero (thin history, below
	// CloseDateMinHistory) falls back to CloseDateStageDays.
	StageVelocityDays float64
}

// CloseDateHygiene is the §11 per-deal output.
type CloseDateHygiene struct {
	Flags   []CloseDateFlag
	Flagged bool
	// ProposedClose is the activity/velocity-informed replacement date;
	// nil when nothing is flagged.
	ProposedClose *time.Time
	Action        CloseDateAction
	// Provisional: the written date is a guess a human must confirm — the
	// deal stays out of Commit/Best-case until then.
	Provisional bool
	// Downgrade: the deal has gone quiet; its forecast category drops one
	// notch instead of the date being pushed optimistically forward.
	Downgrade bool
}

// CloseDateAssessment evaluates §11 for one deal at one instant. "Today"
// buckets in the workspace zone (data-semantics §2 r4); everything else
// is date arithmetic on that day.
func CloseDateAssessment(in CloseDateInput, now time.Time, workspaceTZ *time.Location) CloseDateHygiene {
	if in.Status != "open" {
		return CloseDateHygiene{} // won/lost deals are never flagged
	}
	today := dateAt(now, workspaceTZ)

	findings := closeDateFindings{
		stalled: IsStalled(in.Status, in.CreatedAt, in.LastActivityAt, in.WaitUntil, now),
		paused:  in.WaitUntil != nil && now.Before(*in.WaitUntil),
	}

	var out CloseDateHygiene
	if in.ExpectedClose == nil {
		findings.missing = true
		out.Flags = append(out.Flags, CloseDateMissing)
	} else {
		expected := dateOnly(*in.ExpectedClose)
		// Strict <: a deal legitimately closing today is fine.
		findings.overdue = expected.Before(today)
		if findings.overdue {
			out.Flags = append(out.Flags, CloseDateOverdue)
		}
		// The soon window judges a CLAIMED FUTURE date; a past one already
		// reads overdue — §11's own worked example treats an overdue
		// early-stage (20%) deal as clear_overdue, i.e. the 🟢 tier.
		findings.unrealisticSoon = !findings.overdue &&
			!expected.After(today.AddDate(0, 0, CloseDateUnrealisticSoonDays)) &&
			in.StageWinProbability < unrealisticSoonMaxProb
		if findings.unrealisticSoon {
			out.Flags = append(out.Flags, CloseDateUnrealisticSoon)
		}
		// unrealistic_stale rides is_stalled, so a non-null future
		// wait_until suppresses it (an explicitly-paused deal is not
		// "gone dark") — but never the overdue flag above.
		if findings.stalled && !expected.After(today.AddDate(0, 0, StalledThresholdDays)) {
			out.Flags = append(out.Flags, CloseDateUnrealisticStale)
		}
	}
	out.Flagged = len(out.Flags) > 0
	if !out.Flagged {
		return out
	}

	proposed := proposedCloseDate(today, in.RemainingOpenStages, in.StageVelocityDays)
	out.ProposedClose = &proposed
	out.Action = closeDateAction(findings, in, CloseDateAutoApplyEnabled)
	out.Downgrade = out.Action == CloseDateActionDowngradeAndReview
	// A 🟡 date is a guess by definition; a 🔻 deal only gets a guessed
	// date when the invariant forces one (past or missing) — it is never
	// re-dated optimistically on top of the downgrade.
	out.Provisional = out.Action == CloseDateActionProvisionalConfirm ||
		(out.Downgrade && (findings.overdue || findings.missing))
	return out
}

// closeDateFindings are the §11 predicates the tier policy folds over.
type closeDateFindings struct {
	stalled         bool
	paused          bool // future wait_until: explicitly deferred, not gone dark
	overdue         bool
	missing         bool
	unrealisticSoon bool
}

// closeDateAction is the DECISIONS A6 hybrid policy fold, parameterized
// on the CLOSE_DATE_AUTOAPPLY switch so both positions are testable.
func closeDateAction(f closeDateFindings, in CloseDateInput, autoApply bool) CloseDateAction {
	if f.stalled {
		// Gone dark: never push the date forward on a deal nobody is
		// touching — that is how zombie deals stay forecast-fresh.
		return CloseDateActionDowngradeAndReview
	}
	// §11 edge case: a paused deal (future wait_until) is not "gone dark",
	// but its past date is corrected on the 🟡 path — the customer said
	// wait, so a stage-velocity guess is never silently finalized.
	clearOverdue := f.overdue && !f.missing && !f.unrealisticSoon
	lateStage := in.StageWinProbability >= lateStageMinProb
	if autoApply && clearOverdue && !f.paused && !in.InForecastCommit && !lateStage {
		return CloseDateActionAutoApply
	}
	return CloseDateActionProvisionalConfirm
}

// proposedCloseDate is §11's computed correction: today plus at least one
// stage-worth of the workspace's observed (or fallback) velocity.
func proposedCloseDate(today time.Time, remainingOpenStages int, velocityDays float64) time.Time {
	if velocityDays <= 0 {
		velocityDays = CloseDateStageDays
	}
	stages := remainingOpenStages
	if stages < 1 {
		stages = 1
	}
	return today.AddDate(0, 0, int(float64(stages)*velocityDays))
}

// dateAt reduces an instant to its calendar date in the given zone,
// represented (like every scanned date column) as UTC midnight.
func dateAt(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// dateOnly strips any time-of-day a date column round-trip left behind.
func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
