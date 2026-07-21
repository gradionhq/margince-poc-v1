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

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// Record is one task×provider×model×environment certification outcome —
// the durable, committed artifact a certification run produces and
// `e2e-ai-report` reads back. RanAt is a caller-stamped RFC 3339 timestamp:
// this package never calls time.Now, so the same []RunResult always
// produces the same Record byte-for-byte except for whatever the caller
// puts in RanAt.
type Record struct {
	Task          string  `json:"task"`
	Provider      string  `json:"provider"`
	ServedModel   string  `json:"served_model"`
	EnvClass      string  `json:"env_class"`
	PromptVersion string  `json:"prompt_version"`
	CorpusVersion string  `json:"corpus_version"`
	Verdict       string  `json:"verdict"`
	Runs          int     `json:"runs"`
	Reliability   float64 `json:"reliability"`
	ScoreP50      int     `json:"score_p50"`
	ScoreMin      int     `json:"score_min"`
	LatencyP50    int64   `json:"latency_p50"`
	LatencyP95    int64   `json:"latency_p95"`
	MeanTokens    int     `json:"mean_tokens"`
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
func buildRecord(task ai.Task, taskVerdict string, reliability float64, results []RunResult, latencies []int64,
	tokensInTotal, tokensOutTotal, cachedTokensTotal, cacheWriteTokensTotal int,
	provider, servedModel, identitySource, judgeServedModel string, selfJudgedEveryRun bool, baseCfg ai.RoutingConfig,
) Record {
	scores := make([]int, len(results))
	for i, r := range results {
		scores[i] = r.Score
	}
	sort.Ints(scores)

	sortedLatencies := append([]int64(nil), latencies...)
	sort.Slice(sortedLatencies, func(i, j int) bool { return sortedLatencies[i] < sortedLatencies[j] })

	n := len(results)
	meanTokens, meanTokensIn, meanTokensOut, meanCachedTokens, meanCacheWriteTokens := 0, 0, 0, 0, 0
	if n > 0 {
		// meanTokens divides the exact summed total (tokensInTotal +
		// tokensOutTotal, an exact sum of two exact sums) — bit-for-bit the
		// same value this field held before the per-bucket split existed.
		// Each mean bucket below divides its OWN total independently, so it
		// need not add back up to meanTokens after truncation.
		meanTokens = (tokensInTotal + tokensOutTotal) / n
		meanTokensIn = tokensInTotal / n
		meanTokensOut = tokensOutTotal / n
		meanCachedTokens = cachedTokensTotal / n
		meanCacheWriteTokens = cacheWriteTokensTotal / n
	}

	// ranAt is captured once and reused for both RanAt and the pricing
	// snapshot date so buildRecord never calls nowFunc twice — the record's
	// timestamp and the rate sheet it priced against are always the same
	// instant.
	ranAt := nowFunc().UTC()
	estCostMicroUSD := int64(0)
	usage := ai.Usage{
		TokensIn: meanTokensIn, TokensOut: meanTokensOut,
		CachedTokens: meanCachedTokens, CacheWriteTokens: meanCacheWriteTokens,
	}
	if rate, ok := seedRateFor(provider, servedModel, ranAt); ok {
		estCostMicroUSD = ai.PriceCall(usage, rate)
	}

	return Record{
		Task:                 string(task),
		Provider:             provider,
		ServedModel:          servedModel,
		EnvClass:             string(baseCfg.Profile),
		PromptVersion:        promptVersionV1,
		CorpusVersion:        corpusVersionV1,
		Verdict:              taskVerdict,
		Runs:                 n,
		Reliability:          reliability,
		ScoreP50:             scores[len(scores)/2],
		ScoreMin:             scores[0],
		LatencyP50:           percentile(sortedLatencies, 0.50),
		LatencyP95:           percentile(sortedLatencies, 0.95),
		MeanTokens:           meanTokens,
		MeanTokensIn:         meanTokensIn,
		MeanTokensOut:        meanTokensOut,
		MeanCachedTokens:     meanCachedTokens,
		MeanCacheWriteTokens: meanCacheWriteTokens,
		// EstCostMicroUSD prices the pooled per-bucket means against the
		// cert lane's in-memory seed rate sheet (ai.SeedModelRates): the
		// cert lane runs outside any DB workspace tx, so there is no
		// ai_model_rate table to read RateStore.RateFor's own way — this is
		// the closest analogue available here. No matching (provider,
		// served model) row keeps it an honest 0, exactly like an unpriced
		// RateStore.RateFor call (price-on-read; never fabricate a price).
		EstCostMicroUSD:      estCostMicroUSD,
		JudgeServedModel:     judgeServedModel,
		SelfJudged:           selfJudgedEveryRun,
		ServedIdentitySource: identitySource,
		RanAt:                ranAt.Format(time.RFC3339),
	}
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
