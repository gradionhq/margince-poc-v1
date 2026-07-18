// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The runner: the one part of this package that actually drives model
// calls. Everything else (scenario.go, checks.go, score.go, record.go)
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
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// defaultRepeats is Repeats' fallback when a caller (the env-driven CLI
// lane) leaves it unset. Odd, per Verdict's median requirement.
const defaultRepeats = 3

// promptVersionV1 and corpusVersionV1 are this generation's fixed
// version stamps: neither the scenario format nor the judge prompt
// carries its own version field yet, so every Record this runner
// produces names the same one until a versioning scheme is introduced
// alongside a real second version.
const (
	promptVersionV1 = "v1"
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
	RoutingPath string // MARGINCE_AI_ROUTING
	TaskFilter  string // MARGINCE_AICERT_TASK ("" = all tasks with a corpus)
	Override    string // MARGINCE_AICERT_MODEL "provider:model" — candidate only
	Repeats     int    // MARGINCE_AICERT_RUNS, default 3, must be odd
	RecordDir   string
	CorpusDir   string
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
	repeats, err := repeatsOrDefault(cfg.Repeats)
	if err != nil {
		return nil, err
	}

	baseCfg, err := ai.LoadRoutingFile(cfg.RoutingPath)
	if err != nil {
		return nil, fmt.Errorf("aicert: runner: %w", err)
	}

	scenarios, err := LoadCorpus(cfg.CorpusDir)
	if err != nil {
		return nil, fmt.Errorf("aicert: runner: %w", err)
	}

	byTask := groupByTask(scenarios, cfg.TaskFilter)
	if cfg.TaskFilter != "" && len(byTask) == 0 {
		return nil, fmt.Errorf("aicert: runner: task %q has no scenarios under %s", cfg.TaskFilter, cfg.CorpusDir)
	}

	ctx = ensureWorkspace(ctx)

	var records []Record
	var runErrs []error
	for _, task := range sortedTasks(byTask) {
		rec, err := certifyTask(ctx, task, byTask[task], baseCfg, cfg.Override, repeats, log, nil)
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

// certifyHooks lets this package's own tests reach into certifyTask's two
// router constructions — a scripted *ai.FakeClient via ai.WithFakeClient,
// a starved ai.WithMonthlyBudget to force a deterministic degrade — none
// of which RunnerConfig's pinned shape has room for and none of which a
// real cert run ever needs (Run always passes nil). This mirrors
// ai.assembleRouter: "the seam unit tests inject fakes through."
type certifyHooks struct {
	candidateOpts []ai.LocalOption
	judgeOpts     []ai.LocalOption
}

// certifyTask runs every scenario for one task over a fresh
// candidate/judge router pair and folds the outcome into one Record.
func certifyTask(ctx context.Context, task ai.Task, scenarios []Scenario, baseCfg ai.RoutingConfig, override string, repeats int, log *slog.Logger, hooks *certifyHooks) (Record, error) {
	candidateCfg, err := overrideForTask(baseCfg, task, override)
	if err != nil {
		return Record{}, err
	}
	var candidateExtra, judgeExtra []ai.LocalOption
	if hooks != nil {
		candidateExtra, judgeExtra = hooks.candidateOpts, hooks.judgeOpts
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
		scenarioVerdict, err := runScenario(ctx, task, sc, repeats, candidateRouter, candidateRec, judgeRouter, judgeRec, log, acc)
		if err != nil {
			return Record{}, err
		}
		taskVerdict = worstVerdict(taskVerdict, scenarioVerdict)
	}

	reliability := float64(acc.passed) / float64(len(acc.allResults))
	return buildRecord(task, taskVerdict, reliability, acc.allResults, acc.latencies, acc.tokensTotal,
		acc.provider, acc.servedModel, acc.identitySource, acc.judgeServedModel, acc.selfJudgedEveryRun, baseCfg), nil
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
	allResults                                              []RunResult
	latencies                                               []int64
	tokensTotal                                             int
	passed                                                  int
	provider, servedModel, identitySource, judgeServedModel string
	selfJudgedEveryRun                                      bool
	identitySet                                             bool
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
	acc.tokensTotal += outcome.Tokens
	acc.provider, acc.servedModel, acc.identitySource = outcome.Provider, outcome.ServedModel, outcome.ServedIdentitySource
	acc.identitySet = true
	acc.judgeServedModel = outcome.JudgeServedModel
	if !selfJudged(outcome.ServedModel, outcome.JudgeServedModel) {
		acc.selfJudgedEveryRun = false
	}
	if outcome.HardPass {
		acc.passed++
	}
	return nil
}

// runScenario drives repeats runs of one scenario, folding each into acc,
// and returns the scenario's own verdict for certifyTask to fold into the
// task's worst-case verdict. Split out of certifyTask so the per-run
// degrade/uniformity gates and the per-scenario verdict fold live on
// their own function, not certifyTask's.
func runScenario(ctx context.Context, task ai.Task, sc Scenario, repeats int,
	candidateRouter *ai.Router, candidateRec *traceRecorder, judgeRouter *ai.Router, judgeRec *traceRecorder,
	log *slog.Logger, acc *taskAccumulation,
) (string, error) {
	scenarioResults := make([]RunResult, 0, repeats)
	for i := 0; i < repeats; i++ {
		outcome, runErr := runOnce(ctx, candidateRouter, candidateRec, judgeRouter, judgeRec, sc, task, log)
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
type runOutcome struct {
	RunResult
	Provider, ServedModel, ServedIdentitySource, JudgeServedModel string
	JudgeDegraded                                                 bool
}

// runOnce drives exactly one candidate completion and its judge score —
// one fresh logical call on each router, cache off, so no repeat ever
// collapses onto a prior one's answer. A degraded CANDIDATE attempt
// short-circuits before the judge is ever called: certifyTask voids the
// whole task's record on outcome.Degraded regardless of what the judge
// says, so scoring a demoted answer would be a real, paid judge call
// spent on a result guaranteed to be thrown away.
func runOnce(ctx context.Context, candidate *ai.Router, candidateRec *traceRecorder, judge *ai.Router, judgeRec *traceRecorder, sc Scenario, task ai.Task, log *slog.Logger) (runOutcome, error) {
	resp, _, err := candidate.Complete(ctx, task, buildRequest(sc))
	if err != nil {
		return runOutcome{}, fmt.Errorf("candidate call: %w", err)
	}
	term, ok := candidateRec.lastTerminal()
	if !ok {
		return runOutcome{}, fmt.Errorf("candidate call: no terminal trace recorded")
	}
	if term.Degraded {
		return runOutcome{RunResult: RunResult{Degraded: true}}, nil
	}

	// Judge and checks consume what production's parsers consume: the
	// unfenced text (every serving path strips markdown fences before
	// json.Unmarshal, so a fence is presentation, not a defect).
	output := ai.Unfence(resp.Text)

	score, judgeServedModel, judgeDegraded, err := judgeScore(ctx, judge, judgeRec, sc, output, log)
	if err != nil {
		return runOutcome{}, fmt.Errorf("judge: %w", err)
	}

	structuralOK, structuralFailures := RunChecks(sc.Expect.Structural, output)
	capsOK, capFailures := checkCaps(sc.Expect.Caps, term)
	if !structuralOK || !capsOK {
		log.WarnContext(ctx, "aicert: run failed its structural/caps gate",
			"task", string(task), "scenario", sc.Name,
			"structural_failures", structuralFailures, "cap_failures", capFailures)
	}

	return runOutcome{
		RunResult: RunResult{
			Output:    output,
			LatencyMS: term.LatencyMS,
			Tokens:    term.TokensIn + term.TokensOut,
			HardPass:  structuralOK && capsOK,
			Score:     score,
		},
		Provider:             term.Provider,
		ServedModel:          term.ServedModel,
		ServedIdentitySource: term.ServedIdentitySource,
		JudgeServedModel:     judgeServedModel,
		JudgeDegraded:        judgeDegraded,
	}, nil
}

// overrideForTask rebinds ONLY the tiers on task's routing ladder to the
// MODEL= override's provider:model, over a COPY of base's tier map —
// base itself is never mutated, so the judge router built from the same
// base afterward still sees every tier as configured. Empty override is
// a no-op: the candidate then rides base exactly like the judge.
func overrideForTask(base ai.RoutingConfig, task ai.Task, override string) (ai.RoutingConfig, error) {
	if override == "" {
		return base, nil
	}
	provider, modelName, found := strings.Cut(override, ":")
	if !found || provider == "" || modelName == "" {
		return ai.RoutingConfig{}, fmt.Errorf("aicert: MARGINCE_AICERT_MODEL wants provider:model, got %q", override)
	}
	ladder := ai.TaskLadder(task)
	if len(ladder) == 0 {
		return ai.RoutingConfig{}, fmt.Errorf("aicert: task %s has no routing ladder to override", task)
	}
	tiers := make(map[ai.Tier]ai.ProviderConfig, len(base.Tiers))
	for tier, binding := range base.Tiers {
		tiers[tier] = binding
	}
	for _, tier := range ladder {
		tiers[tier] = ai.ProviderConfig{Provider: provider, Model: modelName}
	}
	overridden := base
	overridden.Tiers = tiers
	return overridden, nil
}

// groupByTask buckets scenarios by their Task field, keeping only tasks
// matching filter when filter is non-empty.
func groupByTask(scenarios []Scenario, filter string) map[ai.Task][]Scenario {
	byTask := map[ai.Task][]Scenario{}
	for _, sc := range scenarios {
		if filter != "" && sc.Task != filter {
			continue
		}
		t := ai.Task(sc.Task)
		byTask[t] = append(byTask[t], sc)
	}
	return byTask
}

// sortedTasks returns byTask's keys in deterministic order, so two runs
// over the same corpus process tasks (and therefore emit any errors) in
// the same order.
func sortedTasks(byTask map[ai.Task][]Scenario) []ai.Task {
	tasks := make([]ai.Task, 0, len(byTask))
	for t := range byTask {
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i] < tasks[j] })
	return tasks
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

// buildRecord folds one task's pooled runs (across every scenario, every
// repeat) and its already-folded taskVerdict into the on-disk Record
// shape. Score/latency percentiles are computed directly here (not via
// Verdict, which is scoped to one scenario's odd-N run set and would
// panic on a multi-scenario task's pooled, possibly-even count).
func buildRecord(task ai.Task, taskVerdict string, reliability float64, results []RunResult, latencies []int64, tokensTotal int,
	provider, servedModel, identitySource, judgeServedModel string, selfJudgedEveryRun bool, baseCfg ai.RoutingConfig,
) Record {
	scores := make([]int, len(results))
	for i, r := range results {
		scores[i] = r.Score
	}
	sort.Ints(scores)

	sortedLatencies := append([]int64(nil), latencies...)
	sort.Slice(sortedLatencies, func(i, j int) bool { return sortedLatencies[i] < sortedLatencies[j] })

	meanTokens := 0
	if len(results) > 0 {
		meanTokens = tokensTotal / len(results)
	}

	return Record{
		Task:          string(task),
		Provider:      provider,
		ServedModel:   servedModel,
		EnvClass:      string(baseCfg.Profile),
		PromptVersion: promptVersionV1,
		CorpusVersion: corpusVersionV1,
		Verdict:       taskVerdict,
		Runs:          len(results),
		Reliability:   reliability,
		ScoreP50:      scores[len(scores)/2],
		ScoreMin:      scores[0],
		LatencyP50:    percentile(sortedLatencies, 0.50),
		LatencyP95:    percentile(sortedLatencies, 0.95),
		MeanTokens:    meanTokens,
		// EstCostMicroUSD stays 0: no cost model prices ai.Call yet
		// (ai.Call.EstimatedCostMicroUSD's own doc: "nil until a cost model
		// prices the call") — an honest zero, not a fabricated estimate.
		EstCostMicroUSD:      0,
		JudgeServedModel:     judgeServedModel,
		SelfJudged:           selfJudgedEveryRun,
		ServedIdentitySource: identitySource,
		RanAt:                nowFunc().UTC().Format(time.RFC3339),
	}
}

// percentile returns the nearest-rank pth percentile (p in [0,1]) of
// sorted, which must already be sorted ascending.
func percentile(sorted []int64, p float64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}
