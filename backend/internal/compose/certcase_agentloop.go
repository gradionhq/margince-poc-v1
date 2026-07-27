// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for agent_loop/loop — the Surface-B runner's model
// turn: the window the loop builds, and the step the loop will accept back.
//
// WHAT IT EXERCISES. One turn, driven through the shipped entry point. Run calls
// runner.Run with the runner's own brain and tool seams, so the prompt is the
// loop's own — the system frame carrying this run's boundary rule and the offered
// tool list, and the goal turn carrying the seed grounding fenced by trust tier —
// and the reply is graded by parseStep, the step protocol the loop admits a
// proposal through. Nothing here re-creates any of it.
//
// WHAT IT DOES NOT EXERCISE, which is the loop. The run is bounded to the single
// turn it grades, so no observation is ever fed back, no second prompt is ever
// built, no window is ever elided, and nothing is ever executed, suspended or
// resumed. Site.CertifiedScope() already reports single_turn for this kind; this
// case is that scope and not one step more, and a record built from it may not be
// read as a claim about a run.
//
// WHY A FRESH WINDOW AND NOT A RESUMED ONE. Resume is the loop's other entry
// point and it cannot be driven honestly from a fixture. Its window is rebuilt
// from a stored transcript plus the fence that transcript's spans were written
// with, and the two must be the same fence: a case that minted a fresh marker and
// handed it a snapshot written under another would be telling the model to honour
// a boundary its own text does not carry — the exact failure windowFromSnapshot
// refuses an unminted fence to prevent. A corpus-supplied fence would put a fixed
// nonce back in the corpus, and building a correctly bounded snapshot here would
// mean re-creating the window's own observation format in this file, which is a
// copy of the thing being certified. Seed grounding is how a fresh run is given
// prior context anyway, so a window seeded that way is the shape the loop's own
// scenarios already describe. This case therefore never reaches Resume, and it
// claims nothing about resuming.
//
// WHAT THE EXPECTATION MEANS. The one thing a turn of this site decides: which
// step the model takes — call one of the tools this window offers, or answer the
// goal. That is what the step protocol itself distinguishes, so it is what a
// scenario can assert; the reasoning behind the step is prose, and pinning it
// would fail every model that reached the same step differently, which is what
// the rubric and the judge are for.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// agentLoopSite names this site in every refusal it writes, so a corpus author
// reading one knows which scenario to open.
const agentLoopSite = "agent_loop/loop"

// agentLoopFinalStep is the expectation token for the step that ends a run. It
// is the protocol's own word for it, and it is the one name a fixture may not
// give a tool — the two would be indistinguishable in an expectation.
const agentLoopFinalStep = "final"

// agentLoopFixture is ONE runner turn in exactly what the loop is handed: the
// job the catalog spec carries, the seed context retrieval returned for it, and
// the tool surface this run was offered.
//
// The grounding arrives already retrieved and the tools already registered,
// because the certified thing is the window built from them, not the search and
// the registry that produced them. What those guarantee about them is enforced at
// Prepare instead.
type agentLoopFixture struct {
	Goal       string               `json:"goal"`
	TriggerRef string               `json:"trigger_ref"`
	Grounding  []agentLoopGrounding `json:"grounding"`
	Tools      []agentLoopTool      `json:"tools"`
}

// agentLoopGrounding is one provenance-stamped seed item, in the shape retrieval
// hands it over: the evidence source, the trust tier that decides whether it
// prints raw or inside the run's boundary, and the snippet itself.
type agentLoopGrounding struct {
	SourceID  string `json:"source_id"`
	TrustTier string `json:"trust_tier"`
	Content   string `json:"content"`
}

// agentLoopTool is one offered tool as the window prints it. A registered tool
// carries admission machinery too — required scope, risk tier, its resolver —
// and none of it is here, because none of it reaches the prompt and this case
// executes nothing. A fixture carrying it would describe authority the certified
// turn never exercises.
type agentLoopTool struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// agentLoopCases serves the one site that runs a governed agent loop.
type agentLoopCases struct{}

func (agentLoopCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskAgentLoop,
		Variant: "loop",
		Kind:    ai.SiteKindAgentLoop,
	}
}

// Prepare turns one seeded window and the step the scenario expects into a
// runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (agentLoopCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f agentLoopFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", agentLoopSite, err)
	}
	if err := refuseUnrunnableAgentJob(f); err != nil {
		return nil, err
	}
	specs, err := agentLoopToolSpecs(f.Tools)
	if err != nil {
		return nil, err
	}
	// A turn that took the right step differs from one that took the wrong step
	// in that step alone, so the expectation IS that token rather than a wrapper
	// carrying it.
	var want string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("%s: the expected answer is not a step name: %w", agentLoopSite, err)
	}
	if err := refuseUnreachableAgentStep(want, specs); err != nil {
		return nil, err
	}
	return &agentLoopCase{
		job: runner.Job{
			Goal:       f.Goal,
			TriggerRef: f.TriggerRef,
			Grounding:  agentLoopSeedContext(f.Grounding),
		},
		specs:    specs,
		expected: want,
	}, nil
}

// agentLoopToolSpecs rebuilds the offered tool surface, refusing one the registry
// could never advertise. Every clause is a bound the tool surface already holds:
// a registered tool has a name, the registry holds one entry per name, and every
// entry advertises an input schema — which the window prints verbatim, so a
// fixture without one sends a prompt the product never sends.
func agentLoopToolSpecs(tools []agentLoopTool) ([]mcp.ToolSpec, error) {
	specs := make([]mcp.ToolSpec, 0, len(tools))
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		switch {
		case strings.TrimSpace(tool.Name) == "":
			return nil, fmt.Errorf(
				"%s: the fixture offers a tool with no name, and the name is the only thing a step can call it by",
				agentLoopSite)
		case seen[tool.Name]:
			return nil, fmt.Errorf(
				"%s: the fixture offers tool %q twice, and the registry holds one entry per name",
				agentLoopSite, tool.Name)
		case tool.Name == agentLoopFinalStep:
			return nil, fmt.Errorf(
				"%s: the fixture offers a tool named %q, which is the step protocol's own word for ending a run — "+
					"an expectation could not tell the two apart", agentLoopSite, agentLoopFinalStep)
		case !agentLoopSchemaObject(tool.InputSchema):
			return nil, fmt.Errorf(
				"%s: tool %q advertises no input schema object, and the window prints the schema the model has to "+
					"call it by", agentLoopSite, tool.Name)
		}
		seen[tool.Name] = true
		specs = append(specs, mcp.ToolSpec{Name: tool.Name, InputSchema: tool.InputSchema})
	}
	return specs, nil
}

// agentLoopSchemaObject reports whether an advertised schema is the JSON Schema
// object every registered tool carries. Well-formed JSON is not enough: a fixture
// that omits the field encodes as null, which parses and would print the word
// "null" into the prompt as the shape the model must call the tool by.
func agentLoopSchemaObject(schema json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(schema, &object) == nil && object != nil
}

// refuseUnrunnableAgentJob names a job the scheduler could never have handed the
// runner, and so a window the product never builds. A catalog spec carries a goal
// and an occurrence ref, and retrieval stamps every seed item with the tier that
// decides whether it prints raw or inside the boundary — an item with no tier
// would be fenced here by the same default-deny that fences an unknown one, which
// is the right behaviour for text and the wrong thing to certify as a fixture.
func refuseUnrunnableAgentJob(f agentLoopFixture) error {
	switch {
	case strings.TrimSpace(f.Goal) == "":
		return fmt.Errorf(
			"%s: the fixture carries no goal, and the goal is the prompt's only statement of what to do",
			agentLoopSite)
	case strings.TrimSpace(f.TriggerRef) == "":
		return fmt.Errorf(
			"%s: the fixture names no trigger, and every run the scheduler starts is one named occurrence",
			agentLoopSite)
	case len(f.Tools) == 0:
		return fmt.Errorf(
			"%s: the fixture offers no tools, and a run is always offered the workspace's governed tool surface",
			agentLoopSite)
	}
	for _, item := range f.Grounding {
		if strings.TrimSpace(item.TrustTier) == "" {
			return fmt.Errorf(
				"%s: seed item %q carries no trust tier, and the tier is what decides whether it enters the prompt "+
					"raw or inside the run's boundary", agentLoopSite, item.SourceID)
		}
	}
	return nil
}

// refuseUnreachableAgentStep names an expectation no reply to this window could
// satisfy. The window lists the tools it offers and the model proposes from that
// list, so an expectation naming a tool this run was never offered could only
// ever be measured as a wrong answer, for a reason that is the scenario's rather
// than the model's.
func refuseUnreachableAgentStep(want string, specs []mcp.ToolSpec) error {
	if want == agentLoopFinalStep {
		return nil
	}
	if strings.TrimSpace(want) == "" {
		return fmt.Errorf(
			"%s: the scenario names no step the turn must take, so it asserts nothing", agentLoopSite)
	}
	offered := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Name == want {
			return nil
		}
		offered = append(offered, spec.Name)
	}
	return fmt.Errorf(
		"%s: the scenario expects the turn to call %q, and this window offers %s (or %q to answer the goal)",
		agentLoopSite, want, strings.Join(offered, ", "), agentLoopFinalStep)
}

// agentLoopSeedContext re-types the fixture's seed items into the loop's own
// grounding, which is where the tier rule and the provenance-ref shape gate are
// applied — by the window, on the way into the prompt.
func agentLoopSeedContext(items []agentLoopGrounding) []runner.Grounding {
	seed := make([]runner.Grounding, 0, len(items))
	for _, item := range items {
		seed = append(seed, runner.Grounding{
			SourceID: item.SourceID, TrustTier: item.TrustTier, Content: item.Content,
		})
	}
	return seed
}

// agentLoopCase is one seeded window ready to be answered, closed over the tool
// surface the answer is judged against.
type agentLoopCase struct {
	job      runner.Job
	specs    []mcp.ToolSpec
	expected string
}

// agentLoopTurnBudget bounds a run to the single turn this site certifies.
//
// The budget is the case's, not the fixture's, because the scope is: one paid
// reply per scenario, never the two further attempts the loop makes when a reply
// will not parse — a case that let those run would certify the answer a model
// gives after being told what it got wrong rather than the answer it gives. The
// bound reaches nothing the model reads: the step count never enters a prompt,
// and the output budget it leaves defaulted is far above the per-call ceiling
// that actually sizes the request, so this turn's prompt is the one a shipped run
// sends first.
func agentLoopTurnBudget() runner.Budget { return runner.Budget{MaxSteps: 1} }

// Run drives runner.Run for one turn and records the request it issued.
//
// The loop does no I/O of its own — the window is built from the job and the
// offered specs, and every outside reach is through the two seams handed to New —
// so a run needs no database, and both seams here are the lane's: the brain
// forwards to the completer, and the tool surface stages.
//
// What the run made of the reply is not inherited: Evaluate re-runs the same
// entry point over the recorded text, so the verdict is measured through the
// shipped path rather than read off a result this file interpreted.
func (c *agentLoopCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	recorder := &agentLoopRecorder{completer: completer}
	job := c.job
	job.Budget = agentLoopTurnBudget()
	_, err := runner.New(agentLoopToolSurface{specs: c.specs}, recorder).Run(ctx, job)
	trace := aitasks.Trace{Requests: recorder.requests}
	if recorder.failed != nil {
		return trace, fmt.Errorf("%s: the model call did not complete: %w", agentLoopSite, recorder.failed)
	}
	if err != nil {
		return trace, fmt.Errorf("%s: the run never reached a reply to measure: %w", agentLoopSite, err)
	}
	trace.Output = recorder.reply
	return trace, nil
}

// agentLoopRecorder is the brain the loop reasons through: it records the request
// the loop issued and the reply it read.
//
// It reports an empty Meta because it knows nothing to put there. Meta is the
// served model identity the router stamps on a trace step, and the cert lane's
// completer is bound to one (provider, model, env) that the RECORD names — so a
// step claiming an identity this seam never resolved would be the one part of the
// trace that was invented.
type agentLoopRecorder struct {
	completer aitasks.Completer
	requests  []model.Request
	reply     string
	// failed is the completer's own failure. It is kept apart from anything the
	// loop decided because a call that never completed is the lane's problem, not
	// a measurement of the reply.
	failed error
}

func (r *agentLoopRecorder) Complete(ctx context.Context, req model.Request) (model.Response, runner.Meta, error) {
	r.requests = append(r.requests, req)
	resp, err := r.completer.Complete(ctx, req)
	if err != nil {
		r.failed = err
		return model.Response{}, runner.Meta{}, err
	}
	r.reply = resp.Text
	return resp, runner.Meta{}, nil
}

// agentLoopReplay answers with the reply the run recorded, so Evaluate reaches
// the step protocol the only way it is reachable: by running the loop.
type agentLoopReplay struct{ reply string }

func (r agentLoopReplay) Complete(context.Context, model.Request) (model.Response, runner.Meta, error) {
	return model.Response{Text: r.reply}, runner.Meta{}, nil
}

// agentLoopToolSurface is the tool surface a certification run is offered: it
// advertises the fixture's tools and applies none of them.
//
// Staging every proposal is the honest posture for a lane that holds no
// authority. A certification run has no seat, no passport and no workspace to act
// in, so nothing it proposes may execute — and staging is the loop's own answer
// for an action a human has not yet allowed, which suspends the run instead of
// pretending an outcome. It also keeps the graded turn to one turn: a refusal
// would be fed back as an observation and re-prompted, which is the loop, and the
// loop is not what this site's scenarios measure.
type agentLoopToolSurface struct{ specs []mcp.ToolSpec }

func (s agentLoopToolSurface) Specs() []mcp.ToolSpec { return s.specs }

func (agentLoopToolSurface) Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, &workflow.StagedApprovalError{ApprovalID: ids.New[ids.ApprovalKind]()}
}

// Evaluate replays the recorded reply through the same loop, which runs the step
// protocol over it in the protocol's own order, and only then asks whether the
// step it took is the step the scenario expects. The order is the meaning: a
// reply the protocol refuses has no step to disagree with.
//
// The replay runs on the loop's default budget rather than the single-turn one,
// because a reply the protocol refuses is re-prompted and the loop's own message
// about WHY only reaches the result once it gives up on that text. Handing the
// same text back until it does is what makes that message reachable; it cannot
// turn a refusal into an acceptance, since a reply the protocol accepts ends the
// replay on the first pass. The replay does no I/O, so it needs nothing from the
// run's context.
func (c *agentLoopCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	res, err := runner.New(agentLoopToolSurface{specs: c.specs}, agentLoopReplay{reply: trace.Output}).
		Run(context.Background(), c.job)
	if err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: agentLoopSite + ": the loop could not replay the reply: " + err.Error(),
		}
	}
	switch {
	case res.Outcome == runner.OutcomeCompleted:
		return c.gradeStep(agentLoopFinalStep)
	case res.Pending != nil:
		// The only thing this lane's tool surface does with a proposal is stage
		// it, so a suspended replay is a well-formed tool call and the pending
		// call names which tool.
		return c.gradeStep(res.Pending.Tool)
	default:
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: res.DegradeReason}
	}
}

// gradeStep compares the step the turn took against the step the scenario
// expects, naming both in the protocol's own vocabulary — a tool name, or the
// word that ends a run.
//
// The step the turn took is safe to quote back into a record because the protocol
// bounds it before returning it: a tool name is a registry identifier, and one
// long enough to carry prose never becomes a step at all.
func (c *agentLoopCase) gradeStep(step string) aitasks.Outcome {
	if step != c.expected {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("the turn took the step %q where the scenario expects %q", step, c.expected),
		}
	}
	return aitasks.Outcome{
		Result: aitasks.OutcomeAccepted,
		Detail: fmt.Sprintf("the turn took the step %q", step),
	}
}
