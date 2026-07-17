// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// `worker siteread` — the deep read's DB-less debug loop: crawl and
// extract one or more sites in memory (no Postgres, no Redis, no
// staging) and print everything the production dossier keeps to itself.
// This is the tuning surface for ingestion quality; the worker's normal
// boot path is untouched (main dispatches here before flag parsing).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

type siteReadFlags struct {
	maxPages    int
	maxBytes    int
	wall        time.Duration
	routingPath string
	modelSpec   string
	fakeBrain   bool
	jsonPath    string
	dumpDir     string
	urlsFile    string
	logLevel    string
	logFormat   string
	seeds       []string
}

func parseSiteReadFlags(args []string) (siteReadFlags, error) {
	fs := flag.NewFlagSet("worker siteread", flag.ContinueOnError)
	var cfg siteReadFlags
	fs.IntVar(&cfg.maxPages, "max-pages", 0, "crawl page cap; 0 takes the built-in default")
	fs.IntVar(&cfg.maxBytes, "max-bytes", 0, "crawl aggregate byte cap; 0 takes the built-in default")
	fs.DurationVar(&cfg.wall, "wall", 0, "crawl wall clock; 0 takes the built-in default")
	fs.StringVar(&cfg.routingPath, "ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml")
	fs.StringVar(&cfg.modelSpec, "model", "", "direct model override, provider:model (e.g. anthropic:claude-sonnet-4-6)")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "offline fake model: crawl dry-run, extraction yields nothing")
	fs.StringVar(&cfg.jsonPath, "json", "", "write the machine-readable report here ('-' = stdout). Diff two runs with: jq 'del(.crawl.duration_ms, .crawl.pages[].fetch_ms, .model_calls[].latency_ms)'")
	fs.StringVar(&cfg.dumpDir, "dump-pages", "", "directory to save each fetched page's reduced text into")
	fs.StringVar(&cfg.urlsFile, "urls-file", "", "file of seed URLs, one per line (# comments allowed)")
	fs.StringVar(&cfg.logLevel, "log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&cfg.logFormat, "log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")
	// stdlib flag stops at the first positional; re-parsing the remainder
	// lets URLs and flags interleave (`siteread https://x.de --ai-fake`).
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return siteReadFlags{}, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		cfg.seeds = append(cfg.seeds, rest[0])
		rest = rest[1:]
	}
	if cfg.maxPages < 0 || cfg.maxBytes < 0 || cfg.wall < 0 {
		return siteReadFlags{}, errors.New("the caps must be zero (default) or positive")
	}

	if cfg.urlsFile != "" {
		fromFile, err := readSeedFile(cfg.urlsFile)
		if err != nil {
			return siteReadFlags{}, err
		}
		cfg.seeds = append(cfg.seeds, fromFile...)
	}
	if len(cfg.seeds) == 0 {
		return siteReadFlags{}, errors.New("no seed URL: pass one or more URLs, or --urls-file")
	}
	for i, seed := range cfg.seeds {
		cfg.seeds[i] = normalizeSeed(seed)
	}
	return cfg, nil
}

// normalizeSeed lets the operator type a bare domain; the crawler itself
// only speaks absolute http(s).
func normalizeSeed(seed string) string {
	if strings.HasPrefix(seed, "http://") || strings.HasPrefix(seed, "https://") {
		return seed
	}
	return "https://" + seed
}

func readSeedFile(path string) ([]string, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-chosen input file
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			slog.Warn("closing the urls file", "path", path, "err", cerr)
		}
	}()
	var seeds []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seeds = append(seeds, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return seeds, nil
}

// runSiteReadDebug is the subcommand: every seed gets a full crawl +
// extract + report; one seed's failure is reported and does not stop
// the rest. Non-nil return = at least one seed failed.
func runSiteReadDebug(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := parseSiteReadFlags(args)
	if err != nil {
		return err
	}
	handler, err := httpserver.LogHandler(stdout, cfg.logLevel, cfg.logFormat)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(handler))

	brain, banner, err := compose.SiteReadDebugBrain(cfg.routingPath, cfg.modelSpec, cfg.fakeBrain)
	if err != nil {
		return err
	}
	caps := compose.CrawlCaps{MaxPages: cfg.maxPages, MaxBytes: cfg.maxBytes, Wall: cfg.wall}

	var reports []compose.SiteReadDebugReport
	var failures []error
	for _, seed := range cfg.seeds {
		_, _ = fmt.Fprintf(stdout, "\n=== %s (model: %s)\n", seed, banner)
		report, err := compose.RunSiteReadDebug(ctx, compose.SiteReadDebugOptions{
			SeedURL:         seed,
			Caps:            caps,
			Brain:           brain,
			IncludePageText: cfg.dumpDir != "",
		})
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", seed, err))
			_, _ = fmt.Fprintf(stdout, "FAILED: %v\n", err)
			continue
		}
		if cfg.dumpDir != "" {
			if err := dumpPages(cfg.dumpDir, &report); err != nil {
				return err
			}
		}
		renderSiteReadReport(stdout, report)
		reports = append(reports, report)
	}

	if cfg.jsonPath != "" {
		if err := writeJSONReport(cfg.jsonPath, stdout, reports); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}

// dumpPages writes each fetched page's reduced text to
// <dir>/<n>-<slug>.txt and then strips the text out of the report, so
// the JSON output stays diffable rather than dwarfed by page bodies.
func dumpPages(dir string, report *compose.SiteReadDebugReport) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for i := range report.Crawl.Pages {
		page := &report.Crawl.Pages[i]
		name := fmt.Sprintf("%02d-%s.txt", i, slugifyURL(page.URL))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(page.Text), 0o600); err != nil {
			return err
		}
		page.Text = ""
	}
	return nil
}

func slugifyURL(rawURL string) string {
	slug := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	slug = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		default:
			return '_'
		}
	}, slug)
	const maxSlug = 80
	if len(slug) > maxSlug {
		slug = slug[:maxSlug]
	}
	return slug
}

func writeJSONReport(path string, stdout io.Writer, reports []compose.SiteReadDebugReport) error {
	var payload any = reports
	if len(reports) == 1 {
		payload = reports[0]
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if path == "-" {
		_, err := stdout.Write(encoded)
		return err
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "\nJSON report written to %s\n", path)
	return nil
}

// renderSiteReadReport prints the human-readable side: what was read,
// what was skipped, what was extracted with its evidence, how the merge
// decided conflicts, and what every model call cost.
func renderSiteReadReport(w io.Writer, r compose.SiteReadDebugReport) {
	renderCrawl(w, r)
	renderExtraction(w, r)
	renderModelCalls(w, r)
}

func renderCrawl(w io.Writer, r compose.SiteReadDebugReport) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

	stopped := r.Crawl.StoppedReason
	if stopped == "" {
		stopped = "discovery exhausted"
	}
	p("crawl: %d pages, %d skipped, %s in %s (caps %d pages / %s / %s wall)\n",
		len(r.Crawl.Pages), len(r.Crawl.Skipped), stopped,
		time.Duration(r.Crawl.DurationMs)*time.Millisecond,
		r.Caps.MaxPages, byteSize(r.Caps.MaxBytes), time.Duration(r.Caps.WallMs)*time.Millisecond)

	p("\nPAGES\n")
	for _, page := range r.Crawl.Pages {
		note := ""
		if !page.Extracted {
			note = "  [NOT extracted: model lane stopped]"
		}
		p("  %-10s %7s %6dms  %s%s\n", page.Kind, byteSize(page.Bytes), page.FetchMs, page.URL, note)
	}
	if len(r.Crawl.Skipped) > 0 {
		p("\nSKIPPED\n")
		for _, skip := range r.Crawl.Skipped {
			p("  %-12s %s\n", skip.Reason, skip.URL)
		}
	}
}

func renderExtraction(w io.Writer, r compose.SiteReadDebugReport) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

	p("\nFIELDS (%d)\n", len(r.Extraction.Fields))
	for _, f := range r.Extraction.Fields {
		p("  %-20s %.2f  %q\n", f.Field, f.Confidence, f.Value)
		p("  %20s       evidence: %q  (%s)\n", "", truncate(f.EvidenceSnippet, 100), f.SourceURL)
	}
	p("\nFACTS (%d)\n", len(r.Extraction.Facts))
	for _, f := range r.Extraction.Facts {
		p("  %-9s %-15s %.2f  %q  (%s)\n", f.Category, f.Field, f.Confidence, truncate(f.Value, 80), f.SourceURL)
	}
	p("\nPEOPLE (%d)\n", len(r.Extraction.People))
	for _, person := range r.Extraction.People {
		contact := ""
		if person.PublishedEmail != "" {
			contact += "  " + person.PublishedEmail
		}
		if person.LinkedinURL != "" {
			contact += "  " + person.LinkedinURL
		}
		p("  %s — %s%s  (%s)\n", person.Name, person.Role, contact, person.SourceURL)
	}

	if len(r.Extraction.Dropped) > 0 {
		p("\nDROPPED BY THE EVIDENCE GATE (%d)\n", len(r.Extraction.Dropped))
		for _, d := range r.Extraction.Dropped {
			p("  %-24s %-18s %-20s %q  (%s)\n", d.Reason, d.Lane, d.Field, truncate(d.Value, 60), d.PageURL)
		}
	}

	if len(r.Extraction.MergeDecisions) > 0 {
		p("\nMERGE DECISIONS\n")
		for _, d := range r.Extraction.MergeDecisions {
			p("  %s: kept %q from %s\n", d.Field, truncate(d.WinnerValue, 60), d.WinnerSource)
			for _, loser := range d.Losers {
				p("      over %q from %s\n", truncate(loser.Value, 60), loser.Source)
			}
		}
	}
}

func renderModelCalls(w io.Writer, r compose.SiteReadDebugReport) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

	var tokensIn, tokensOut, callErrors int
	for _, call := range r.ModelCalls {
		tokensIn += call.InputTokens
		tokensOut += call.OutputTokens
		if call.Error != "" {
			callErrors++
		}
	}
	p("\nMODEL CALLS (%d; %d in / %d out tokens, %d errors)\n", len(r.ModelCalls), tokensIn, tokensOut, callErrors)
	for _, call := range r.ModelCalls {
		errNote := ""
		if call.Error != "" {
			errNote = "  ERROR: " + call.Error
		}
		p("  %-18s %6dms %6d/%-5d  %s%s\n", call.Lane, call.LatencyMs, call.InputTokens, call.OutputTokens, call.PageURL, errNote)
	}
	if r.ModelLaneError != "" {
		p("\nMODEL LANE STOPPED MIDWAY: %s\n", r.ModelLaneError)
	}
	for _, warning := range r.Warnings {
		p("\nWARNING: %s\n", warning)
	}
	if r.Proposal == nil {
		p("\nPROPOSAL: none — nothing survived the evidence gate\n")
	}
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

func byteSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
