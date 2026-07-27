// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The runner: the one part of this package that actually drives model
// calls. Everything else (scenario.go, promptversion.go, score.go, record.go)
// is a pure library callable without a network or a database; this file
// wires that library to TWO DB-less ai.Router instances, assembled via
// compose.NewLocalRouterForCert (ai.NewLocalRouter over a CallRecorder
// this package supplies, called through brain.go so the raw
// model-client construction stays inside the one seam arch_test.go's
// TestNoModelClientOutsideTheGate enforces) — one
// serving the task under certification (optionally MODEL=-overridden on
// just that task's ladder), one serving the fixed cert_judge task on the
// UNMODIFIED routing config, so a candidate can never grade its own
// homework by construction.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// defaultRepeats is Repeats' fallback when a caller (the env-driven CLI
// lane) leaves it unset. Odd, per Verdict's median requirement.
const defaultRepeats = 3

// corpusVersionV1 is this generation's fixed corpus-format stamp: the
// scenario format carries no version field of its own yet, so every
// Record names the same one until a versioning scheme arrives alongside
// a real second version.
const (
	corpusVersionV1 = "v1"
)

// nowFunc is the runner's injectable clock for Record.RanAt. Run's
// signature is pinned with no Clock parameter, so this is this file's
// own seam — the same pattern ai.Router's unexported `now func()
// time.Time` field uses for the same reason: production wants
// time.Now, a test wants a fixed instant.
var nowFunc = time.Now

// RunnerConfig configures one certification run.
type RunnerConfig struct {
	// Census is this build's invocation-site registry, and the only thing that
	// turns a scenario into something runnable: every site's certification case
	// is bound there, and a run drives those cases rather than prompts of its
	// own. Required — see Run.
	Census      *aitasks.Registry
	RoutingPath string // MARGINCE_AI_ROUTING
	TaskFilter  string // MARGINCE_AICERT_TASK ("" = all tasks with a corpus)
	Override    string // MARGINCE_AICERT_MODEL "provider:model" — candidate only
	Repeats     int    // MARGINCE_AICERT_RUNS, default 3, must be odd
	RecordDir   string
	CorpusDir   string
	// TraceDir, when non-empty, turns on the opt-in payload trace
	// (MARGINCE_AICERT_TRACE): every candidate and judge call's
	// post-stripper request+response is dumped to a JSONL file under this
	// directory and its path printed to stdout. Empty = no trace.
	TraceDir string
}

// Run certifies every task named by cfg.TaskFilter (or, when empty,
// every task the corpus carries at least one scenario for): N repeats
// per scenario over a candidate router (MODEL=-overridden on just that
// task's ladder, when set) scored by a second, always-unmodified judge
// router. It writes one Record per task that reaches a verdict and
// returns every Record it wrote. A single task's certification failing
// (a corpus/config problem, or ANY candidate OR judge attempt coming
// back router-degraded) never aborts the others: that task gets no
// record, and its error is folded into the returned error (errors.Join)
// — heard, never swallowed — while every other task still gets its own
// record.
func Run(ctx context.Context, cfg RunnerConfig, log *slog.Logger) ([]Record, error) {
	// A missing census is refused here rather than tolerated per scenario: the
	// cases it binds ARE what a run certifies, so a run without one could only
	// report that it measured nothing, after paying for it.
	if cfg.Census == nil {
		return nil, errors.New("aicert: runner: no census supplied — a run drives the certification case each site binds, so RunnerConfig.Census is required")
	}
	repeats, err := repeatsOrDefault(cfg.Repeats)
	if err != nil {
		return nil, err
	}

	baseCfg, err := ai.LoadRoutingFile(cfg.RoutingPath)
	if err != nil {
		return nil, fmt.Errorf("aicert: runner: %w", err)
	}

	scenarios, err := LoadCorpus(cfg.CorpusDir, cfg.Census)
	if err != nil {
		return nil, fmt.Errorf("aicert: runner: %w", err)
	}

	byTask := groupByTask(scenarios, cfg.TaskFilter)
	if cfg.TaskFilter != "" && len(byTask) == 0 {
		return nil, fmt.Errorf("aicert: runner: task %q has no scenarios under %s", cfg.TaskFilter, cfg.CorpusDir)
	}

	ctx = ensureWorkspace(ctx)

	// TraceDir empty ⇒ tracing off: trace stays nil and every method no-ops.
	var trace *payloadTrace
	if cfg.TraceDir != "" {
		trace, err = openPayloadTrace(cfg.TraceDir, nowFunc().UTC().Format("20060102T150405Z"))
		if err != nil {
			return nil, fmt.Errorf("aicert: runner: %w", err)
		}
	}
	defer func() {
		if cerr := trace.close(); cerr != nil {
			log.WarnContext(ctx, "aicert: closing payload trace", "err", cerr)
		}
	}()

	var records []Record
	var runErrs []error
	for _, task := range sortedTasks(byTask) {
		rec, err := certifyTask(ctx, task, byTask[task], cfg.Census, baseCfg, cfg.Override, repeats, log, &certifyHooks{trace: trace})
		if err != nil {
			log.ErrorContext(ctx, "aicert: task certification failed — no record written", "task", string(task), "err", err)
			runErrs = append(runErrs, fmt.Errorf("task %s: %w", task, err))
			continue
		}
		if err := WriteRecord(cfg.RecordDir, rec); err != nil {
			runErrs = append(runErrs, fmt.Errorf("task %s: writing record: %w", task, err))
			continue
		}
		records = append(records, rec)
	}
	return records, errors.Join(runErrs...)
}

// certifyHooks is the injection seam for certifyTask's two router
// constructions and the per-run payload trace both routers feed. The
// candidate/judge LocalOption lists let this package's own tests reach in —
// a scripted *ai.FakeClient via ai.WithFakeClient, a starved
// ai.WithMonthlyBudget to force a deterministic degrade — none of which
// RunnerConfig's pinned shape has room for. trace is the one field a real
// run sets: Run passes &certifyHooks{trace: t} (t nil unless
// MARGINCE_AICERT_TRACE named a directory), the tests leave it nil. This
// mirrors ai.assembleRouter: "the seam unit tests inject fakes through."
type certifyHooks struct {
	candidateOpts []ai.LocalOption
	judgeOpts     []ai.LocalOption
	trace         *payloadTrace
}

// certifyTask runs every scenario for one task over a fresh
// candidate/judge router pair and folds the outcome into one Record.
func certifyTask(ctx context.Context, task ai.Task, scenarios []Scenario, census *aitasks.Registry, baseCfg ai.RoutingConfig, override string, repeats int, log *slog.Logger, hooks *certifyHooks) (Record, error) {
	candidateCfg, err := overrideForTask(baseCfg, task, override)
	if err != nil {
		return Record{}, err
	}
	var candidateExtra, judgeExtra []ai.LocalOption
	var trace *payloadTrace
	if hooks != nil {
		candidateExtra, judgeExtra, trace = hooks.candidateOpts, hooks.judgeOpts, hooks.trace
	}
	// Capture the post-stripper bodies only when a trace will consume them —
	// otherwise the router pays the marshal+strip cost for content nothing reads.
	if trace != nil {
		candidateExtra = append(candidateExtra, ai.WithPayloadCapture())
		judgeExtra = append(judgeExtra, ai.WithPayloadCapture())
	}

	candidateRec := newTraceRecorder()
	candidateOpts := append([]ai.LocalOption{ai.WithoutResultCache(), ai.WithCallStore(candidateRec)}, candidateExtra...)
	candidateRouter, err := compose.NewLocalRouterForCert(candidateCfg, candidateOpts...)
	if err != nil {
		return Record{}, fmt.Errorf("aicert: task %s: candidate router: %w", task, err)
	}

	// The judge NEVER rides the override — grading the candidate with the
	// candidate's own binding would let a MODEL= override judge itself by
	// construction, defeating the whole point of a second router.
	judgeRec := newTraceRecorder()
	judgeOpts := append([]ai.LocalOption{ai.WithoutResultCache(), ai.WithCallStore(judgeRec)}, judgeExtra...)
	judgeRouter, err := compose.NewLocalRouterForCert(baseCfg, judgeOpts...)
	if err != nil {
		return Record{}, fmt.Errorf("aicert: task %s: judge router: %w", task, err)
	}

	acc := &taskAccumulation{selfJudgedEveryRun: true}
	taskVerdict := VerdictCertified // folded down to the worst scenario verdict below

	for _, sc := range scenarios {
		scenarioVerdict, err := runScenario(ctx, task, sc, census, repeats, candidateRouter, candidateRec, judgeRouter, judgeRec, log, acc, trace)
		if err != nil {
			return Record{}, err
		}
		taskVerdict = worstVerdict(taskVerdict, scenarioVerdict)
	}

	reliability := float64(acc.passed) / float64(len(acc.allResults))
	return buildRecord(task, taskVerdict, acc.certifiedScope, reliability, acc.allResults, acc.latencies,
		acc.tokensInTotal, acc.tokensOutTotal, acc.cachedTokensTotal, acc.cacheWriteTokensTotal,
		acc.provider, acc.servedModel, acc.identitySource, acc.judgeServedModel, acc.selfJudgedEveryRun,
		baseCfg, PromptVersion(scenarios)), nil
}

// taskAccumulation collects the pooled stats certifyTask folds across
// every scenario's repeats for buildRecord, plus the I2 served-identity
// uniformity state: the first run's candidate provider/model is the
// task's baseline, and every later run must match it exactly. A mid-set
// ladder fallback (a transient provider error on any repeat serving that
// run from a DIFFERENT rung's model) must void the whole record rather
// than let it certify "task x provider x model" over scores partly
// produced by another model.
type taskAccumulation struct {
	allResults []RunResult
	latencies  []int64
	// tokensInTotal/tokensOutTotal/cachedTokensTotal/cacheWriteTokensTotal
	// are the pooled per-bucket sums across every run. buildRecord derives
	// MeanTokens from tokensInTotal+tokensOutTotal (an exact sum, so it
	// matches this package's pre-bucketed MeanTokens arithmetic bit for
	// bit) and each MeanBucket from its own total, independently.
	tokensInTotal, tokensOutTotal                           int
	cachedTokensTotal, cacheWriteTokensTotal                int
	passed                                                  int
	provider, servedModel, identitySource, judgeServedModel string
	selfJudgedEveryRun                                      bool
	identitySet                                             bool
	// certifiedScope is the narrowest scope any run's site covered. A task is
	// one record but not always one site — cold_start ships a one-shot
	// extraction beside three multi-turn conversations — so the record may
	// claim only what its weakest site proved.
	certifiedScope string
}

// addRun folds one scored run into acc, first checking outcome's candidate
// identity against the task's baseline (the first run recorded). Returns
// an error — voiding the whole task's record — when a later run's
// provider or served model diverges from that baseline.
func (acc *taskAccumulation) addRun(task ai.Task, sc Scenario, runIndex int, outcome runOutcome) error {
	if acc.identitySet && (outcome.Provider != acc.provider || outcome.ServedModel != acc.servedModel) {
		return fmt.Errorf(
			"aicert: task %s scenario %s run %d: candidate served by %s:%s, but run 1 was served by %s:%s — refusing to certify a mixed run set",
			task, sc.Name, runIndex+1, outcome.Provider, outcome.ServedModel, acc.provider, acc.servedModel)
	}
	acc.allResults = append(acc.allResults, outcome.RunResult)
	acc.latencies = append(acc.latencies, outcome.LatencyMS)
	acc.tokensInTotal += outcome.TokensIn
	acc.tokensOutTotal += outcome.TokensOut
	acc.cachedTokensTotal += outcome.CachedTokens
	acc.cacheWriteTokensTotal += outcome.CacheWriteTokens
	acc.provider, acc.servedModel, acc.identitySource = outcome.Provider, outcome.ServedModel, outcome.ServedIdentitySource
	acc.identitySet = true
	acc.judgeServedModel = outcome.JudgeServedModel
	acc.certifiedScope = narrowerScope(acc.certifiedScope, outcome.CertifiedScope)
	if !selfJudged(outcome.ServedModel, outcome.JudgeServedModel) {
		acc.selfJudgedEveryRun = false
	}
	if outcome.HardPass {
		acc.passed++
	}
	return nil
}

// narrowerScope folds two certified scopes to the less complete of the two, so
// a pooled record claims only what every run it pooled actually proved. The
// zero value carries no claim at all and yields to whatever the first run
// covered.
func narrowerScope(a, b string) string {
	if a == "" {
		return b // the accumulator's zero value carries no claim to fold yet.
	}
	if a == aitasks.ScopeSingleTurn || b == aitasks.ScopeSingleTurn {
		return aitasks.ScopeSingleTurn
	}
	return a
}

// runScenario drives repeats runs of one scenario, folding each into acc,
// and returns the scenario's own verdict for certifyTask to fold into the
// task's worst-case verdict. Split out of certifyTask so the per-run
// degrade/uniformity gates and the per-scenario verdict fold live on
// their own function, not certifyTask's.
func runScenario(ctx context.Context, task ai.Task, sc Scenario, census *aitasks.Registry, repeats int,
	candidateRouter *ai.Router, candidateRec *traceRecorder, judgeRouter *ai.Router, judgeRec *traceRecorder,
	log *slog.Logger, acc *taskAccumulation, trace *payloadTrace,
) (string, error) {
	scenarioResults := make([]RunResult, 0, repeats)
	for i := 0; i < repeats; i++ {
		outcome, runErr := runOnce(ctx, candidateRouter, candidateRec, judgeRouter, judgeRec, sc, task, census, log, trace, i+1)
		if runErr != nil {
			return "", fmt.Errorf("aicert: task %s scenario %s run %d: %w", task, sc.Name, i+1, runErr)
		}
		if outcome.Degraded {
			return "", fmt.Errorf(
				"aicert: task %s scenario %s run %d: candidate attempt served on a budget-degraded route — refusing to certify a demoted answer",
				task, sc.Name, i+1)
		}
		if outcome.JudgeDegraded {
			return "", fmt.Errorf(
				"aicert: task %s scenario %s run %d: judge attempt served on a budget-degraded route — refusing to trust a demoted grader",
				task, sc.Name, i+1)
		}
		if err := acc.addRun(task, sc, i, outcome); err != nil {
			return "", err
		}
		scenarioResults = append(scenarioResults, outcome.RunResult)
	}
	scenarioVerdict, _ := Verdict(scenarioResults, sc.Expect.Bands)
	return scenarioVerdict, nil
}

// runOutcome is one scored run plus the identity fields Record needs
// that RunResult itself has no room for (RunResult is score.go's public,
// runner-agnostic shape). JudgeDegraded mirrors RunResult.Degraded's
// candidate-side signal for the judge's own trace — certifyTask checks
// both before ever trusting an outcome.
// CertifiedScope is read off the case's OWN site rather than the scenario's
// name for it, because the site is what says how much of the invocation the
// case actually drives.
type runOutcome struct {
	RunResult
	Provider, ServedModel, ServedIdentitySource, JudgeServedModel string
	CertifiedScope                                                string
	JudgeDegraded                                                 bool
}

// runOnce drives exactly one prepared case and its judge score — one fresh
// logical call on each router, cache off, so no repeat ever collapses onto a
// prior one's answer. A degraded CANDIDATE attempt short-circuits before the
// judge is ever called: certifyTask voids the whole task's record on
// outcome.Degraded regardless of what the judge says, so scoring a demoted
// answer would be a real, paid judge call spent on a result guaranteed to be
// thrown away.
//
// The case is prepared, run and evaluated here rather than built from the
// scenario: the request is the one the site's own code issues and the verdict
// is the one the site's own validator reaches, so a run measures what ships
// instead of a corpus author's description of it.
func runOnce(ctx context.Context, candidate *ai.Router, candidateRec *traceRecorder, judge *ai.Router, judgeRec *traceRecorder, sc Scenario, task ai.Task, census *aitasks.Registry, log *slog.Logger, trace *payloadTrace, run int) (runOutcome, error) {
	factory, bound := census.CaseFor(task, sc.Site)
	if !bound {
		return runOutcome{}, fmt.Errorf("no certification case is bound to site %s/%s", task, sc.Site)
	}
	prepared, err := factory.Prepare(json.RawMessage(sc.Fixture), json.RawMessage(sc.Expect.Answer))
	if err != nil {
		return runOutcome{}, fmt.Errorf("preparing the case: %w", err)
	}

	caseTrace, err := prepared.Run(ctx, routedCompleter{router: candidate, task: task})
	if err != nil {
		return runOutcome{}, fmt.Errorf("candidate call: %w", err)
	}
	term, ok := candidateRec.lastTerminal()
	if !ok {
		return runOutcome{}, fmt.Errorf("candidate call: no terminal trace recorded")
	}
	traceCall(ctx, trace, "candidate", task, sc, run, term, log)
	if term.Degraded {
		return runOutcome{RunResult: RunResult{Degraded: true}}, nil
	}

	// The site's own validator, over the site's own trace: Evaluate reports a
	// measurement, so a refused reply and a wrong answer stay distinguishable
	// instead of collapsing into one failed run.
	evaluated := prepared.Evaluate(caseTrace)
	if !aitasks.KnownOutcome(evaluated.Result) {
		return runOutcome{}, fmt.Errorf(
			"the case for site %s/%s evaluated to %q, which is not one of the outcomes a reply can have — a run counted under no outcome would leave the record's own totals unable to add up",
			task, sc.Site, evaluated.Result)
	}
	capsOK, capFailures := checkCaps(sc.Expect.Caps, term)
	// A run passes when what happened is what the scenario said should happen.
	// Comparing against a fixed "accepted" instead would make expect.outcome a
	// declaration the harness ignores — and would leave a scenario whose right
	// answer is a refusal unable to say so.
	outcomeAsExpected := evaluated.Result == sc.Expect.Outcome
	if !outcomeAsExpected || !capsOK {
		log.WarnContext(ctx, "aicert: run did not pass its validator/caps gate",
			"task", string(task), "scenario", sc.Name, "site", sc.Site,
			"outcome", evaluated.Result, "want_outcome", sc.Expect.Outcome,
			"detail", evaluated.Detail, "cap_failures", capFailures)
	}

	// The judge reads what production's parsers read: the unfenced text (every
	// serving path strips markdown fences before json.Unmarshal, so a fence is
	// presentation, not a defect).
	output := ai.Unfence(caseTrace.Output)

	score, judgeServedModel, judgeDegraded, err := judgeScore(ctx, judge, judgeRec, sc, output, log)
	if err != nil {
		return runOutcome{}, fmt.Errorf("judge: %w", err)
	}
	if judgeTerm, ok := judgeRec.lastTerminal(); ok {
		traceCall(ctx, trace, "judge", task, sc, run, judgeTerm, log)
	}

	return runOutcome{
		RunResult: RunResult{
			Output:           output,
			Outcome:          evaluated.Result,
			LatencyMS:        term.LatencyMS,
			TokensIn:         term.TokensIn,
			TokensOut:        term.TokensOut,
			CachedTokens:     term.CachedTokens,
			CacheWriteTokens: term.CacheWriteTokens,
			HardPass:         outcomeAsExpected && capsOK,
			Score:            score,
		},
		Provider:             term.Provider,
		ServedModel:          term.ServedModel,
		ServedIdentitySource: term.ServedIdentitySource,
		CertifiedScope:       factory.Site().CertifiedScope(),
		JudgeServedModel:     judgeServedModel,
		JudgeDegraded:        judgeDegraded,
	}, nil
}

// routedCompleter hands a prepared case the one model call it may make,
// bound to the task under certification. A case names no task and no router:
// it knows the request its site sends, and the harness decides which candidate
// binding answers it.
type routedCompleter struct {
	router *ai.Router
	task   ai.Task
}

func (c routedCompleter) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	resp, _, err := c.router.Complete(ctx, c.task, req)
	if err != nil {
		return model.Response{}, fmt.Errorf("aicert: %s: %w", c.task, err)
	}
	return resp, nil
}

// repeatsOrDefault applies RunnerConfig.Repeats' default and validates
// its oddness up front — a wrong-N call into Verdict is a programmer
// bug (score.go panics on it), but a wrong MARGINCE_AICERT_RUNS is an
// operator input error and must fail with a message that says so.
func repeatsOrDefault(n int) (int, error) {
	if n == 0 {
		n = defaultRepeats
	}
	if n < 1 || n%2 == 0 {
		return 0, fmt.Errorf("aicert: runner: repeats must be odd and positive, got %d", n)
	}
	return n, nil
}

// ensureWorkspace mints a fixed, DB-less workspace principal when ctx
// carries none — the router's own precondition for tracing a call
// (Router.serveAttempt refuses outside a workspace context), mirrored
// from compose/sitereaddebug.go's identical DB-less debug-lane pattern.
func ensureWorkspace(ctx context.Context) context.Context {
	if _, ok := principal.WorkspaceID(ctx); ok {
		return ctx
	}
	return principal.WithWorkspaceID(ctx, ids.NewV7())
}

// verdictRank orders the three §5 verdicts worst-to-best so a
// multi-scenario task can fold down to its worst scenario outcome.
var verdictRank = map[string]int{
	VerdictNotSupported:      0,
	VerdictSupportedDegraded: 1,
	VerdictCertified:         2,
}

// worstVerdict returns whichever of a, b ranks lower (less certified).
func worstVerdict(a, b string) string {
	if verdictRank[a] <= verdictRank[b] {
		return a
	}
	return b
}

// buildRecord, seedRateFor, and percentile live in record.go alongside the
// Record type they build — that file already owns "the on-disk Record
// shape," so folding pooled run stats into one is that same concern, not
// this file's own "drive the routers" one.
