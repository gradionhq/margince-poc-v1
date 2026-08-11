// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package runner is the Surface-B reason-act-observe loop
// (architecture/07): the model PROPOSES, the governed tool surface
// DECIDES. The runner reaches every action through the same
// Registry.Invoke an inbound A2 agent hits — no back door, no
// privileged registry, one audit stream (the two-directions invariant,
// ADR-0009 Decision 5). A 🟡 refusal suspends the run on the staged
// approval; scope and budget refusals are fed back as observations so
// the model re-plans within authority.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// Invoker is the runner's ONLY path to an action: the governed tool
// surface. agents.Registry satisfies it; nothing else may.
type Invoker interface {
	Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
	// Offered is the catalog THIS caller may invoke — the scope-filtered set,
	// which is what the run is shown. A run offered a verb its passport cannot
	// spend is being asked to choose among names it will be refused for, and
	// the whole listing rides in the system prompt, which elision never touches.
	Offered(ctx context.Context) []mcp.ToolSpec
	// Specs is the WHOLE catalog, and it is a separate question. It names which
	// tools an observation may be attributed to, which is about what already
	// happened rather than what may be chosen next — see sourceVocabulary.
	Specs() []mcp.ToolSpec
}

// Meta names the model identity a completion was served with — the
// RUNNER-AC-4 replay evidence the trace step records.
type Meta struct {
	ModelID string
	Tier    string
}

// Brain is one completion call. Compose adapts ai.Router into this so
// the runner rides tiered routing, budget bands and secret-stripping
// without importing a sibling module; Meta carries the served model
// identity so the trace records what answered without re-calling it.
type Brain interface {
	Complete(ctx context.Context, req model.Request) (model.Response, Meta, error)
}

type Outcome string

const (
	OutcomeCompleted        Outcome = "completed"
	OutcomeDegraded         Outcome = "degraded"
	OutcomeAwaitingApproval Outcome = "awaiting_approval"
)

// Result is what a run produced. Degraded runs still carry the best
// partial state (§4 — budget exhaustion is not a crash), and a
// suspended run carries everything needed to resume.
type Result struct {
	Outcome       Outcome
	Final         json.RawMessage
	DegradeReason string
	Pending       *Pending
	Steps         []Step
	StepsUsed     int
	OutputTokens  int
}

// Step is one trace entry: proposal → admission outcome → observation,
// plus the model identity and per-step spend the run was served with. The
// ordered list is the §6 replayable record — RUNNER-AC-4 requires it to
// reconstruct model identity WITHOUT re-calling the model.
type Step struct {
	Tool        string
	Args        json.RawMessage
	Observation string
	ModelID     string
	Tier        string
	TokensIn    int
	TokensOut   int
	Admission   string // "executed" | "refused" | "staged" | "rejected"
}

// Pending snapshots a run suspended on a 🟡 staging: the approval to
// watch, the exact call to re-submit, the window to resume from, and
// the budget already consumed (the resumed run continues the SAME
// budget — suspension is not a refill).
type Pending struct {
	ApprovalID ids.ApprovalID
	Tool       string
	Args       json.RawMessage
	Window     []model.Message
	// Fence is the boundary the window's untrusted spans were written
	// with. It travels WITH the window: a resumed run that minted a
	// fresh marker would be telling the model to honour a boundary its
	// own stored text does not carry.
	Fence promptfence.Fence
	// TranscriptVersion records the rules the stored Window's spans were
	// written under. The fence alone does not say whether they hold: a
	// transcript bounded with Wrap rather than WrapAuthored carries a marker
	// the model was free to close, so it may ALREADY contain prompt-voice
	// text inside what looks like a span. Absent (zero) means it predates
	// that fix and cannot be told apart from an escaped one after the fact.
	TranscriptVersion int
	StepsUsed         int
	OutputTokens      int
}

// neutralisedObservations is the current transcript version: every observation
// bounded with [promptfence.Fence.WrapAuthored], so nothing the model wrote can
// end its own span. Bump this whenever a change makes an OLD transcript unsafe
// to resume — the resume path refuses anything older rather than guessing.
const neutralisedObservations = 1

type Runner struct {
	tools Invoker
	brain Brain
}

func New(tools Invoker, brain Brain) *Runner {
	return &Runner{tools: tools, brain: brain}
}

// Run executes a fresh job until terminal answer, suspension, or a
// budget guarantee fires.
func (r *Runner) Run(ctx context.Context, job Job) (Result, error) {
	admitted := r.tools.Offered(ctx)
	if missing := unfundedTools(job, admitted); len(missing) > 0 {
		// Before the first completion, so a misconfigured agent costs no
		// model spend on its way to failing.
		return r.degrade(Result{}, "this agent's passport does not admit "+
			strings.Join(missing, ", ")+" — grant the scope those tools need, or narrow the agent's catalog entry"), nil
	}
	win := newWindow(job, offeredToJob(job, admitted), r.tools.Specs())
	return r.loop(ctx, job, win, Result{})
}

// Decision is a human approval outcome fed back into a suspended run.
type Decision struct {
	Pending  Pending
	Approved bool
}

// Resume continues a suspended run. Approved: the identical staged call
// is re-submitted with the approval id — the gate re-validates against
// the CURRENT target (version skew rejects; the world cannot have
// silently changed under an approved diff). Rejected: the refusal is
// observed and the model re-plans without that action.
func (r *Runner) Resume(ctx context.Context, job Job, dec Decision) (Result, error) {
	admitted := r.tools.Offered(ctx)
	// The same shortfall check Run makes, at the same strength. A resumed run
	// whose entry the passport can no longer fund is as misconfigured as a
	// fresh one, and a half-authorised resume is the worse of the two: it
	// carries a transcript that reads like progress.
	if missing := unfundedTools(job, admitted); len(missing) > 0 {
		return r.degrade(Result{StepsUsed: dec.Pending.StepsUsed, OutputTokens: dec.Pending.OutputTokens},
			"this agent's passport does not admit "+strings.Join(missing, ", ")+
				" — the run cannot resume under an entry its passport cannot fund"), nil
	}
	win, err := windowFromSnapshot(job, offeredToJob(job, admitted), r.tools.Specs(),
		dec.Pending.Window, dec.Pending.Fence, dec.Pending.TranscriptVersion)
	if err != nil {
		return Result{}, err
	}
	carried := Result{StepsUsed: dec.Pending.StepsUsed, OutputTokens: dec.Pending.OutputTokens}

	if !dec.Approved {
		win.observeThen(dec.Pending.Tool, "", "the human REJECTED this proposed action; re-plan without it")
		// A rejection is a human action, not a model call: ModelID/Tier stay
		// empty and tokens zero — the trace records honestly that no model
		// served this step.
		carried.Steps = append(carried.Steps, Step{
			Tool: dec.Pending.Tool, Args: dec.Pending.Args, Observation: "rejected by human",
			Admission: "rejected",
		})
		return r.loop(ctx, job, win, carried)
	}

	args, err := withApprovalID(dec.Pending.Args, dec.Pending.ApprovalID)
	if err != nil {
		return Result{}, err
	}
	// The allowlist is re-checked HERE and not only in the loop. A staged
	// call redeems before the resumed loop takes its first step, so a tool
	// the catalog entry no longer names would otherwise execute on the
	// strength of an approval granted while it still did. The human's yes
	// authorised an action, never an authority that outlives the entry —
	// the same posture Resume already takes when the passport died while
	// the run was parked.
	out, err := r.invokePermitted(ctx, job, dec.Pending.Tool, args)
	observation := string(out)
	admission := "executed"
	if err != nil {
		// Version skew, expiry, or any other redemption failure is an
		// observation, not a crash: the model re-plans against current
		// state (a re-staging is a fresh human decision). The step records
		// "refused" — replay must never claim a mutation that the gate did
		// not apply.
		observation = "approved action could not be applied: " + err.Error()
		admission = "refused"
	}
	win.observe(dec.Pending.Tool, observation)
	// The approved staged call redeems here with no fresh model completion —
	// the model identity was recorded on the step that proposed it before
	// suspension, so this redemption step leaves ModelID/Tier empty and
	// tokens zero rather than re-attributing a call that never happened.
	carried.Steps = append(carried.Steps, Step{
		Tool: dec.Pending.Tool, Args: dec.Pending.Args, Observation: truncate(observation),
		Admission: admission,
	})
	return r.loop(ctx, job, win, carried)
}

// observeRefusal feeds a refusal back as an observation and returns the
// trace step for it. A DECLARED capability gap is called out as terminal,
// because it is not a fault the model can route around by trying again — and
// this is the loop with a step budget, so a re-plan that re-calls the same
// tool spends the run on a permanent no.
func observeRefusal(win *window, step modelStep, err error, meta Meta, resp model.Response) Step {
	observation := "tool call refused: " + err.Error()
	// Telling the model not to retry is an ORDER, so it rides the directive
	// argument: inside the fence it would be text the prompt has already
	// declared to be data the model must disregard (observeThen's own doc). The
	// trace keeps both halves joined — a trace is a record of what happened,
	// not a prompt.
	directive := ""
	switch {
	case errors.Is(err, apperrors.ErrUnsupportedBySoR):
		directive = "this workspace's system of record cannot serve this tool at all; do not call it again in this run"
	case errors.Is(err, errOutsideAgentSpec):
		// Permanent for the same reason and for a different cause: the
		// allowlist is code, so no re-plan reaches it within this run.
		directive = "this tool is outside what this agent may do; do not call it again in this run"
	}
	win.observeThen(step.Tool, observation, directive)
	// Reserve the directive's room inside the cap: provider text whose LENGTH is
	// influenceable by mirrored content must neither crowd "this was terminal"
	// out of the trace nor grow the entry past the bound.
	suffix := ""
	if directive != "" {
		suffix = " — " + directive
	}
	recorded := truncateTo(observation, traceObservationLimit-len(suffix)) + suffix
	return Step{
		Tool: step.Tool, Args: step.Args, Observation: recorded,
		ModelID: meta.ModelID, Tier: meta.Tier, TokensIn: resp.InputTokens, TokensOut: resp.OutputTokens,
		Admission: "refused",
	}
}

// consecutiveInvalidLimit ends a run whose model cannot produce a valid
// step: retry-with-error-feedback twice, then degrade honestly
// (ai-operational-spec §5.2 — never a partial fabrication).
const consecutiveInvalidLimit = 3

// maxToolNameLen bounds a proposed tool name. Generous next to the longest
// registered name, short enough that the field cannot carry prose.
const maxToolNameLen = 64

func (r *Runner) loop(ctx context.Context, job Job, win *window, acc Result) (Result, error) {
	budget := job.Budget.withDefaults()
	invalidStreak := 0
	for {
		if err := ctx.Err(); err != nil {
			// Wall clock is the third guarantee (§4): the scheduler cancels
			// the context and the loop unwinds here.
			return r.degrade(acc, "wall clock exceeded: "+err.Error()), nil
		}
		if acc.StepsUsed >= budget.MaxSteps {
			return r.degrade(acc, "step budget exhausted"), nil
		}
		if acc.OutputTokens >= budget.MaxOutputTokens {
			return r.degrade(acc, "output token budget exhausted"), nil
		}
		acc.StepsUsed++

		resp, meta, err := r.brain.Complete(ctx, win.asRequest(budget.MaxOutputTokens-acc.OutputTokens))
		if err != nil {
			return r.degrade(acc, "model call failed: "+err.Error()), nil
		}
		acc.OutputTokens += resp.OutputTokens

		step, parseErr := parseStep(resp.Text)
		if parseErr != nil {
			invalidStreak++
			if invalidStreak >= consecutiveInvalidLimit {
				return r.degrade(acc, "model output failed validation "+fmt.Sprint(invalidStreak)+" times: "+parseErr.Error()), nil
			}
			win.observeThen(outputValidatorSource, "your previous output failed validation: "+parseErr.Error(), "Return ONLY the step JSON.")
			continue
		}
		invalidStreak = 0

		if step.Final != nil {
			acc.Outcome = OutcomeCompleted
			acc.Final = step.Final
			return acc, nil
		}

		out, err := r.invokePermitted(ctx, job, step.Tool, step.Args)
		var staged *workflow.StagedApprovalError
		switch {
		case errors.As(err, &staged):
			// 🟡 mid-loop: the proposal is durably staged; suspend, never
			// block (§5). The snapshot makes the run resumable.
			return suspend(acc, staged.ApprovalID, step, win, meta, resp), nil
		case err != nil:
			// Refusals (scope, tier, seat, unknown tool, bad args) feed
			// back as observations — the model learns it cannot do that
			// and re-plans; agent ≤ human holds without the loop knowing
			// the policy.
			acc.Steps = append(acc.Steps, observeRefusal(win, step, err, meta, resp))
		default:
			win.observe(step.Tool, string(out))
			acc.Steps = append(acc.Steps, Step{
				Tool: step.Tool, Args: step.Args, Observation: truncate(string(out)),
				ModelID: meta.ModelID, Tier: meta.Tier, TokensIn: resp.InputTokens, TokensOut: resp.OutputTokens,
				Admission: "executed",
			})
		}
	}
}

// suspend records the staged proposal and the state a Resume needs: the
// transcript, the boundary that transcript's untrusted spans were written
// with, and the budget already spent — suspension is not a refill.
func suspend(acc Result, approvalID ids.ApprovalID, step modelStep, win *window, meta Meta, resp model.Response) Result {
	acc.Outcome = OutcomeAwaitingApproval
	acc.Steps = append(acc.Steps, Step{
		Tool: step.Tool, Args: step.Args, Observation: "staged for approval " + approvalID.String(),
		ModelID: meta.ModelID, Tier: meta.Tier, TokensIn: resp.InputTokens, TokensOut: resp.OutputTokens,
		Admission: "staged",
	})
	acc.Pending = &Pending{
		ApprovalID:        approvalID,
		Tool:              step.Tool,
		Args:              step.Args,
		Window:            win.snapshot(),
		Fence:             win.fence,
		TranscriptVersion: neutralisedObservations,
		StepsUsed:         acc.StepsUsed,
		OutputTokens:      acc.OutputTokens,
	}
	return acc
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

// modelStep is the step protocol: exactly one of tool-call or final.
type modelStep struct {
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args"`
	Final json.RawMessage `json:"final"`
}

func parseStep(text string) (modelStep, error) {
	cleaned := strings.TrimSpace(text)
	// Models under JSON-only instructions still fence habitually; strip
	// a well-formed fence rather than failing the step over formatting.
	if after, found := strings.CutPrefix(cleaned, "```json"); found {
		cleaned = after
	} else if after, found := strings.CutPrefix(cleaned, "```"); found {
		cleaned = after
	}
	cleaned = strings.TrimSuffix(strings.TrimSpace(cleaned), "```")

	var step modelStep
	dec := json.NewDecoder(strings.NewReader(cleaned))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&step); err != nil {
		return modelStep{}, fmt.Errorf(`expected {"tool":..., "args":{...}} or {"final":{...}}: %w`, err)
	}
	hasTool := step.Tool != ""
	hasFinal := step.Final != nil
	if hasTool == hasFinal {
		return modelStep{}, errors.New(`exactly one of "tool" or "final" must be set`)
	}
	if hasTool && len(step.Tool) > maxToolNameLen {
		// A tool name is a registry identifier, so a long one is not a typo —
		// it is the model writing a payload into a field the trace persists and
		// the refusal path echoes. Bound it here, at the one place model output
		// becomes a step, rather than at each place it is later printed.
		return modelStep{}, fmt.Errorf("tool name is longer than %d characters", maxToolNameLen)
	}
	if hasTool && step.Args == nil {
		step.Args = json.RawMessage(`{}`)
	}
	return step, nil
}

// withApprovalID re-forms the staged call with the redemption id — the
// same canonical bytes plus approval_id, exactly what a human-driven
// retry would send.
func withApprovalID(args json.RawMessage, id ids.ApprovalID) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return nil, fmt.Errorf("runner: pending args: %w", err)
	}
	m["approval_id"] = id.String()
	return json.Marshal(m)
}

// traceObservationLimit bounds trace observations: the trace is a record of
// what happened, not a second copy of every payload.
const traceObservationLimit = 2000

// truncationMarker names the elision, and its own length counts against the
// cap — a caller reserving room for a suffix has to reserve for this too.
const truncationMarker = "…[truncated]"

// truncate bounds one trace observation at the default limit.
func truncate(s string) string { return truncateTo(s, traceObservationLimit) }

// truncateTo bounds s so the RESULT is at most limit bytes, marker included —
// which is what lets a caller reserve room for a suffix inside the same cap
// rather than overrun it. The bound holds for every limit, including one too
// small to carry the marker: a function that exists to enforce a cap must not
// exceed it while saying it does.
func truncateTo(s string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if len(s) <= limit {
		return s
	}
	if limit <= len(truncationMarker) {
		// No room to say "elided" without breaking the bound; the bound wins.
		return s[:limit]
	}
	return s[:limit-len(truncationMarker)] + truncationMarker
}
