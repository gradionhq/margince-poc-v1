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
	partial, _ := json.Marshal(map[string]any{
		"partial":         true,
		"reason":          reason,
		"steps_completed": len(acc.Steps),
	})
	acc.Final = partial
	return acc
}
