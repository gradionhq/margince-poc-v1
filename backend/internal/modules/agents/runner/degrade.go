// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// The degrade reasons are a CLOSED vocabulary, and closed is a security
// property rather than a style: agent_run.degrade_reason is read by the ordinary
// human the run acted for (GET /me/agent-activity), and a wrapped cause from
// here carries the model provider's own message — vendor identity, internal
// error text, and in the credential arms the key material the provider echoed
// back. That is the same infrastructure cause httperr.Write withholds from a
// caller, so it is withheld here too and the operator log keeps it instead.
//
// Each reason names what stopped the run and what to do about it, because it is
// the only sentence anyone gets.
const (
	reasonWallClockExceeded = "wall clock exceeded — the run was cancelled before it finished; " +
		"the next scheduled occurrence will start clean"
	reasonModelCallFailed = "model call failed — the AI provider did not answer this run; " +
		"the server log carries the provider's own message"
	reasonStepBudgetExhausted        = "step budget exhausted"
	reasonOutputTokenBudgetExhausted = "output token budget exhausted"
)

// invalidOutputReason names a run whose model could not produce a parseable step
// within the retry limit. The count is server-authored; the parser's message is
// not, and it stays in the log.
func invalidOutputReason(attempts int) string {
	return fmt.Sprintf("model output failed validation %d times — "+
		"the server log carries what the parser rejected", attempts)
}

// degradeFromCause degrades on one of the closed reasons and routes the
// underlying cause to the operator log, which is the only place it may go: the
// reason reaches a browser, the cause does not, and losing the cause entirely
// would leave a degraded overnight run with nothing to diagnose it from.
func (r *Runner) degradeFromCause(acc Result, job Job, reason string, cause error) Result {
	slog.Warn("agent run degraded", "trigger_ref", job.TriggerRef, "reason", reason, "cause", cause)
	degraded := r.degrade(acc, reason)
	degraded.DegradeCause = cause.Error()
	return degraded
}

// DegradeDetail is the fullest account of why a run stopped, for an operator
// surface — the certification report, a diagnostic. Anything a PERSON reads
// takes DegradeReason instead: the cause is what must not reach a browser.
func (r Result) DegradeDetail() string {
	if r.DegradeCause == "" {
		return r.DegradeReason
	}
	return r.DegradeReason + ": " + r.DegradeCause
}

// degrade produces the best partial result reached so far — the B32
// graceful-degrade contract. Anything 🟡 the run wanted is already
// staged (it was staged at proposal time), so nothing is silently lost.
func (r *Runner) degrade(acc Result, reason string) Result {
	acc.Outcome = OutcomeDegraded
	acc.DegradeReason = reason
	acc.Final = partialResult{
		Partial:        true,
		Reason:         reason,
		StepsCompleted: len(acc.Steps),
	}.render()
	return acc
}

// partialResult is what a degraded run leaves in agent_run.result: the shape a
// reader of that column parses, named so it is one declared thing rather than a
// map assembled at the call site.
type partialResult struct {
	Partial        bool   `json:"partial"`
	Reason         string `json:"reason"`
	StepsCompleted int    `json:"steps_completed"`
}

// render is the JSON the run stores. A degrade cannot fail — it IS the failure
// path, and returning an error here would turn a graceful degrade into a crash —
// so an unmarshalable payload falls back to the one field that carries the
// outcome, and the reason it could not be rendered goes to the operator log
// rather than being dropped on the floor.
func (p partialResult) render() json.RawMessage {
	rendered, err := json.Marshal(p)
	if err != nil {
		slog.Error("agent run: a degraded run's partial result could not be rendered",
			"reason", p.Reason, "cause", err)
		return json.RawMessage(`{"partial":true}`)
	}
	return rendered
}

// FailureReason closes a run from OUTSIDE the loop — a resume whose authority
// died, a fault the loop never saw, an abandoned row a sweep found. It lands in
// the SAME agent_run.degrade_reason the loop writes, so it is bound by the same
// rule, and the named type is what makes that rule hold at compile time:
// err.Error() is a typed string and does not convert implicitly, so the two ways
// a cause reached this column — the bare error text and a prefix concatenated
// onto it — no longer build. A caller with a cause logs it and picks a reason.
type FailureReason string

// The closed vocabulary for those closes. Each says what ended the run and what
// the reader can do about it; none is derived from a cause, and none names an
// internal identifier a reader cannot act on.
const (
	FailureEditedApprovalCarriedNoChange FailureReason = "the approval was edited but the decision " +
		"carries no edited version of the action, so there was nothing safe to re-present; ask for the action again"
	FailurePassportNoLongerValid FailureReason = "the authority this run was acting under is no longer " +
		"valid — the passport was revoked or expired, or the person it acts for was deactivated; " +
		"grant it again and the next occurrence will run"
	FailureSpecLeftTheCatalog FailureReason = "this scheduled agent was removed while the run waited " +
		"for an answer, so there is no goal left to resume; nothing further is needed"
	// FailureCouldNotStart is a job that never became a run: the passport would
	// not resolve, or the run row could not be written. The CAUSE is the
	// operator log's — a resolution error carries identity's own words and a
	// write error carries the driver's, and both reach an ordinary rep through
	// the AI-activity rail.
	FailureCouldNotStart FailureReason = "this scheduled run could not start; " +
		"nothing was changed, and it will be tried again on its next schedule"

	FailureRunFaulted FailureReason = "the run stopped on a fault before it could finish — " +
		"the server log carries the cause; the next scheduled occurrence starts clean"
)
