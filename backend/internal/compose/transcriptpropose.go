// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Reading one meeting transcript for the next steps in it (S-E04.3), and
// staging each as a question a human answers.
//
// What this site may claim is bounded by what a transcript IS: a record of what
// people said. So a proposal here is always "somebody said they would do this",
// never "this deal should move to negotiation" — the second is a conclusion
// about the account, and nothing in a transcript states it. Every proposal
// cites the lines it was read from and writes NOTHING until a human accepts it
// (GATE-AI-2); accepting creates one task activity through the same RBAC-gated
// write a rep's own "add task" takes.
//
// It mirrors signalextract.go deliberately — same fenced prompt, same pure
// request builder, same validator-then-floor split — because the two sites do
// the same job on different material, and one shape is what lets a reader who
// knows either one read the other.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

const (
	// transcriptConfidenceFloor: below it the proposal is DROPPED rather than
	// staged. A question a human has to answer costs their attention, and an
	// unsure reading of what someone committed to is not worth spending it on
	// — the transcript is still on the timeline for them to read themselves.
	transcriptConfidenceFloor = 0.7
	// maxTranscriptProposals caps one reading's yield. A meeting that produces
	// eight next steps has not been read, it has been summarized — and eight
	// questions in the inbox is not a confirmation flow, it is a form.
	maxTranscriptProposals = 5
	// maxTranscriptPromptLines and maxTranscriptPromptChars bound what one
	// reading addresses. Beyond them the read FAILS with a message that says
	// so, rather than reading a prefix: a proposal set that silently covers the
	// first half of a meeting looks exactly like one that covers all of it.
	maxTranscriptPromptLines = 600
	maxTranscriptPromptChars = 60000
	// maxTranscriptCitedLines bounds one claim's citation. A commitment stated
	// across more than a few lines is being summarized, not located.
	maxTranscriptCitedLines = 6
)

const transcriptSystem = `You read one meeting or call transcript and report the NEXT STEPS and COMMITMENTS
it states — a specific thing a named party said they would do. Report one only when the
transcript SAYS it: "I'll send the pricing by Friday", "we'll get you the security review".
Report nothing for topics discussed without a commitment, for things you are inferring
rather than reading, and for anything about what the DEAL should do — a transcript records
what people said, not what should happen to the account. Cite the line numbers the
commitment is stated on. Reporting nothing is the correct answer for many transcripts.`

// transcriptSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func transcriptSystemFor(fence promptfence.Fence) string {
	return transcriptSystem + "\n" + fence.Rule("line")
}

// TranscriptProposer reads a transcript and stages what it says was promised.
type TranscriptProposer struct {
	pool     *pgxpool.Pool
	brain    completer
	approval *approvals.Service
	now      func() time.Time
	log      *slog.Logger
}

// NewTranscriptProposer builds the engine over the pool, one model lane, and
// the SAME approvals service the HTTP surface decides on — so a released effect
// can redeem the proposal this staged.
func NewTranscriptProposer(
	pool *pgxpool.Pool, brain completer, approval *approvals.Service, now func() time.Time, log *slog.Logger,
) *TranscriptProposer {
	return &TranscriptProposer{pool: pool, brain: brain, approval: approval, now: now, log: log}
}

// proposedStep is one next step as the model reports it.
type proposedStep struct {
	Summary     string            `json:"summary"`
	Owner       string            `json:"owner"`
	SourceLines []int             `json:"source_lines"`
	Confidence  schema.Confidence `json:"confidence"`
}

// Proposals is a POINTER so an absent key stays distinguishable from an empty
// list. "The transcript stated no next steps" is a real and common answer; a
// reply carrying no `proposals` key did not answer at all, and only the first
// of those is an outcome to record.
type transcriptPayload struct {
	Proposals *[]proposedStep `json:"proposals"`
}

func (p transcriptPayload) proposals() []proposedStep {
	if p.Proposals == nil {
		return nil
	}
	return *p.Proposals
}

// errRefusedTranscript is terminal for this reading: the model answered with
// something this site may not act on. It fails the READ, not the job — a
// retry would ask the same question of the same text and get the same answer.
var errRefusedTranscript = errors.New("compose: the reading could not be used")

// transcriptRequest builds the model call for one transcript.
//
// It is a PURE function of the lines so the certification case can issue the
// SHIPPING request rather than a copy of it — a cert that grades a
// hand-rewritten prompt certifies nothing about what runs.
func transcriptRequest(lines []string) model.Request {
	fence := promptfence.New()
	var prompt strings.Builder
	prompt.WriteString("One meeting transcript, in order (untrusted). Each line is numbered:\n")
	for i, line := range lines {
		prompt.WriteString(fence.WrapAttr("line", strconv.Itoa(i+1), line) + "\n")
	}
	fmt.Fprintf(&prompt,
		`Return JSON: { "proposals": [ { "summary", "owner", "source_lines", "confidence" } ] } — `+
			`at most %d, and an empty list when the transcript states none. `+
			`"summary" is one plain sentence naming the thing to be done. `+
			`"owner" is the party who said they would do it, as the transcript names them. `+
			`"source_lines" are the line numbers it is stated on, between 1 and %d.`,
		maxTranscriptProposals, len(lines))

	return model.Request{
		System:         transcriptSystemFor(fence),
		Messages:       []model.Message{{Role: chatRoleUser, Content: prompt.String()}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: transcriptSchema(),
		SecretStripper: ai.NewSecretStripper(),
	}
}

// transcriptSchema is the generation-time shape guardrail.
func transcriptSchema() json.RawMessage {
	return schema.Must(schema.Object(
		map[string]schema.Node{
			"proposals": schema.Array(schema.Object(
				map[string]schema.Node{
					"summary":               schema.String(),
					"owner":                 schema.String(),
					"source_lines":          schema.Array(schema.Number()),
					extractionConfidenceKey: schema.Number(),
				},
				"summary", "owner", "source_lines", extractionConfidenceKey,
			)),
		},
		"proposals",
	))
}

// transcriptShapeValid is the §5.2 validator: the cap respected, every cited
// line one this call actually supplied. The citation check is the whole point —
// a proposal that cannot point at a line it read is a guess, and staging a
// guess with a citation shaped like evidence is worse than staging nothing.
func transcriptShapeValid(lineCount int) ai.Validator {
	return func(text string) error {
		var payload transcriptPayload
		if err := json.Unmarshal([]byte(ai.Unfence(text)), &payload); err != nil {
			return fmt.Errorf("output is not the required JSON shape: %w", err)
		}
		if msg := validateTranscriptPayload(payload, lineCount); msg != "" {
			return errors.New(msg)
		}
		return nil
	}
}

// validateTranscriptPayload names the first fidelity violation, or "" when the
// payload is one this site may act on.
func validateTranscriptPayload(payload transcriptPayload, lineCount int) string {
	if payload.Proposals == nil {
		return "the reply carries no proposals key, so it did not answer the question"
	}
	proposals := payload.proposals()
	if len(proposals) > maxTranscriptProposals {
		return fmt.Sprintf("the transcript yielded %d next steps, and at most %d may be proposed",
			len(proposals), maxTranscriptProposals)
	}
	for _, step := range proposals {
		if msg := validateProposedStep(step, lineCount); msg != "" {
			return msg
		}
	}
	return ""
}

// validateProposedStep holds one proposal to what a transcript can support.
// Every echoed token is MODEL output — a speaker who got the model to obey can
// choose it — so anything that reaches a log or a retry prompt is bounded.
func validateProposedStep(step proposedStep, lineCount int) string {
	switch {
	case strings.TrimSpace(step.Summary) == "":
		return "a proposal carries no summary, so nothing in the inbox would say what was promised"
	case strings.TrimSpace(step.Owner) == "":
		return "a proposal names no owner, so nobody would know whose commitment it was"
	case len(step.SourceLines) == 0:
		return fmt.Sprintf("proposal %q cites no line, and an uncited next step is a guess",
			clampToken(step.Summary))
	case len(step.SourceLines) > maxTranscriptCitedLines:
		return fmt.Sprintf("proposal %q cites %d lines, and at most %d may be cited",
			clampToken(step.Summary), len(step.SourceLines), maxTranscriptCitedLines)
	case step.Confidence < 0 || step.Confidence > 1:
		return fmt.Sprintf("confidence %v is outside [0,1]", step.Confidence)
	}
	for _, line := range step.SourceLines {
		if line < 1 || line > lineCount {
			return fmt.Sprintf("proposal cites line %d, and this transcript has lines 1 to %d",
				line, lineCount)
		}
	}
	return ""
}

// ask puts one transcript to the model and returns what it may act on.
func (p *TranscriptProposer) ask(ctx context.Context, lines []string) ([]proposedStep, error) {
	req := transcriptRequest(lines)
	validate := transcriptShapeValid(len(lines))
	var resp model.Response
	var err error
	if structured, ok := p.brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, validate)
	} else {
		resp, err = p.brain.Complete(ctx, req)
	}
	if err != nil {
		if errors.Is(err, ai.ErrOutputRejected) {
			return nil, fmt.Errorf("%w: %w", errRefusedTranscript, err)
		}
		return nil, err
	}
	// Re-parsed and re-validated even when CompleteValidated already ran the
	// validator: a bare completer (the offline fake, a role wired without the
	// structured lane) does not, and this is the only floor those paths have.
	var payload transcriptPayload
	if err := json.Unmarshal([]byte(ai.Unfence(resp.Text)), &payload); err != nil {
		return nil, fmt.Errorf("%w: unparseable model output: %w", errRefusedTranscript, err)
	}
	if msg := validateTranscriptPayload(payload, len(lines)); msg != "" {
		return nil, fmt.Errorf("%w: %s", errRefusedTranscript, msg)
	}
	return payload.proposals(), nil
}

// aboveFloor drops the proposals not worth a human's attention.
//
// The floor is applied HERE and not in the validator on purpose: the validator
// answers "may this site act on the reply at all", and a well-formed but unsure
// reading is a valid reply that simply does not earn a question. Keeping the
// two apart is also what lets the certification case grade the raw reading
// instead of grading this policy.
func aboveFloor(steps []proposedStep) []proposedStep {
	kept := make([]proposedStep, 0, len(steps))
	for _, step := range steps {
		if step.Confidence >= transcriptConfidenceFloor {
			kept = append(kept, step)
		}
	}
	return kept
}

// transcriptTooLongError explains a refusal a rep can act on, naming the size
// of the thing they gave and the size a reading addresses.
type transcriptTooLongError struct {
	lines, chars int
}

func (e transcriptTooLongError) Error() string {
	return fmt.Sprintf(
		"this transcript is %d lines / %d characters, and one reading addresses at most %d lines / %d characters; "+
			"log the meeting as more than one transcript and read each",
		e.lines, e.chars, maxTranscriptPromptLines, maxTranscriptPromptChars)
}

// withinReadingBounds refuses a transcript larger than one reading covers. It
// refuses rather than truncating because a proposal set covering the first half
// of a meeting is indistinguishable from one covering all of it.
func withinReadingBounds(lines []string) error {
	chars := 0
	for _, line := range lines {
		chars += len(line) + 1
	}
	if len(lines) > maxTranscriptPromptLines || chars > maxTranscriptPromptChars {
		return transcriptTooLongError{lines: len(lines), chars: chars}
	}
	return nil
}

// stepEvidence renders the lines one proposal was read from as the approval's
// evidence, quoting the transcript rather than the model's paraphrase of it —
// the point is to show the human what the text actually says.
func stepEvidence(step proposedStep, lines []string, activityID ids.ActivityID) approvals.Evidence {
	quoted := make([]string, 0, len(step.SourceLines))
	for _, line := range step.SourceLines {
		quoted = append(quoted, lines[line-1])
	}
	snippet := strings.Join(quoted, "\n")
	if len(snippet) > maxEvidenceSnippetChars {
		snippet = snippet[:maxEvidenceSnippetChars]
	}
	return approvals.Evidence{
		Snippet:     snippet,
		SourceType:  string(recordTypeActivity),
		SourceID:    activityID.UUID,
		SourceLines: step.SourceLines,
	}
}

// maxEvidenceSnippetChars mirrors the approvals module's own snippet cap, so a
// long cited line is trimmed here with intent rather than refused there as a
// staging error the rep can do nothing about.
const maxEvidenceSnippetChars = 500

// transcriptReadStore is the slice of the activities store this engine drives.
// A narrow interface because the engine's whole relationship with the module is
// these three calls, and naming them is what says the engine owns no rows.
type transcriptReadStore interface {
	BeginTranscriptRead(ctx context.Context, readID ids.UUID) (activities.TranscriptRead, error)
	ReadTranscript(ctx context.Context, activityID ids.ActivityID) (activities.TranscriptReading, error)
	FinishTranscriptRead(ctx context.Context, readID ids.UUID, outcome activities.TranscriptReadOutcome) error
}
