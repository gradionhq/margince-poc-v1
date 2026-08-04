// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The probe's boundary report: what the site was given, what each call carried,
// and what the production validator made of the reply — as numbers, because the
// numbers are what turn "the refresh errored" into a cause.

import (
	"fmt"
	"io"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// approxTokensPerByte is the crude divisor the request line's estimate uses.
// It is labelled "~" everywhere it is printed: the point is the ORDER of
// magnitude against a context window and an output cap, and a real tokenizer
// per provider would buy precision nobody reading this line needs.
const approxTokensPerByte = 4

func writeProbeReport(w io.Writer, res probeResult) error {
	var b strings.Builder
	fmt.Fprintf(&b, "site      %s   kind=%s   scope=%s\n", res.Site, res.Kind, res.Scope)
	fmt.Fprintf(&b, "binding   %s   ladder [%s]\n", res.Binding, res.Ladder)
	fmt.Fprintf(&b, "caveat    %s\n", res.ContextCaveat)
	fmt.Fprintf(&b, "fixture   %d B%s\n", res.FixtureBytes, expectationNote(res))

	for i, call := range res.Calls {
		fmt.Fprintf(&b, "\ncall %d\n", i+1)
		fmt.Fprintf(&b, "  request   %s\n", requestLine(call.Request))
		if call.Err != "" {
			fmt.Fprintf(&b, "  response  FAILED after %s — %s\n", call.Latency.Round(1e6), call.Err)
		} else {
			fmt.Fprintf(&b, "  response  %s\n", responseLine(call))
		}
		// Kept on its own line: a credential pass that could not run is THIS
		// tool's problem, and rendering it as a failed model call would blame
		// the provider for it.
		if call.RedactionErr != "" {
			fmt.Fprintf(&b, "  redaction %s — nothing was written for this call\n", call.RedactionErr)
		}
	}

	switch {
	case res.Failure != "":
		fmt.Fprintf(&b, "\nfailed    %s\n", res.Failure)
	case res.Outcome != nil:
		fmt.Fprintf(&b, "\nevaluate  %s%s\n", res.Outcome.Result, detailNote(res.Outcome.Detail))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// expectationNote says when no expectation was supplied, because that changes
// what the outcome can possibly be: with none, a well-formed reply saying the
// wrong thing is indistinguishable from one saying the right thing.
func expectationNote(res probeResult) string {
	if res.HasExpectation {
		return ""
	}
	return "   (no expectation supplied — a wrong answer cannot be detected)"
}

func detailNote(detail string) string {
	if detail == "" {
		return ""
	}
	return " — " + detail
}

// requestLine sizes the request the way the failure modes present: the system
// prompt and the payload separately (one is the site's, one is the operator's),
// an order-of-magnitude token estimate, and the output ceiling the reply has to
// fit inside.
func requestLine(req model.Request) string {
	payload := 0
	for _, msg := range req.Messages {
		payload += len(msg.Content)
	}
	parts := []string{
		fmt.Sprintf("system %d B", len(req.System)),
		fmt.Sprintf("payload %d B", payload),
		fmt.Sprintf("passages %d", payloadPassages(req)),
		fmt.Sprintf("~%s tok", humanTokens((len(req.System)+payload)/approxTokensPerByte)),
		fmt.Sprintf("max_tokens %d", req.MaxTokens),
	}
	if len(req.ResponseSchema) > 0 {
		parts = append(parts, fmt.Sprintf("schema %d B", len(req.ResponseSchema)))
	}
	return strings.Join(parts, "  ")
}

// payloadPassages counts the numbered passages a built request actually carries.
//
// It counts `[sN]` markers rather than non-empty lines, because by this point
// the page text has been WRAPPED: the fence adds an opening and a closing
// marker of its own, and counting those would report a one-model payload as
// three passages — an evidence-coverage claim that is simply false.
// compose.CountPassages answers the other question (what raw text WOULD number
// to) and is what `fetch` uses, before any fencing exists.
//
// This is the number that explains a whole class of failure: a body served as
// ONE line numbers to ONE passage however many bytes it holds, so every
// extracted row cites the same id and the evidence gate has nothing to disagree
// with. A byte count hides that completely.
func payloadPassages(req model.Request) int {
	n := 0
	for _, msg := range req.Messages {
		for _, line := range strings.Split(msg.Content, "\n") {
			if isNumberedPassage(strings.TrimSpace(line)) {
				n++
			}
		}
	}
	return n
}

// isNumberedPassage matches the `[sN] ` prefix numberPassages emits.
//
// The marker has to END, not merely appear: `[s12]not-a-marker` is page content
// that happens to start like one, and counting it would inflate the very number
// the report exists to make trustworthy.
func isNumberedPassage(line string) bool {
	rest, ok := strings.CutPrefix(line, "[s")
	if !ok {
		return false
	}
	digits, after, ok := strings.Cut(rest, "]")
	if !ok || digits == "" {
		return false
	}
	if strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return false
	}
	// numberPassages writes "[sN] text"; a bare "[sN]" ends the line.
	return after == "" || strings.HasPrefix(after, " ")
}

func responseLine(call probeCall) string {
	parts := []string{
		fmt.Sprintf("in %d tok", call.Reply.InputTokens),
		fmt.Sprintf("out %d tok%s", call.Reply.OutputTokens, capFlag(call)),
		fmt.Sprintf("%d B", len(call.Reply.Text)),
	}
	if served := servedIdentity(call); served != "" {
		parts = append(parts, "served="+served)
	}
	if call.Route.Tier != "" {
		parts = append(parts, "tier="+string(call.Route.Tier))
	}
	if call.Route.Degraded {
		parts = append(parts, "DEGRADED")
	}
	// A probe reports what a call DID. A cache-served repeat that looked like a
	// fresh call would be the one thing the report must never imply.
	if call.Route.Cached {
		parts = append(parts, "CACHED")
	}
	parts = append(parts, call.Latency.Round(1e6).String())
	return strings.Join(parts, "  ")
}

// capFlag marks a reply that used its entire output budget.
//
// This is an INFERENCE, not a fact off the wire: model.Response carries no
// finish reason, so a model that legitimately stopped exactly at the ceiling is
// indistinguishable from one that was cut off. It is printed beside the raw
// numbers as a flag, never as a claim about why the provider stopped — but a
// site whose answer scales with its input hits it long before anything else
// goes wrong, and that is worth pointing at.
func capFlag(call probeCall) string {
	if hitCap(call.Response, call.Request) {
		return " (HIT CAP)"
	}
	return ""
}

func hitCap(resp model.Response, req model.Request) bool {
	return req.MaxTokens > 0 && resp.OutputTokens >= req.MaxTokens
}

// servedIdentity prefers what the PROVIDER said answered over what the routing
// bound, because a vendor may silently substitute a model and the difference is
// exactly what a surprising result is explained by.
func servedIdentity(call probeCall) string {
	if call.Reply.ServedModel != "" {
		return call.Reply.ServedModel
	}
	if call.Route.ModelID != "" {
		return call.Route.ModelID
	}
	return ""
}

// humanTokens keeps the estimate readable at the scale that matters — a 132k
// figure beside a 262k window is the whole point, and "132k" reads faster than
// "132464" while it is an estimate either way.
func humanTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.0fk", float64(n)/1000)
}
