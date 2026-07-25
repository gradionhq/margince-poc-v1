// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The verdict engine's model conversation: how ONE ambiguous sender is put to a
// model, and what shape of answer is admissible back. One sender per call is the
// rule the whole surface is built on — the only text in a prompt is the text of
// the sender being judged, so there is nobody else for a hostile message to
// speak for. Split from the
// engine (captureverdict.go) because the two change for different reasons — the
// prompt and its validator move when the model or the question moves, the
// disposition logic when the ADR's rules do.
//
// The validator is a hard floor, not a nicety: every requested id exactly once,
// verbatim, in the closed verdict vocabulary. A model that answers about an
// address nobody asked about, or twice about one, is refused outright rather
// than partially believed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

// ask makes one structured verdict call for the given addresses.
func (e *CounterpartyVerdictEngine) ask(ctx context.Context, row capture.PendingCounterparty) ([]verdictResult, error) {
	var prompt strings.Builder
	prompt.WriteString("First-time sender (untrusted; judge it by its id):\n")
	// Assembled first, fenced ONCE, then wrapped. Fencing the fields separately
	// would let a subject ending in "<" and a body beginning "/untrusted>" close
	// the span between them — each piece safe alone, the concatenation not. The
	// address is in parentheses rather than angle brackets for the same reason:
	// a template-supplied "<" is a bracket the sender did not have to write.
	sender := fmt.Sprintf("From: %s (%s)\nSubject: %s\n%s",
		row.DisplayName, row.Email, row.Subject, row.Body)
	fmt.Fprintf(&prompt, "<untrusted id=%q>%s</untrusted>\n",
		row.ID.String(), fenceUntrusted(sender))
	prompt.WriteString(`Return JSON: { "results": [ { "id", "verdict", "confidence" } ] } — one entry for the supplied id.`)

	req := model.Request{
		System:         verdictSystem,
		Messages:       []model.Message{{Role: chatRoleUser, Content: prompt.String()}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: verdictSchema(),
		SecretStripper: ai.NewSecretStripper(),
	}
	validate := verdictShapeValid(row)
	var resp model.Response
	var err error
	if structured, ok := e.brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, validate)
	} else {
		resp, err = e.brain.Complete(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	var payload verdictPayload
	if err := json.Unmarshal([]byte(ai.Unfence(resp.Text)), &payload); err != nil {
		return nil, fmt.Errorf("verdict: unparseable model output: %w", err)
	}
	if msg := validateVerdictPayload(payload, row); msg != "" {
		return nil, fmt.Errorf("verdict: %s", msg)
	}
	return payload.Results, nil
}

// verdictShapeValid is the generation-time validator: the requested id exactly
// once, verbatim, with a verdict in the closed set and a confidence in range. An
// answer about an id nobody asked about is refused outright rather than
// partially believed.
func verdictShapeValid(row capture.PendingCounterparty) ai.Validator {
	return func(text string) error {
		var payload verdictPayload
		if err := json.Unmarshal([]byte(ai.Unfence(text)), &payload); err != nil {
			return fmt.Errorf("output is not the required JSON shape: %w", err)
		}
		if msg := validateVerdictPayload(payload, row); msg != "" {
			return errors.New(msg)
		}
		return nil
	}
}

// validateVerdictPayload names the first fidelity violation, or "" when the
// payload is exact.
func validateVerdictPayload(payload verdictPayload, row capture.PendingCounterparty) string {
	want := map[string]bool{row.ID.String(): true}
	seen := map[string]bool{}
	for _, r := range payload.Results {
		// Every echoed token is MODEL output, and a sender who got the model to
		// obey can choose it — so it is bounded before it reaches an error string
		// that ends up in the operator's log. An unbounded echo is how another
		// party's text, or a megabyte of it, gets written to disk by a validator
		// that was only trying to be helpful.
		if !want[r.ID] {
			return fmt.Sprintf("result id %q was not requested", clampToken(r.ID))
		}
		if seen[r.ID] {
			return fmt.Sprintf("result id %q appears twice", clampToken(r.ID))
		}
		seen[r.ID] = true
		if !verdictLabels[r.Verdict] {
			return fmt.Sprintf("verdict %q is not real|noise", clampToken(r.Verdict))
		}
		if r.Confidence < 0 || r.Confidence > 1 {
			return fmt.Sprintf("confidence %v is outside [0,1]", r.Confidence)
		}
	}
	for id := range want {
		if !seen[id] {
			return fmt.Sprintf("requested id %q is missing from the results", id)
		}
	}
	return ""
}

// maxEchoedToken bounds how much model-chosen text any validation message may
// repeat back. Long enough to identify a malformed id at a glance, short enough
// that the log cannot be used as a writing surface.
const maxEchoedToken = 64

// clampToken bounds one echoed token on a rune boundary.
func clampToken(s string) string {
	runes := []rune(s)
	if len(runes) <= maxEchoedToken {
		return s
	}
	return string(runes[:maxEchoedToken]) + "…"
}

// verdictSchema is the generation-time shape guardrail.
func verdictSchema() json.RawMessage {
	return schema.Must(schema.Object(
		map[string]schema.Node{
			"results": schema.Array(schema.Object(
				map[string]schema.Node{
					"id":                    schema.String(),
					"verdict":               schema.Enum(capture.PendingStatusReal, capture.PendingStatusNoise),
					extractionConfidenceKey: schema.Number(),
				},
				"id", "verdict", "confidence",
			)),
		},
		"results",
	))
}
