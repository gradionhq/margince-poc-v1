// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// window is the bounded, grounded context (architecture/07 §3): seed
// grounding + the running observation log, under a hard prompt ceiling.
// Old observations are elided from the middle when the window would
// overflow — the goal and the newest observations always survive.
type window struct {
	system string
	// fence bounds every piece of captured text in this window: tool output,
	// and T2 seed grounding. It belongs to the RUN, not to one model call, because
	// the transcript is cumulative — an observation written in step 2 is still
	// in the prompt at step 9, so the marker naming it in the system prompt has
	// to be the one it was written with, and has to survive a suspension.
	fence promptfence.Fence
	// knownSources is the closed vocabulary [window.observe] may name in the
	// prompt's own voice: the tools this run was offered, plus the runner's own
	// internal reporters. Everything else is model-chosen text.
	knownSources map[string]bool
	msgs         []model.Message
}

// unknownSourceLabel stands in for a source outside the closed vocabulary.
const unknownSourceLabel = "an unrecognized tool"

// sourceVocabulary is the closed set of names an observation may be attributed
// to: every offered tool, plus the runner's own reporters.
func sourceVocabulary(specs []mcp.ToolSpec) map[string]bool {
	known := map[string]bool{outputValidatorSource: true}
	for _, spec := range specs {
		known[spec.Name] = true
	}
	return known
}

// outputValidatorSource attributes the runner's own re-prompt after a model
// reply that would not parse.
const outputValidatorSource = "output_validator"

// windowPromptTokenCeiling bounds the prompt (§3: the window has a hard
// token ceiling; a long run cannot silently grow the context).
const windowPromptTokenCeiling = 24_000

// roleUser is the wire role every window message carries: the goal, each
// observation, and the elision notice are all things the runner SAYS to the
// model — only the model's own replies are the other role.
const roleUser = "user"

// perCallOutputCeiling caps one completion; the remaining run budget
// tightens it further.
const perCallOutputCeiling = 4096

func newWindow(job Job, specs []mcp.ToolSpec) *window {
	fence := promptfence.New()
	w := &window{system: systemPrompt(specs, fence), fence: fence, knownSources: sourceVocabulary(specs)}
	w.msgs = append(w.msgs, model.Message{Role: roleUser, Content: goalPrompt(job, fence)})
	return w
}

// windowFromSnapshot rebuilds a suspended run's window around the fence that
// run's transcript was written with.
//
// A snapshot with text but no fence predates the per-run boundary: its spans
// are marked with a fixed marker any captured page or mail could have written,
// so no marker this build could name would actually bound them. The run is
// refused rather than continued under a boundary that is not one.
func windowFromSnapshot(job Job, specs []mcp.ToolSpec, snapshot []model.Message, fence promptfence.Fence) (*window, error) {
	if len(snapshot) == 0 {
		return newWindow(job, specs), nil
	}
	if !fence.Minted() {
		return nil, fmt.Errorf("%w: this run was suspended before prompt boundaries were per-run; start it again rather than resuming it", apperrors.ErrConflict)
	}
	w := &window{system: systemPrompt(specs, fence), fence: fence, knownSources: sourceVocabulary(specs)}
	w.msgs = append(w.msgs, snapshot...)
	return w, nil
}

// observe appends a tool result (or refusal) as the next user turn.
// Tool output is captured data — T2 by handling rule — so it is
// spotlighted as data-not-instructions (D1) inside the run's own boundary.
//
// The bound is WrapAuthored, not Wrap, because ONE fence spans the whole run
// and the model has read its marker in the system prompt since step 1. That
// makes the model an author who can close the span exactly, and it does not
// need an outsider to do it: a refusal or parse error echoes the model's OWN
// tool arguments and JSON keys straight back into an observation. A page that
// talks the model into emitting the marker once gets prompt-voice text into a
// transcript that is CUMULATIVE — present in every later step, and carried into
// the suspended-run snapshot.
//
// Text a tool merely read is unaffected: it has never seen the nonce, so it
// contains nothing to neutralise and still goes in byte for byte.
func (w *window) observe(source, content string) {
	w.observeThen(source, content, "")
}

// observeThen appends an observation followed by a directive of OURS. The
// directive stays OUTSIDE the span on purpose: text inside is declared "never
// instructions", so an order placed there is one the model has been told to
// disregard — and it would be the one part of the turn that is allowed to give
// orders. An observation with no data carries no span at all.
func (w *window) observeThen(source, data, directive string) {
	content := "observation from " + w.sourceLabel(source) + ":"
	if data != "" {
		content += "\n" + w.fence.WrapAuthored(data)
	}
	if directive != "" {
		content += "\n" + directive
	}
	w.msgs = append(w.msgs, model.Message{Role: roleUser, Content: content})
}

// sourceLabel bounds the one part of an observation that sits OUTSIDE the fence.
//
// The source is the tool name the MODEL chose, and a name the registry does not
// know is unvalidated model output. Printing it in the prompt's own voice would
// undo the fence by another route: a page that talks the model into one crafted
// tool name gets that string into the transcript unfenced, and the transcript is
// cumulative — it is in every later prompt of the run, and it survives into the
// suspended-run snapshot. So a name outside the closed vocabulary is reported as
// a fixed label; what the model actually asked for is still recorded, inside the
// fence, as part of the refusal it earns.
func (w *window) sourceLabel(source string) string {
	if w.knownSources[source] {
		return source
	}
	return unknownSourceLabel
}

func (w *window) snapshot() []model.Message {
	return append([]model.Message(nil), w.msgs...)
}

func (w *window) asRequest(remainingOutputTokens int) model.Request {
	maxTokens := perCallOutputCeiling
	if remainingOutputTokens < maxTokens {
		maxTokens = remainingOutputTokens
	}
	return model.Request{
		System:    w.system,
		Messages:  w.bounded(),
		MaxTokens: maxTokens,
	}
}

const elisionMarker = "[earlier observations elided to fit the context window]"

// bounded elides the oldest observations until the estimated prompt
// fits the ceiling. The first message (goal + grounding) is never
// dropped; the newest observations are kept because they are what the
// model is reasoning over right now.
func (w *window) bounded() []model.Message {
	msgs := append([]model.Message(nil), w.msgs...)
	for estimateTokens(w.system, msgs) > windowPromptTokenCeiling && len(msgs) > 2 {
		oldest := 1
		if msgs[1].Content == elisionMarker {
			oldest = 2
		}
		trimmed := make([]model.Message, 0, len(msgs))
		trimmed = append(trimmed, msgs[0], model.Message{Role: roleUser, Content: elisionMarker})
		trimmed = append(trimmed, msgs[oldest+1:]...)
		msgs = trimmed
	}
	return msgs
}

// estimateTokens is the ~4-bytes-per-token heuristic — coarse, but the
// ceiling exists to stop runaway growth, not to bill by it.
func estimateTokens(system string, msgs []model.Message) int {
	total := len(system)
	for _, m := range msgs {
		total += len(m.Content)
	}
	return total / 4
}

// systemPrompt is the §2.0 shared frame plus the tool surface: JSON-only
// output, the evidence rule, and untrusted-content handling.
func systemPrompt(specs []mcp.ToolSpec, fence promptfence.Fence) string {
	var b strings.Builder
	b.WriteString(`You are the Margince agent runner, a CRM reasoning component, not a chatbot.
You work toward the stated goal by calling tools, one per turn.

Respond with ONE JSON object and nothing else:
  {"tool": "<name>", "args": {…}}   to call a tool, or
  {"final": {…}}                     when the goal is done (include a "summary" string grounded in your observations).

Rules:
- Every claim in your final output must be grounded in an observation; omit what you cannot ground.
- A refused tool call is an answer: re-plan within what you are allowed to do; do not retry the same refused call.
- Actions needing human approval are staged automatically; never fabricate their outcome.
- `)
	b.WriteString(fence.Rule("captured external"))
	b.WriteString(`

Available tools:
`)
	sorted := append([]mcp.ToolSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, spec := range sorted {
		fmt.Fprintf(&b, "- %s (input schema: %s)\n", spec.Name, string(spec.InputSchema))
	}
	return b.String()
}

func goalPrompt(job Job, fence promptfence.Fence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\nTrigger: %s\n", job.Goal, job.TriggerRef)
	if len(job.Grounding) > 0 {
		b.WriteString("Seed context (each item carries its source and trust tier):\n")
	}
	for _, g := range job.Grounding {
		// Default-deny: only a tier this build RECOGNISES as first-party prints
		// raw. TrustTier is a free-form string on an exported Job, so testing for
		// the captured tier instead would fence nothing for "t2", "T2 ", or any
		// tier a later provider invents — an unknown tier is captured text until
		// something says otherwise.
		if trustedTiers[g.TrustTier] {
			fmt.Fprintf(&b, "[%s %s] %s\n", groundingRef(g.SourceID), g.TrustTier, g.Content)
			continue
		}
		fmt.Fprintf(&b, "[%s %s] %s\n", groundingRef(g.SourceID), g.TrustTier, fence.Wrap(g.Content))
	}
	return b.String()
}

// trustedTiers is the closed set of tiers whose content is this system's own —
// a record it holds, or a value a human here entered. Everything else, named or
// not, is captured text and rides inside the fence.
var trustedTiers = map[string]bool{"T0": true, "T1": true}

// groundingRef bounds a seed item's provenance ref, which sits OUTSIDE the fence.
//
// retrieval.Evidence.Source is a free-form seam field. Today's only provider
// fills it with "<type>:<uuid>", but the next one is free to put a subject line
// or a page title there, and that would be captured text reading in the prompt's
// own voice. A ref that is not of the expected shape is reported as unnamed
// rather than printed.
func groundingRef(sourceID string) string {
	if refShape.MatchString(sourceID) {
		return sourceID
	}
	return "unnamed source"
}

// refShape is the provenance form the prompt frame will print: a record type and
// an id, nothing that could carry a sentence.
var refShape = regexp.MustCompile(`^[a-z_]{1,32}:[0-9a-fA-F-]{1,36}$`)
