// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aitasks

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Completer is the one model call a prepared case may make. It is the seam
// every production site already takes, so a case runs the shipped path rather
// than a re-creation of it.
type Completer interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

// CaseFactory binds one registered site to the production code that serves it.
// It lives in the composition layer because that is where the private request
// builders are, and because aicert imports compose — never the reverse.
type CaseFactory interface {
	// Site names which registered site this factory serves.
	Site() Site
	// Prepare turns one corpus fixture and the answer the scenario expects into
	// a runnable case.
	//
	// The two arrive separately because they are different kinds of thing: the
	// fixture is what PRODUCTION is given, the expectation is what the CORPUS
	// asserts about the reply. Folding one into the other makes every gate that
	// rewrites a fixture — the canary sweep seeds every free-text field it has —
	// able to rewrite an assertion by accident.
	//
	// Both arrive at prepare time for the same reason: a case can refuse an
	// unreachable expectation before a paid run spends anything on it, and the
	// production validator closes over the fixture here — the requested row id,
	// the snippet index, the page menu — so the cert lane runs the SAME
	// validator production runs, built the same way. A validator that cannot see
	// the fixture is strictly weaker than the one it claims to stand for.
	Prepare(fixture, expected json.RawMessage) (PreparedCase, error)
}

// PreparedCase is one fixture ready to be certified.
type PreparedCase interface {
	// Run issues the production invocation. It may be one request, several
	// turns, a retry, or a whole tool-fed loop: an agent loop has no single
	// buildable request, and forcing one would make the case lie about what it
	// exercises.
	Run(ctx context.Context, c Completer) (Trace, error)
	// Evaluate applies the production validator to what Run produced. It
	// returns an Outcome rather than an error, because "the model answered
	// wrongly" is a measurement, not a failure of the harness.
	Evaluate(Trace) Outcome
}

// Trace is what one prepared case actually did. Output is the final model text
// the validator reads; Requests carries every request issued, which is what the
// canary gate reads to prove no untrusted fixture text reached an instruction.
type Trace struct {
	Requests []model.Request
	Output   string
}

// The three things a certified reply can be. They are distinct because an
// unusable reply and a wrong answer fail for opposite reasons and want
// opposite fixes, and a record that collapses them can report neither.
const (
	// OutcomeAccepted: the production validator accepted the reply AND it is
	// the answer the fixture expects.
	OutcomeAccepted = "accepted"
	// OutcomeWrongAnswer: the validator accepted a well-formed reply that says
	// something other than what the fixture expects.
	OutcomeWrongAnswer = "wrong_answer"
	// OutcomeInvalid: the production validator refused the reply. This is the
	// deterministic signal that the model produced something unusable.
	OutcomeInvalid = "invalid"
)

// Outcome is one evaluated run. Detail names why, in the validator's own
// words, and is what turns a reliability drop into a diagnosis.
type Outcome struct {
	Result string
	Detail string
}
