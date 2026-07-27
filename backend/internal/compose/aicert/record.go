// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// Record is one task×provider×model×environment certification outcome —
// the durable, committed artifact a certification run produces and
// `e2e-ai-report` reads back. RanAt is a caller-stamped RFC 3339 timestamp:
// this package never calls time.Now, so the same []RunResult always
// produces the same Record byte-for-byte except for whatever the caller
// puts in RanAt.
type Record struct {
	Task          string `json:"task"`
	Provider      string `json:"provider"`
	ServedModel   string `json:"served_model"`
	EnvClass      string `json:"env_class"`
	PromptVersion string `json:"prompt_version"`
	CorpusVersion string `json:"corpus_version"`
	Verdict       string `json:"verdict"`
	Runs          int    `json:"runs"`
	// Passed is how many runs did what their scenario asked — the reply the
	// scenario expects, inside its caps — and Reliability is that count over
	// Runs. It is carried as a count of its own because pass/fail is the first
	// question asked of a record, and the four counts below cannot answer it:
	// a run that came back accepted where the scenario demanded an abstention
	// is a FAILED run that still raises ReportedAccepted.
	Passed      int     `json:"passed"`
	Reliability float64 `json:"reliability"`
	// ReportedAccepted/ReportedWrongAnswer/ReportedInvalid/ReportedAbstained
	// are what the sites' own validators REPORTED across the pooled runs, one
	// count per outcome — never a pass/fail column. They are what turns a
	// reliability number into a diagnosis: replies the validator refused want a
	// different fix from well-formed replies that say the wrong thing, and an
	// abstention is a right answer that neither of the other two can express.
	// They always sum to Runs.
	ReportedAccepted    int `json:"reported_accepted"`
	ReportedWrongAnswer int `json:"reported_wrong_answer"`
	ReportedInvalid     int `json:"reported_invalid"`
	ReportedAbstained   int `json:"reported_abstained"`
	// CertifiedScope is how much of the task this record actually covers (one of
	// the aitasks.Scope* words), folded to the NARROWEST scope any site the run
	// touched could claim. A multi-turn or agent-loop scenario seeds the window
	// and grades the one reply that follows; a case bound to a site that calls
	// the model more than once for one invocation grades one of those calls. In
	// both, something the product does is supplied or skipped rather than
	// exercised, and a record silent about it claims more than it tested.
	CertifiedScope string `json:"certified_scope"`
	// ContextApplied says whether the runs were served the company context
	// production prepends. It is recorded rather than implied because the
	// answer is no: assembling that context reads the database, and the cert
	// lane runs without one. A record that omitted the field would leave a
	// reader to assume parity nobody checked.
	ContextApplied bool  `json:"context_applied"`
	ScoreP50       int   `json:"score_p50"`
	ScoreMin       int   `json:"score_min"`
	LatencyP50     int64 `json:"latency_p50"`
	LatencyP95     int64 `json:"latency_p95"`
	MeanTokens     int   `json:"mean_tokens"`
	// MeanTokensIn/MeanTokensOut/MeanCachedTokens/MeanCacheWriteTokens are
	// the four-bucket baseline (ADR-0067 phase 2): the pooled run set's
	// per-bucket mean, each bucket's own truncating integer division —
	// independent of MeanTokens (kept for compat), which divides the exact
	// summed total instead, so the two need not add up bucket-for-bucket.
	MeanTokensIn         int    `json:"mean_tokens_in"`
	MeanTokensOut        int    `json:"mean_tokens_out"`
	MeanCachedTokens     int    `json:"mean_cached_tokens"`
	MeanCacheWriteTokens int    `json:"mean_cache_write_tokens"`
	EstCostMicroUSD      int64  `json:"est_cost_microusd"`
	JudgeServedModel     string `json:"judge_served_model"`
	SelfJudged           bool   `json:"self_judged"`
	ServedIdentitySource string `json:"served_identity_source"`
	RanAt                string `json:"ran_at"`
	// Scenarios is every scenario this record pooled, with its own verdict and
	// its own counts. A record is written per TASK and a task is not one
	// scenario or even one site — cold_start ships four sites — so the pooled
	// numbers above cannot answer the question a failure actually raises:
	// WHICH scenario failed. One injection scenario failing every run and one
	// ordinary scenario failing occasionally reach the same task reliability
	// and want completely different fixes.
	Scenarios []ScenarioRecord `json:"scenarios"`
}

// ScenarioRecord is one scenario's own share of a task's record: what it asked
// for, how often it got it, and what came back the rest of the time.
//
// Site is carried per scenario rather than derived, because it is what lets a
// reader — and the readiness report — attribute a task's record to the site
// each of its rows actually measured.
type ScenarioRecord struct {
	Scenario string `json:"scenario"`
	Site     string `json:"site"`
	Verdict  string `json:"verdict"`
	Runs     int    `json:"runs"`
	Passed   int    `json:"passed"`
	// The same reported-outcome counts the task carries, on this scenario's own
	// runs: they say what came back, never whether it was what was asked for.
	ReportedAccepted    int `json:"reported_accepted"`
	ReportedWrongAnswer int `json:"reported_wrong_answer"`
	ReportedInvalid     int `json:"reported_invalid"`
	ReportedAbstained   int `json:"reported_abstained"`
}

// SiteTally is one SITE's share of a task's record, folded from the scenario
// rows that ran on it.
//
// It exists because a record covers a task and a reader asks about a site: a
// task's pooled numbers printed under four site labels are the same four
// numbers wearing labels they did not earn.
type SiteTally struct {
	Verdict             string
	Runs                int
	Passed              int
	ReportedAccepted    int
	ReportedWrongAnswer int
	ReportedInvalid     int
	ReportedAbstained   int
}

// Reliability is the fraction of this site's runs that did what their scenario
// asked. A site with no run has no reliability rather than a perfect one.
func (t SiteTally) Reliability() float64 {
	if t.Runs == 0 {
		return 0
	}
	return float64(t.Passed) / float64(t.Runs)
}

// ForSite folds every scenario row this record kept for one site's variant.
// False means this record measured that site not at all — a different thing
// from measuring it and finding nothing, which is why it is not a zero tally.
//
// The verdict folds to the WORST of the site's scenarios, the same way the
// task's own does: a site is only as certified as its weakest scenario.
func (r Record) ForSite(variant string) (SiteTally, bool) {
	var tally SiteTally
	found := false
	for _, sc := range r.Scenarios {
		if sc.Site != variant {
			continue
		}
		if !found {
			tally.Verdict = sc.Verdict
			found = true
		} else {
			tally.Verdict = worstVerdict(tally.Verdict, sc.Verdict)
		}
		tally.Runs += sc.Runs
		tally.Passed += sc.Passed
		tally.ReportedAccepted += sc.ReportedAccepted
		tally.ReportedWrongAnswer += sc.ReportedWrongAnswer
		tally.ReportedInvalid += sc.ReportedInvalid
		tally.ReportedAbstained += sc.ReportedAbstained
	}
	return tally, found
}

// sanitizeForPath maps a raw identifier (a provider name, or a served-model
// string like "accounts/fireworks/models/llama-v3-70b-instruct" that
// carries filesystem-hostile characters) to a safe path segment: every "/"
// and ":" becomes "_". This is a one-way, lossy mapping — two distinct raw
// strings could collide on the same sanitized segment — but it is
// deterministic, which is the property WriteRecord/LoadRecords actually
// need: the same raw string always resolves to the same file.
func sanitizeForPath(s string) string {
	replacer := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return replacer.Replace(s)
}

// recordPath returns the file WriteRecord/LoadRecords use for r under dir:
// records/<task>/<provider>_<model>_<env>.json.
func recordPath(dir string, r Record) string {
	filename := fmt.Sprintf("%s_%s_%s.json",
		sanitizeForPath(r.Provider), sanitizeForPath(r.ServedModel), sanitizeForPath(r.EnvClass))
	return filepath.Join(dir, sanitizeForPath(r.Task), filename)
}

// WriteRecord persists r under dir at its task/provider/model/env path,
// creating parent directories as needed. Marshaling is stable — fixed
// struct field order via json.MarshalIndent, a trailing newline — so a
// re-run that produces an identical Record leaves a diff-free file; only a
// genuine change in outcome touches the committed record.
func WriteRecord(dir string, r Record) error {
	path := recordPath(dir, r)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("aicert: creating %s: %w", filepath.Dir(path), err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("aicert: marshaling record for %s: %w", path, err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("aicert: writing %s: %w", path, err)
	}
	return nil
}

// LoadRecords reads every *.json file under dir into a Record, sorted by
// Task/Provider/ServedModel/EnvClass so a report over the same record set
// always renders the same order. A directory that does not exist yet
// (no certification has run) is not an error — it reads as an empty
// record set, the honest "nothing certified yet" state.
func LoadRecords(dir string) ([]Record, error) {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("aicert: records %s: %w", dir, err)
	}

	var records []Record
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("aicert: records %s: %w", path, err)
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		raw, readErr := os.ReadFile(path) // #nosec G304 G122 -- path is a *.json file from walking the trusted records tree
		if readErr != nil {
			return fmt.Errorf("aicert: reading %s: %w", path, readErr)
		}
		var r Record
		if decodeErr := json.Unmarshal(raw, &r); decodeErr != nil {
			return fmt.Errorf("aicert: parsing %s: %w", path, decodeErr)
		}
		records = append(records, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.ServedModel != b.ServedModel {
			return a.ServedModel < b.ServedModel
		}
		return a.EnvClass < b.EnvClass
	})
	return records, nil
}

// buildRecord folds one task's pooled runs (across every scenario, every
// repeat) and its already-folded taskVerdict into the on-disk Record
// shape. Score/latency percentiles are computed directly here (not via
// Verdict, which is scoped to one scenario's odd-N run set and would
// panic on a multi-scenario task's pooled, possibly-even count).
//
// The run set arrives as the accumulation certifyTask folded it into, rather
// than as one parameter per number: the record IS that accumulation written
// down, and a dozen same-typed positional arguments is a transposition waiting
// to happen that no test would catch.
func buildRecord(task ai.Task, taskVerdict string, acc *taskAccumulation, baseCfg ai.RoutingConfig, promptVersion string) Record {
	results := acc.allResults
	scores := make([]int, len(results))
	for i, r := range results {
		scores[i] = r.Score
	}
	sort.Ints(scores)

	sortedLatencies := append([]int64(nil), acc.latencies...)
	sort.Slice(sortedLatencies, func(i, j int) bool { return sortedLatencies[i] < sortedLatencies[j] })

	n := len(results)
	reliability := 0.0
	if n > 0 {
		reliability = float64(acc.passed) / float64(n)
	}
	meanTokens, usage := meanUsage(acc, n)

	// ranAt is captured once and reused for both RanAt and the pricing
	// snapshot date so buildRecord never calls nowFunc twice — the record's
	// timestamp and the rate sheet it priced against are always the same
	// instant.
	ranAt := nowFunc().UTC()
	estCostMicroUSD := int64(0)
	if rate, ok := seedRateFor(acc.provider, acc.servedModel, ranAt); ok {
		estCostMicroUSD = ai.PriceCall(usage, rate)
	}

	tally := tallyOutcomes(results)
	return Record{
		Task:                 string(task),
		Provider:             acc.provider,
		ServedModel:          acc.servedModel,
		EnvClass:             string(baseCfg.Profile),
		PromptVersion:        promptVersion,
		CorpusVersion:        corpusVersionV1,
		Verdict:              taskVerdict,
		Runs:                 n,
		Passed:               acc.passed,
		Reliability:          reliability,
		ReportedAccepted:     tally.accepted,
		ReportedWrongAnswer:  tally.wrongAnswer,
		ReportedInvalid:      tally.invalid,
		ReportedAbstained:    tally.abstained,
		CertifiedScope:       acc.certifiedScope,
		ContextApplied:       certLaneAppliesCompanyContext,
		ScoreP50:             scores[len(scores)/2],
		ScoreMin:             scores[0],
		LatencyP50:           percentile(sortedLatencies, 0.50),
		LatencyP95:           percentile(sortedLatencies, 0.95),
		MeanTokens:           meanTokens,
		MeanTokensIn:         usage.TokensIn,
		MeanTokensOut:        usage.TokensOut,
		MeanCachedTokens:     usage.CachedTokens,
		MeanCacheWriteTokens: usage.CacheWriteTokens,
		// EstCostMicroUSD prices the pooled per-bucket means against the
		// cert lane's in-memory seed rate sheet (ai.SeedModelRates): the
		// cert lane runs outside any DB workspace tx, so there is no
		// ai_model_rate table to read RateStore.RateFor's own way — this is
		// the closest analogue available here. No matching (provider,
		// served model) row keeps it an honest 0, exactly like an unpriced
		// RateStore.RateFor call (price-on-read; never fabricate a price).
		EstCostMicroUSD:      estCostMicroUSD,
		JudgeServedModel:     acc.judgeServedModel,
		SelfJudged:           acc.selfJudgedEveryRun,
		ServedIdentitySource: acc.identitySource,
		RanAt:                ranAt.Format(time.RFC3339),
		Scenarios:            acc.scenarios,
	}
}

// meanUsage is the pooled run set's per-bucket mean, alongside the flat
// MeanTokens the record has always carried.
//
// MeanTokens divides the exact summed total (tokens in + tokens out, an exact
// sum of two exact sums) — bit-for-bit the value that field held before the
// per-bucket split existed. Each bucket divides its OWN total independently, so
// the four need not add back up to MeanTokens after truncation.
func meanUsage(acc *taskAccumulation, n int) (int, ai.Usage) {
	if n == 0 {
		return 0, ai.Usage{}
	}
	return (acc.tokensInTotal + acc.tokensOutTotal) / n, ai.Usage{
		TokensIn:         acc.tokensInTotal / n,
		TokensOut:        acc.tokensOutTotal / n,
		CachedTokens:     acc.cachedTokensTotal / n,
		CacheWriteTokens: acc.cacheWriteTokensTotal / n,
	}
}

// certLaneAppliesCompanyContext is false because this lane has no database.
// The company context production prepends is assembled from stored workspace
// facts, and every certification run here is DB-less — so the requests scored
// are the site's own prompt without it. Spelled as a named constant so the
// record's claim is a stated fact of the lane rather than a literal a reader
// has to interpret.
const certLaneAppliesCompanyContext = false

// outcomeTally is the per-outcome run count a record carries. It is a struct
// rather than four returns so the four numbers cannot be transposed at a call
// site, which is the one way this arithmetic can be wrong without failing.
type outcomeTally struct {
	accepted, wrongAnswer, invalid, abstained int
}

// tallyOutcomes counts what each pooled run's validator reported. Every
// RunResult reaching here carries one of the four (the runner refuses a run
// whose case reported anything else), so the counts always sum to len(results).
func tallyOutcomes(results []RunResult) outcomeTally {
	var t outcomeTally
	for _, r := range results {
		switch r.Outcome {
		case aitasks.OutcomeAccepted:
			t.accepted++
		case aitasks.OutcomeWrongAnswer:
			t.wrongAnswer++
		case aitasks.OutcomeInvalid:
			t.invalid++
		case aitasks.OutcomeAbstained:
			t.abstained++
		}
	}
	return t
}

// seedRateFor resolves the exact (provider, servedModel) rate row from
// ai.SeedModelRates(day) — an exact-key lookup, not RateStore.RateFor's
// as-of-date walk, because every row SeedModelRates returns for one day
// carries that same single EffectiveDate. False means no rate is seeded
// for this exact provider/model pair: the call is unpriced, never priced
// at a fabricated 0.
func seedRateFor(provider, servedModel string, day time.Time) (ai.ModelRate, bool) {
	for _, r := range ai.SeedModelRates(day) {
		if r.Provider == provider && r.ModelID == servedModel {
			return r, true
		}
	}
	return ai.ModelRate{}, false
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
