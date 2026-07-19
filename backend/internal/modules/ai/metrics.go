// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"fmt"
	"io"
	"sort"
	"sync"
)

// callMetrics is the in-process AI counter set exposed on /metrics. Counters
// only (monotonic since process start); hand-rolled text like the rest of
// the metrics surface, so no client_golang dependency. Never labels by
// content or workspace — cardinality stays bounded by the closed task/tier
// sets and the small provider list.
type callMetrics struct {
	mu     sync.Mutex
	calls  map[metricKey]uint64
	errors map[metricKey]uint64
	// Token totals are int64, not uint64: model usage counts are always
	// non-negative ints, and widening int→int64 is a lossless conversion
	// (an int→uint64 cast trips gosec G115 on the sign change for no gain).
	tokIn  int64
	tokOut int64
}

type metricKey struct{ task, tier, provider string }

func newCallMetrics() *callMetrics {
	return &callMetrics{calls: map[metricKey]uint64{}, errors: map[metricKey]uint64{}}
}

// sharedCallMetrics is the process-wide AI counter set. Every Router
// increments the same collector so /metrics reports one honest total
// across lanes, and it is rendered exactly once (Prometheus forbids a
// repeated metric family / duplicate series in one exposition).
var sharedCallMetrics = newCallMetrics()

func (m *callMetrics) observe(c Call) {
	k := metricKey{task: string(c.Task), tier: string(c.Tier), provider: c.Provider}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls[k]++
	if c.ErrorSentinel != "" {
		m.errors[k]++
	}
	m.tokIn += int64(c.TokensIn)
	m.tokOut += int64(c.TokensOut)
}

func (m *callMetrics) WritePrometheus(w io.Writer) {
	// Snapshot under the lock, render outside it: w is typically the
	// /metrics HTTP response, and a slow scrape client must never hold
	// the mutex every completion's observe() takes — that would let one
	// stalled scrape block AI calls process-wide.
	m.mu.Lock()
	calls := make(map[metricKey]uint64, len(m.calls))
	for k, v := range m.calls {
		calls[k] = v
	}
	errs := make(map[metricKey]uint64, len(m.errors))
	for k, v := range m.errors {
		errs[k] = v
	}
	tokIn, tokOut := m.tokIn, m.tokOut
	m.mu.Unlock()

	writeCounterFamily(w, "margince_ai_calls_total", "AI call terminals (completion or embedding) since process start.", calls)
	writeCounterFamily(w, "margince_ai_call_errors_total", "AI completion failures since process start.", errs)
	_, _ = fmt.Fprintf(w, "# HELP margince_ai_tokens_total AI tokens billed since process start.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_ai_tokens_total counter\n")
	_, _ = fmt.Fprintf(w, "margince_ai_tokens_total{direction=\"in\"} %d\n", tokIn)
	_, _ = fmt.Fprintf(w, "margince_ai_tokens_total{direction=\"out\"} %d\n", tokOut)
}

func writeCounterFamily(w io.Writer, name, help string, fam map[metricKey]uint64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	keys := make([]metricKey, 0, len(fam))
	for k := range fam {
		keys = append(keys, k)
	}
	// Stable output so scrapes and tests don't flap on map order.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].provider != keys[j].provider {
			return keys[i].provider < keys[j].provider
		}
		if keys[i].task != keys[j].task {
			return keys[i].task < keys[j].task
		}
		return keys[i].tier < keys[j].tier
	})
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "%s{provider=%q,task=%q,tier=%q} %d\n", name, k.provider, k.task, k.tier, fam[k])
	}
}
