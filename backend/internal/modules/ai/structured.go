// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Structured-output enforcement (B-EP06.25, ai-operational-spec §5.1/2):
// parse → validate → retry once with the validator's error appended →
// escalate one tier → return the HONEST degraded state. The model never
// gates itself: validation is code, retries are policy, and a final
// failure is an error the caller surfaces — never a partial fabrication.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Validator judges one completion's text; the returned error is fed
// back verbatim on the retry so the model sees WHY it failed.
type Validator func(text string) error

// CompleteStructured runs the §5.2 retry policy:
//
//	attempt 1: default route
//	attempt 2: same route, validator error appended
//	attempt 3: one tier escalated (ladder offset), error appended
//
// Every attempt is metered like any call — retries count against the
// workspace budget by construction. All three attempts are ONE logical
// call (spec §4): they share a LogicalCallID and are flushed together, so
// a certification read sees the retry/escalation chain as the single
// served-or-failed decision it is, not three unrelated ai_call rows.
func (r *Router) CompleteStructured(ctx context.Context, task Task, req model.Request, validate Validator) (model.Response, RouteInfo, error) {
	ladder, ok := taskLadders[task]
	if !ok {
		return model.Response{}, RouteInfo{}, fmt.Errorf("ai: unknown task %q", task)
	}
	lc := newLogicalCall()
	defer r.flushDetached(ctx, lc)

	resp, info, err := r.serveAttempt(ctx, lc, task, ladder, req, "")
	if err != nil {
		return model.Response{}, info, err
	}
	firstErr := validate(resp.Text)
	if firstErr == nil {
		return resp, info, nil
	}
	r.forgetCached(ctx, task, req)

	retry := withValidatorFeedback(req, resp.Text, firstErr)
	resp, info, err = r.serveAttempt(ctx, lc, task, ladder, retry, attemptReasonSchemaInvalid)
	if err != nil {
		return model.Response{}, info, err
	}
	secondErr := validate(resp.Text)
	if secondErr == nil {
		return resp, info, nil
	}
	r.forgetCached(ctx, task, retry)

	escalated := withValidatorFeedback(req, resp.Text, secondErr)
	resp, info, err = r.completeEscalated(ctx, lc, task, escalated)
	if err != nil {
		return model.Response{}, info, err
	}
	if finalErr := validate(resp.Text); finalErr != nil {
		r.forgetCached(ctx, task, escalated)
		return model.Response{}, info, fmt.Errorf(
			"ai: %s output failed validation after retry and escalation: %w", task, finalErr,
		)
	}
	return resp, info, nil
}

// forgetCached evicts the cached completion for exactly this request: a
// response the validator rejected must not be replayed to a future
// identical call. Without this, a retried BUILD with an unchanged corpus
// deterministically replays its own failure from the cache until the TTL
// expires. Best-effort — a request outside a workspace has no cache row.
func (r *Router) forgetCached(ctx context.Context, task Task, req model.Request) {
	rawWS, ok := principal.WorkspaceID(ctx)
	if !ok {
		return
	}
	key, err := cacheKey(ids.From[ids.WorkspaceKind](rawWS), task, req)
	if err != nil {
		// The serve path derived this same key moments ago, so a failure
		// here is a real anomaly: an unevicted invalid answer would replay
		// on retry, which must not stay invisible to the operator.
		r.log.WarnContext(ctx, "ai: cache eviction skipped", "task", string(task), "err", err)
		return
	}
	r.cache.forget(key)
}

// withValidatorFeedback appends the failed output and its validation
// error as conversation turns, so the retry is a correction, not a
// blind re-roll. The changed messages also miss the result cache — a
// retry can never be served the cached invalid answer.
// Both echoed turns are DATA and go inside the request's boundary. The failed
// output is the model repeating text a sender steered, and the validator's
// message quotes tokens out of it, so appending either in the clear would put
// captured text back in the instruction region — while the system prompt still
// says the markers are the ONLY boundary. That is the hole the fence exists to
// close, reached on the repair path instead of the first attempt, and it
// compounds: attempt 3 escalates to a STRONGER model carrying the same turns.
//
// A prompt that declares NO boundary was shown no untrusted data, so its failed
// output is not attacker-steered and goes back plainly. The quarantine is for
// the prompts that named a fence, which are exactly the ones that read captured
// text.
func withValidatorFeedback(req model.Request, failedText string, cause error) model.Request {
	out := req
	echo := func(s string) string { return s }
	if fence, declared := feedbackFence(out); declared {
		// WrapAuthored, not Wrap: this text came from a model that was SHOWN the
		// marker, so it is the one author who can close the span exactly.
		echo = fence.WrapAuthored
	}
	out.Messages = append(
		append([]model.Message{}, req.Messages...),
		model.Message{Role: "assistant", Content: echo(failedText)},
		model.Message{Role: "user", Content: "That output failed validation: " +
			echo(cause.Error()) + "\n" + retryInstruction},
	)
	return out
}

// retryInstruction is the only part of the feedback turns written by this
// codebase, and therefore the only part that carries authority.
const retryInstruction = "Return ONLY the corrected output in the required format."

// feedbackFence resolves the boundary the echoed turns belong in: the one the
// prompt already declared, never a second one beside it. A marker this package
// could not have minted is not a boundary, and borrowing it would claim a
// protection the span does not have.
func feedbackFence(req model.Request) (promptfence.Fence, bool) {
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		return promptfence.Fence{}, false
	}
	return promptfence.FromMarker(marker)
}

// completeEscalated serves one attempt from the task's ladder with the
// first rung dropped — "escalate one tier on second failure" (§5.2) — as
// the third attempt of lc's logical call. An escalation with nowhere to
// go — a single-rung ladder, or no bound client on any remaining rung —
// answers from the default route instead of failing a call the task's own
// ladder could still serve.
func (r *Router) completeEscalated(ctx context.Context, lc *logicalCall, task Task, req model.Request) (model.Response, RouteInfo, error) {
	ladder, ok := taskLadders[task]
	if !ok || len(ladder) < 2 || !r.anyBound(ladder[1:]) {
		return r.serveAttempt(ctx, lc, task, ladder, req, attemptReasonSchemaInvalid)
	}
	return r.serveAttempt(ctx, lc, task, ladder[1:], req, attemptReasonSchemaInvalid)
}

// anyBound reports whether at least one rung resolves to a bound client.
func (r *Router) anyBound(ladder []Tier) bool {
	for _, t := range ladder {
		if _, ok := r.clients[t]; ok {
			return true
		}
	}
	return false
}
