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
// workspace budget by construction.
func (r *Router) CompleteStructured(ctx context.Context, task Task, req model.Request, validate Validator) (model.Response, RouteInfo, error) {
	resp, info, err := r.Complete(ctx, task, req)
	if err != nil {
		return model.Response{}, info, err
	}
	firstErr := validate(resp.Text)
	if firstErr == nil {
		return resp, info, nil
	}

	retry := withValidatorFeedback(req, resp.Text, firstErr)
	resp, info, err = r.Complete(ctx, task, retry)
	if err != nil {
		return model.Response{}, info, err
	}
	secondErr := validate(resp.Text)
	if secondErr == nil {
		return resp, info, nil
	}

	escalated := withValidatorFeedback(req, resp.Text, secondErr)
	resp, info, err = r.completeEscalated(ctx, task, escalated)
	if err != nil {
		return model.Response{}, info, err
	}
	if finalErr := validate(resp.Text); finalErr != nil {
		return model.Response{}, info, fmt.Errorf(
			"ai: %s output failed validation after retry and escalation: %w", task, finalErr)
	}
	return resp, info, nil
}

// withValidatorFeedback appends the failed output and its validation
// error as conversation turns, so the retry is a correction, not a
// blind re-roll. The changed messages also miss the result cache — a
// retry can never be served the cached invalid answer.
func withValidatorFeedback(req model.Request, failedText string, cause error) model.Request {
	out := req
	out.Messages = append(append([]model.Message{}, req.Messages...),
		model.Message{Role: "assistant", Content: failedText},
		model.Message{Role: "user", Content: "That output failed validation: " + cause.Error() +
			"\nReturn ONLY the corrected output in the required format."},
	)
	return out
}

// completeEscalated serves one call from the task's ladder with the
// first rung dropped — "escalate one tier on second failure" (§5.2).
// A single-rung ladder has nowhere to go; the default route answers.
func (r *Router) completeEscalated(ctx context.Context, task Task, req model.Request) (model.Response, RouteInfo, error) {
	ladder, ok := taskLadders[task]
	if !ok || len(ladder) < 2 {
		return r.Complete(ctx, task, req)
	}
	return r.complete(ctx, task, ladder[1:], req)
}
