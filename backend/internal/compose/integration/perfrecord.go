// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The record a benchmark run leaves behind, so a number nobody re-ran is still
// readable — and still says what it was measured on.
//
// This is the aicert shape, not the rbac-matrix one, and the difference is the
// whole design. rbac-matrix.md is DERIVED from the seeded policy, so it is
// drift-gated: rendering it twice must produce the same bytes or the gate
// fails. A latency is MEASURED, so rendering it twice never produces the same
// bytes and a drift gate on it would fail every run for everybody. What can be
// held to instead is what aicert holds its records to: say what the number was
// measured on, and never let a missing measurement read like a passing one.
//
// What is deliberately NOT captured: hostname, username, and any filesystem
// path. This record is committed to a public repository, and none of the three
// tells a reader why a number is what it is.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// perfRecordDir is where a run leaves its record, from the repository root. The
// records sit beside the page they render into, so a reader who finds one finds
// the other.
const perfRecordDir = "docs/reference/perfbench"

// PerfRecordDir resolves the record directory as an absolute path.
//
// It WALKS UP to the repository root rather than counting `..` segments the way
// identity's repoArtifact does. That helper is fixed at three levels because
// its package cannot move — it sits behind an import fence. These two suites
// sit at different depths (integration, and capture one below it), so a shared
// constant would be wrong for one of them, and a per-caller count is a number
// nobody re-derives when a package moves.
func PerfRecordDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	for {
		// go.work marks the repository root and nothing below it: the backend
		// module has a go.mod, the tools and extensions have their own, and only
		// the root has the workspace that ties them together.
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return filepath.Join(dir, perfRecordDir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.work above %s — the perf record has no repository root to write into", dir)
		}
		dir = parent
	}
}

// MachineFacts is the hardware a measurement is only true of. Every field is a
// string or a number a reader can compare against their own machine; a fact
// this process could not read says so rather than being omitted, because an
// absent field and an unreadable one mean different things.
type MachineFacts struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPU       string `json:"cpu"`
	Cores     int    `json:"cores"`
	MemoryGiB int    `json:"memory_gib"`
	Toolchain string `json:"toolchain"`
	Postgres  string `json:"postgres,omitempty"`
}

// BudgetMeasurement is one published budget and what this run measured for it.
// Durations are milliseconds because that is the unit every budget is published
// in; a reader comparing a row to acceptance-standards.md should not have to
// convert.
type BudgetMeasurement struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	P99Ms    float64 `json:"p99_ms"`
	BudgetMs float64 `json:"budget_ms"`
	Samples  int     `json:"samples"`
	// Caveat is set when the run did NOT meet the condition the budget binds
	// under, and the page prints it beside the verdict.
	//
	// PERF-7 is why this field exists: its budget binds at the mid-market tier,
	// the standing lane runs the SMB canary, and both produce a p95 under the
	// same id. A page that printed the canary's 9 ms as "within budget" would
	// be reporting a number that satisfies a bound it was never measured
	// against — true arithmetic, false claim.
	Caveat string `json:"caveat,omitempty"`
}

// PerfRecord is one target's whole run: the machine, the day, and every budget
// that target measures. One record per TARGET rather than per budget, so a
// partial run — only `make bench-capture`, say — rewrites only its own file and
// leaves the others standing with their own stamps.
type PerfRecord struct {
	Target     string              `json:"target"`
	MeasuredOn string              `json:"measured_on"`
	Machine    MachineFacts        `json:"machine"`
	Budgets    []BudgetMeasurement `json:"budgets"`
}

// RecordingEnabled reports whether this run should leave a record.
//
// It exists for ONE caller. The PERF-3/PERF-7 tier harness carries only the
// `integration` tag, so the standing CI lane runs it as a canary — and that lane
// must not write into the working tree. A scheduled job quietly dirtying a
// machine's numbers is how a published page ends up describing a runner nobody
// can find, on a day nobody chose. `make bench-perf` sets it; CI does not.
//
// The by-hand bench targets need no such switch: nothing but a human runs them,
// so a record is exactly what running one means.
func RecordingEnabled() bool { return os.Getenv("MARGINCE_BENCH_RECORD") == "1" }

// WritePerfRecord stamps the run and writes its record. A failure here fails
// the test: a benchmark that measured something and then silently failed to
// record it is how a page goes stale while every run looks green.
func WritePerfRecord(t *testing.T, target string, postgres string, budgets []BudgetMeasurement) {
	t.Helper()
	record := PerfRecord{
		Target: target,
		// The DAY, not the instant. A record that changed on every run would
		// churn the committed page for no reader's benefit; the day is what
		// answers "is this still current".
		MeasuredOn: time.Now().UTC().Format(time.DateOnly),
		Machine:    readMachineFacts(postgres),
		Budgets:    budgets,
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatalf("rendering the %s perf record: %v", target, err)
	}
	dir := PerfRecordDir(t)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	path := filepath.Join(dir, target+".json")
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	t.Logf("perfbench: wrote %s", path)
}

// readMachineFacts collects what a reader needs to know before trusting a
// number. Nothing here can fail the run: an unreadable fact renders as
// "unknown", because a benchmark is not worth failing over a CPU model string,
// and a reader can tell "unknown" from a wrong answer.
func readMachineFacts(postgres string) MachineFacts {
	return MachineFacts{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPU:       cpuModel(),
		Cores:     runtime.NumCPU(),
		MemoryGiB: memoryGiB(),
		Toolchain: runtime.Version(),
		Postgres:  postgres,
	}
}

// cpuModel reads the processor's own name. The two platforms this repo is
// developed and tested on answer in different places and neither is portable,
// so an unrecognized platform says so instead of guessing.
func cpuModel() string {
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), sysctlTimeout)
		defer cancel()
		// The key is spelled at the call site rather than passed in, so the
		// subprocess is launched with a literal. A helper taking the key as a
		// parameter reads better and is what this was, until gosec's G204
		// pointed out — correctly, as a rule — that it cannot tell a constant
		// argument from a caller-supplied one. Two literals cost less than a
		// waiver that would have to be re-justified by every later reader.
		if out, err := exec.CommandContext(ctx, "sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	case "linux":
		if model := procCPUInfoField("model name"); model != "" {
			return model
		}
	}
	return "unknown"
}

// sysctlTimeout bounds the two macOS kernel reads below. They normally answer
// in microseconds; the bound exists so a pathological case reports an unknown
// CPU instead of hanging the benchmark it is describing.
//
// sysctlTimeout is generous for a kernel read that normally answers in
// microseconds; it exists to bound a pathological case, not to race a healthy one.
const sysctlTimeout = 5 * time.Second

// procCPUInfoField pulls one field out of /proc/cpuinfo. Every core repeats the
// same model, so the first match is the answer.
func procCPUInfoField(field string) string {
	body, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		name, value, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(name) == field {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// memoryGiB reports installed memory, rounded to whole gibibytes — the
// precision a reader comparing machines actually uses. Zero means unread, and
// the page renders it as unknown rather than as a machine with no memory.
func memoryGiB() int {
	const bytesPerGiB = 1 << 30
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), sysctlTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return 0
		}
		return int(bytes / bytesPerGiB)
	case "linux":
		return linuxMemoryGiB()
	}
	return 0
}

// linuxMemoryGiB reads MemTotal, which /proc/meminfo reports in kibibytes.
func linuxMemoryGiB() int {
	body, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(body), "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != "MemTotal" {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return 0
		}
		kib, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return int(kib / (1 << 20))
	}
	return 0
}

// MeasurementFrom folds one recorded distribution into the record's shape.
// Milliseconds as a float rather than a Duration string, so the rendered page
// can align a column and a reader can compare two rows by eye.
func MeasurementFrom(id, name string, p50, p95, p99, budget time.Duration, samples int) BudgetMeasurement {
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	return BudgetMeasurement{
		ID: id, Name: name,
		P50Ms: ms(p50), P95Ms: ms(p95), P99Ms: ms(p99),
		BudgetMs: ms(budget), Samples: samples,
	}
}

// PostgresVersion asks the server what it is, for the record's machine block. A
// latency against Postgres is a claim about that server as much as about this
// code, and the two bench suites that touch a database both report it.
func PostgresVersion(query func(string) (string, error)) string {
	version, err := query("SHOW server_version")
	if err != nil {
		return "unknown"
	}
	// server_version carries the build's own suffix on some distributions
	// ("16.4 (Debian 16.4-1)"); the release is the part a reader compares.
	return strings.TrimSpace(strings.Fields(version)[0])
}
