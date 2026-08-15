// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command gen-perfdoc renders docs/reference/performance-budgets.md from the
// records the benchmark lane leaves behind.
//
// docs/reference/rbac-matrix.md is the model for the SHAPE — a published page a
// reader who cannot run the lane can still consult — and deliberately not the
// model for the SEMANTICS. That page is derived from the seeded policy and so
// is drift-gated: render it twice, get the same bytes. A latency is measured,
// not derived, so rendering twice never gives the same bytes and a drift gate
// would fail every run for everybody. What this holds to instead is the rule
// aicert's records already follow: a measurement says what it ran on, and a
// budget nobody measured is printed as unmeasured rather than omitted.
//
// That last part is the point. #697 was filed because we publish budgets and
// measure most of them nowhere; a page that listed only what happens to have a
// harness would hide exactly the gap it was written to close.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// publishedBudget is one row of the table this product publishes in
// specs/quality/acceptance-standards.md (and, for the two below it, the
// capture and mobile chapters). Every one gets a row whether or not anything
// measures it.
type publishedBudget struct {
	id        string
	operation string
	budget    string
	// measuredBy names the make target that fills this row in, or is empty when
	// nothing measures it yet. An empty value is the honest statement of a gap,
	// and the page prints it as one.
	measuredBy string
}

// The published set, in the order acceptance-standards.md lists it. Hand-kept
// against the spec on purpose: it is the CLAIM side of the page, and deriving
// it from the records would let a budget disappear from the page simply by
// nobody measuring it — which is the failure this whole page exists to prevent.
var published = []publishedBudget{
	{"PERF-1", "Record open (person/org/deal)", "< 100 ms server", "bench-record"},
	{"PERF-2", "List/table view (50 rows, filtered)", "< 150 ms server", ""},
	{"PERF-3", "Search (full-text)", "< 200 ms", "bench-perf"},
	{"PERF-4", "Save/mutation", "< 150 ms server", "bench-record"},
	{"PERF-5", "AI baseline action (summary/draft)", "first token < 1.5 s", ""},
	{"PERF-6", "Cold start (single binary)", "< 2 s", ""},
	{"PERF-7", "Context-graph assembly", "< 300 ms at mid-market", "bench-perf"},
	{"CAP-PARAM-1", "Capture to timeline", "60 s p95", "bench-capture"},
	{"MOBILE-AC-2", "Record open, perceived, Fast-3G", "< 300 ms perceived", "bench-mobile"},
}

type machine struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPU       string `json:"cpu"`
	Cores     int    `json:"cores"`
	MemoryGiB int    `json:"memory_gib"`
	Toolchain string `json:"toolchain"`
	Postgres  string `json:"postgres,omitempty"`
	Network   string `json:"network,omitempty"`
	Viewport  string `json:"viewport,omitempty"`
}

type measurement struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	P99Ms    float64 `json:"p99_ms"`
	BudgetMs float64 `json:"budget_ms"`
	Samples  int     `json:"samples"`
	Caveat   string  `json:"caveat,omitempty"`
}

type record struct {
	Target     string        `json:"target"`
	MeasuredOn string        `json:"measured_on"`
	Machine    machine       `json:"machine"`
	Budgets    []measurement `json:"budgets"`
}

const (
	recordDir = "../docs/reference/perfbench"
	pagePath  = "../docs/reference/performance-budgets.md"
)

func main() {
	records, err := loadRecords(recordDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-perfdoc: reading records: %v\n", err)
		os.Exit(1)
	}
	page := render(records)
	if err := os.WriteFile(pagePath, []byte(page), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "gen-perfdoc: writing %s: %v\n", pagePath, err)
		os.Exit(1)
	}
	fmt.Printf("gen-perfdoc: %s rendered from %d record(s)\n", pagePath, len(records))
}

// loadRecords reads every record in the directory, keyed by target. A missing
// directory is not an error: it is the state of a checkout where nobody has run
// a benchmark yet, and the page it produces — every row unmeasured — is a true
// statement about that checkout.
func loadRecords(dir string) (map[string]record, error) {
	records := map[string]record{}
	// Rooted at the record directory rather than joining names onto it. An
	// fs.FS cannot be walked out of, so a name that tried to reach outside it
	// resolves to nothing instead of being opened — which is the property
	// gosec's G304 actually asks about. Rooting the reads answers the question;
	// a waiver would only have declined to.
	fsys := os.DirFS(dir)
	entries, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		body, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return nil, err
		}
		var r record
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		records[r.Target] = r
	}
	return records, nil
}
