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

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// Invoker is the runner's ONLY path to an action: the governed tool
// surface. agents.Registry satisfies it; nothing else may.
type Invoker interface {
	Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
	Specs() []mcp.ToolSpec
}

// Brain is one completion call. Compose adapts ai.Router into this so
// the runner rides tiered routing, budget bands and secret-stripping
// without importing a sibling module.
type Brain interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

// Budget bounds one run (architecture/07 §4). Both are HARD per-run
// ceilings, deliberately independent of workspace-level budgets: one
// unattended run can never claim the whole workspace budget (RT-AI-H5).
type Budget struct {
	MaxSteps        int
	MaxOutputTokens int
}

// The §4 RATIFY defaults: 40 reason-act cycles sized to one deal-bundle
// pass, 50k output tokens per run.
const (
	DefaultMaxSteps        = 40
	DefaultMaxOutputTokens = 50_000
)

func (b Budget) withDefaults() Budget {
	if b.MaxSteps <= 0 {
		b.MaxSteps = DefaultMaxSteps
	}
	if b.MaxOutputTokens <= 0 {
		b.MaxOutputTokens = DefaultMaxOutputTokens
	}
	return b
}

// Job is one runner invocation: a goal over seed grounding under a
// budget. Authority is NOT here — it rides the context principal, the
// same way every other surface carries it.
type Job struct {
	Goal       string
	TriggerRef string
	Grounding  []Grounding
	Budget     Budget
}

// Grounding is one provenance-stamped seed context item (§3): T2
// content is spotlighted as data-not-instructions before it enters the
// prompt.
type Grounding struct {
	SourceID  string
	TrustTier string // "T0" | "T1" | "T2"
	Content   string
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

// Step is one trace entry: proposal → admission outcome → observation.
// The ordered list is the §6 replayable record of what the run did.
type Step struct {
	Tool        string
	Args        json.RawMessage
	Observation string
}

// Pending snapshots a run suspended on a 🟡 staging: the approval to
// watch, the exact call to re-submit, the window to resume from, and
// the budget already consumed (the resumed run continues the SAME
// budget — suspension is not a refill).
type Pending struct {
	ApprovalID   ids.ApprovalID
	Tool         string
	Args         json.RawMessage
	Window       []model.Message
	StepsUsed    int
	OutputTokens int
}

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
	win := newWindow(job, r.tools.Specs())
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
	win := windowFromSnapshot(job, r.tools.Specs(), dec.Pending.Window)
	carried := Result{StepsUsed: dec.Pending.StepsUsed, OutputTokens: dec.Pending.OutputTokens}

	if !dec.Approved {
		win.observe(dec.Pending.Tool, "the human REJECTED this proposed action; re-plan without it")
		carried.Steps = append(carried.Steps, Step{
			Tool: dec.Pending.Tool, Args: dec.Pending.Args, Observation: "rejected by human",
		})
		return r.loop(ctx, job, win, carried)
	}

	args, err := withApprovalID(dec.Pending.Args, dec.Pending.ApprovalID)
	if err != nil {
		return Result{}, err
	}
	out, err := r.tools.Invoke(ctx, dec.Pending.Tool, args)
	observation := string(out)
	if err != nil {
		// Version skew, expiry, or any other redemption failure is an
		// observation, not a crash: the model re-plans against current
		// state (a re-staging is a fresh human decision).
		observation = "approved action could not be applied: " + err.Error()
	}
	win.observe(dec.Pending.Tool, observation)
	carried.Steps = append(carried.Steps, Step{Tool: dec.Pending.Tool, Args: dec.Pending.Args, Observation: truncate(observation)})
	return r.loop(ctx, job, win, carried)
}

// consecutiveInvalidLimit ends a run whose model cannot produce a valid
// step: retry-with-error-feedback twice, then degrade honestly
// (ai-operational-spec §5.2 — never a partial fabrication).
const consecutiveInvalidLimit = 3

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

		resp, err := r.brain.Complete(ctx, win.asRequest(budget.MaxOutputTokens-acc.OutputTokens))
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
			win.observe("output_validator", "your previous output failed validation: "+parseErr.Error()+"; return ONLY the step JSON")
			continue
		}
		invalidStreak = 0

		if step.Final != nil {
			acc.Outcome = OutcomeCompleted
			acc.Final = step.Final
			return acc, nil
		}

		out, err := r.tools.Invoke(ctx, step.Tool, step.Args)
		var staged *workflow.StagedApprovalError
		switch {
		case errors.As(err, &staged):
			// 🟡 mid-loop: the proposal is durably staged; suspend, never
			// block (§5). The snapshot makes the run resumable.
			acc.Outcome = OutcomeAwaitingApproval
			acc.Steps = append(acc.Steps, Step{Tool: step.Tool, Args: step.Args, Observation: "staged for approval " + staged.ApprovalID.String()})
			acc.Pending = &Pending{
				ApprovalID:   staged.ApprovalID,
				Tool:         step.Tool,
				Args:         step.Args,
				Window:       win.snapshot(),
				StepsUsed:    acc.StepsUsed,
				OutputTokens: acc.OutputTokens,
			}
			return acc, nil
		case err != nil:
			// Refusals (scope, tier, seat, unknown tool, bad args) feed
			// back as observations — the model learns it cannot do that
			// and re-plans; agent ≤ human holds without the loop knowing
			// the policy.
			observation := "tool call refused: " + err.Error()
			win.observe(step.Tool, observation)
			acc.Steps = append(acc.Steps, Step{Tool: step.Tool, Args: step.Args, Observation: truncate(observation)})
		default:
			win.observe(step.Tool, string(out))
			acc.Steps = append(acc.Steps, Step{Tool: step.Tool, Args: step.Args, Observation: truncate(string(out))})
		}
	}
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

// truncate bounds trace observations: the trace is a record of what
// happened, not a second copy of every payload.
const traceObservationLimit = 2000

func truncate(s string) string {
	if len(s) <= traceObservationLimit {
		return s
	}
	return s[:traceObservationLimit] + "…[truncated]"
}
