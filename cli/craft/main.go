// Command craft is the code-craftsmanship gate CLI: it reviews a PR against the
// rubric (review), and — in later subcommands — annotates findings into source
// markers, runs the residue gate, and evaluates the golden set. See docs/quality/craftsmanship.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/gradionhq/margince/cli/craft/gate"
	"github.com/gradionhq/margince/cli/craft/golden"
	"github.com/gradionhq/margince/cli/craft/learn"
	"github.com/gradionhq/margince/cli/craft/rubric"
	"github.com/gradionhq/margince/cli/craft/static"
	"github.com/gradionhq/margince/cli/craft/upstream"
	"github.com/gradionhq/margince/cli/craft/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "static":
		os.Exit(cmdStatic(os.Args[2:]))
	case "review":
		os.Exit(cmdReview(os.Args[2:]))
	case "verdict":
		os.Exit(cmdVerdict(os.Args[2:]))
	case "annotate":
		os.Exit(cmdAnnotate(os.Args[2:]))
	case "residue":
		os.Exit(cmdResidue(os.Args[2:]))
	case "eval":
		os.Exit(cmdEval(os.Args[2:]))
	case "dispute":
		os.Exit(cmdDispute(os.Args[2:]))
	case "version":
		os.Exit(cmdVersion())
	case "upstream":
		os.Exit(cmdUpstream(os.Args[2:]))
	default:
		usage()
	}
}

// cmdUpstream reads a verdict log (JSONL of upstream.Record) and prints the most
// frequent blocker categories as proposed AGENTS.md guardrail additions, plus the
// block-rate trend — the input to a human-ratified PR (architecture/17 §6).
func cmdUpstream(args []string) int {
	fs := flag.NewFlagSet("upstream", flag.ExitOnError)
	logPath := fs.String("log", "", "path to a verdict-log JSONL (upstream.Record per line)")
	topN := fs.Int("top", 3, "how many blocker categories to propose")
	windows := fs.Int("windows", 4, "block-rate trend windows")
	_ = fs.Parse(args)
	if *logPath == "" {
		return fail("upstream: --log is required")
	}
	raw, err := os.ReadFile(*logPath)
	if err != nil {
		return fail("read log: %v", err)
	}
	var records []upstream.Record
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var rec upstream.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return fail("parse log line: %v", err)
		}
		records = append(records, rec)
	}
	r, err := rubric.Load()
	if err != nil {
		return fail("load rubric: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"proposals":        upstream.Propose(records, *topN, r),
		"block_rate":       upstream.BlockRate(records),
		"block_rate_trend": upstream.BlockRateTrend(records, *windows),
	})
	return 0
}

// cmdVersion prints the pinned gate identity tuple stamped on every verdict.
func cmdVersion() int {
	t, err := version.Current()
	if err != nil {
		return fail("gate version: %v", err)
	}
	fmt.Println(t.String()) //nolint:forbidigo // CLI stdout output, not logging
	return 0
}

// pinnedModel returns the model id pinned in gate-version.json — the single
// source of truth for which model the reviewer calls.
func pinnedModel() string {
	if t, err := version.Current(); err == nil {
		return t.Model
	}
	return ""
}

// gateVersion resolves the pinned tuple to the gate_version string, falling back
// to the rubric version only if the tuple can't be composed.
func gateVersion(override string) string {
	if override != "" {
		return override
	}
	if t, err := version.Current(); err == nil {
		return t.String()
	}
	return "unpinned"
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  craft static   [--root .] [--json] [--strict] [path ...]  # deterministic AST checks; exit 1 on BLOCK")
	fmt.Fprintln(os.Stderr, "  craft review   --base <ref> --head <ref> [--root .] [--gate-version <v>]")
	fmt.Fprintln(os.Stderr, "  craft verdict  --result <result.json>   # exit 0 PASS, 1 BLOCK")
	fmt.Fprintln(os.Stderr, "  craft annotate --result <result.json> [--root .]  # write CRAFT-FIX markers")
	fmt.Fprintln(os.Stderr, "  craft residue  [--root .]   # exit 1 if any CRAFT-FIX/CRAFT-DISPUTE marker remains")
	fmt.Fprintln(os.Stderr, "  craft eval     [--min-precision 1.0]  # run the golden set; exit 1 on precision drop/regression")
	fmt.Fprintln(os.Stderr, "  craft dispute  [--root .]   # list the CRAFT-DISPUTE adjudication queue")
	fmt.Fprintln(os.Stderr, "  craft version  # print the pinned gate identity tuple")
	fmt.Fprintln(os.Stderr, "  craft upstream --log <verdicts.jsonl>  # propose AGENTS.md guardrails + block-rate trend")
	os.Exit(2)
}

// cmdDispute prints the adjudication queue: the CRAFT-DISPUTE markers in the tree,
// each routed to a human (not a merge override — the residue gate still blocks).
func cmdDispute(args []string) int {
	fs := flag.NewFlagSet("dispute", flag.ExitOnError)
	root := fs.String("root", ".", "repo root")
	_ = fs.Parse(args)
	markers, err := gate.Collect(*root, gate.CraftToolDir)
	if err != nil {
		return fail("scan disputes: %v", err)
	}
	queue := learn.DisputesFromMarkers(markers)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(queue); err != nil {
		return fail("encode queue: %v", err)
	}
	fmt.Fprintf(os.Stderr, "%d open dispute(s) for adjudication\n", len(queue))
	return 0
}

// cmdEval runs the gate over the golden set and fails (exit 1) if BLOCK precision
// drops below the floor or any confirmed case regressed. This is the calibration
// gate that keeps a no-override hard block safe to leave merge-blocking.
func cmdEval(args []string) int {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	minPrecision := fs.Float64("min-precision", 1.0, "minimum acceptable BLOCK precision")
	gvFlag := fs.String("gate-version", "", "pinned gate version (defaults to the pinned tuple)")
	_ = fs.Parse(args)

	r, err := rubric.Load()
	if err != nil {
		return fail("load rubric: %v", err)
	}
	corpus, err := golden.Load()
	if err != nil {
		return fail("load corpus: %v", err)
	}
	gv := gateVersion(*gvFlag)
	client, err := gate.NewAnthropicClient(pinnedModel())
	if err != nil {
		return fail("inference client: %v", err)
	}

	outcomes, err := golden.Run(context.Background(), gate.NewReviewer(client, r, gv), corpus.Cases)
	if err != nil {
		return fail("golden run: %v", err)
	}
	report := golden.Evaluate(outcomes, *minPrecision)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if !report.Pass {
		fmt.Fprintf(os.Stderr, "eval: FAIL — BLOCK precision %.3f (min %.3f), %d regressed case(s)\n",
			report.Metrics.BlockPrecision, *minPrecision, len(report.Mismatches))
		return 1
	}
	fmt.Fprintf(os.Stderr, "eval: PASS — BLOCK precision %.3f, slop recall %.3f\n",
		report.Metrics.BlockPrecision, report.Metrics.SlopRecall)
	return 0
}

// cmdResidue is the deterministic residue gate: it fails (exit 1) if any
// CRAFT marker remains in the tree, so no marker can reach a merged commit.
func cmdResidue(args []string) int {
	fs := flag.NewFlagSet("residue", flag.ExitOnError)
	root := fs.String("root", ".", "repo root")
	_ = fs.Parse(args)
	markers, err := gate.Residue(*root)
	if err != nil {
		return fail("residue scan: %v", err)
	}
	if len(markers) == 0 {
		fmt.Fprintln(os.Stderr, "residue gate: clean (no CRAFT markers)")
		return 0
	}
	for _, m := range markers {
		fmt.Fprintf(os.Stderr, "%s:%d: %s[%s] must be resolved before merge\n", m.File, m.Line, m.Kind, m.ID)
	}
	fmt.Fprintf(os.Stderr, "residue gate: %d CRAFT marker(s) remain — fix the code and delete them\n", len(markers))
	return 1
}

// cmdAnnotate materializes the blocking findings of a result as in-source
// CRAFT-FIX markers, so the residue gate holds the merge until they are fixed.
func cmdAnnotate(args []string) int {
	fs := flag.NewFlagSet("annotate", flag.ExitOnError)
	path := fs.String("result", "", "path to the review result JSON")
	root := fs.String("root", ".", "repo root")
	_ = fs.Parse(args)
	if *path == "" {
		return fail("annotate: --result is required")
	}
	r, err := rubric.Load()
	if err != nil {
		return fail("load rubric: %v", err)
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		return fail("read result: %v", err)
	}
	var res gate.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return fail("parse result: %v", err)
	}
	blocking := gate.Blocking(res.Findings, r)
	if err := gate.Annotate(*root, blocking); err != nil {
		return fail("annotate: %v", err)
	}
	fmt.Fprintf(os.Stderr, "craft: wrote %d CRAFT-FIX marker(s)\n", len(blocking))
	return 0
}

// cmdVerdict reads a result JSON and exits non-zero on BLOCK — the merge-blocking
// step of the Action. The verdict in the file was computed in code (B-EP11.2b),
// so this is a pure read of that decision.
func cmdVerdict(args []string) int {
	fs := flag.NewFlagSet("verdict", flag.ExitOnError)
	path := fs.String("result", "", "path to the review result JSON")
	_ = fs.Parse(args)
	if *path == "" {
		return fail("verdict: --result is required")
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		return fail("read result: %v", err)
	}
	var res gate.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return fail("parse result: %v", err)
	}
	if res.Verdict == gate.VerdictBlock {
		fmt.Fprintln(os.Stderr, "craftsmanship: BLOCK — fix the CRAFT-FIX markers and repush")
		return 1
	}
	fmt.Fprintln(os.Stderr, "craftsmanship: PASS")
	return 0
}

// cmdReview assembles the PR context, runs the reviewer over the inference seam,
// and prints the canonical result JSON to stdout.
func cmdReview(args []string) int {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	base := fs.String("base", "main", "base ref")
	head := fs.String("head", "HEAD", "head ref")
	root := fs.String("root", ".", "repo root")
	gvFlag := fs.String("gate-version", "", "pinned gate version (defaults to the pinned tuple)")
	_ = fs.Parse(args)

	r, err := rubric.Load()
	if err != nil {
		return fail("load rubric: %v", err)
	}
	gv := gateVersion(*gvFlag)

	// Before activation there is no API key. Skip cleanly with a PASS rather than
	// erroring — a missing key is an un-activated gate, not a failed review, so the
	// check stays green (and the dark-factory orchestrator keeps merging).
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return emitResult(&gate.Result{
			GateVersion: gv, Verdict: gate.VerdictPass,
			Findings:   []gate.Finding{},
			Scratchpad: "skipped: ANTHROPIC_API_KEY unset (craftsmanship gate not yet activated)",
		})
	}

	ctx := context.Background()
	in, err := gate.NewAssembler(*root).Assemble(ctx, *base, *head)
	if err != nil {
		return fail("assemble inputs: %v", err)
	}
	client, err := gate.NewAnthropicClient(pinnedModel())
	if err != nil {
		return fail("inference client: %v", err)
	}
	res, err := gate.NewReviewer(client, r, gv).Review(ctx, in)
	if err != nil {
		return fail("review: %v", err)
	}
	return emitResult(res)
}

// emitResult prints the canonical result JSON to stdout.
func emitResult(res *gate.Result) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return fail("encode result: %v", err)
	}
	return 0
}

// cmdStatic runs the deterministic arm (architecture/15 §4, ADR-0045 Am.1):
// stdlib-only AST checks for the objectively-decidable anti-tells. It needs no
// API key and no compile of the target, so it runs first and cheaply — before
// the Critic Agent spends tokens on the heuristic calls. Exit 1 on BLOCK.
func cmdStatic(args []string) int {
	fs := flag.NewFlagSet("static", flag.ContinueOnError)
	root := fs.String("root", ".", "repo root to scan when no explicit paths are given")
	asJSON := fs.Bool("json", false, "emit canonical JSON instead of text")
	strict := fs.Bool("strict", false, "treat MAJOR findings as blocking too")
	fileMax := fs.Int("max-file-lines", 500, "large-file threshold")
	funcMax := fs.Int("max-func-lines", 80, "long-func body-line threshold")
	if err := fs.Parse(args); err != nil {
		return fail("static: %v", err)
	}

	// Explicit path args win over --root, so a pre-push hook can scope the
	// scan to just the changed files (diff-scoped enforcement); with none, we
	// walk --root.
	paths := fs.Args()
	if len(paths) == 0 {
		paths = []string{*root}
	}
	report, err := static.Run(paths, static.Config{
		MaxFileLines: *fileMax, MaxFuncLines: *funcMax, Strict: *strict,
	})
	if err != nil {
		return fail("static: %v", err)
	}
	if *asJSON {
		if err := report.WriteJSON(os.Stdout); err != nil {
			return fail("encode: %v", err)
		}
	} else {
		report.WriteText(os.Stdout)
	}
	if report.Verdict == "BLOCK" {
		return 1
	}
	return 0
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "craft: "+format+"\n", a...)
	return 1
}
