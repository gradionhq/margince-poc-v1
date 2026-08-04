// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// `worker aitask` — the DB-less probe for one production AI invocation site.
// It drives the SAME certification case `make e2e-ai` drives, but from input an
// operator supplies rather than the committed corpus, and it reports every
// boundary between that input and the verdict as numbers.
//
// The gap it fills: certification measures a model against a fixture, so a site
// can be certified and still fail on the input production actually hands it.
// The corpus says whether the model is good enough for the prompt; this says
// whether the site survives this input.
//
// It knows exactly one thing about any site — fixture JSON in, Trace out — so a
// site added to the census is probeable here with no change to this file.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

// corpusDirDefault is relative to backend/, the cwd the make targets run from —
// matching reportcmd's own default for the sibling record tree.
const corpusDirDefault = "internal/compose/aicert/corpus"

// workDirDefault is where probe artifacts land. Under .tmp/ because it is
// gitignored: a fetched page or a real fixture carries whatever the probed
// source carried, and a probe must not be able to leave that somewhere a commit
// would pick it up. It sits beside .tmp/aicert, the payload trace's own sink.
// Relative to the repo root; the make target passes an absolute path.
const workDirDefault = ".tmp/aitask"

// The verbs. Each is a different question, which is why they are separate
// rather than flags on one: list asks what exists, scaffold asks what a
// fixture looks like, fetch produces input, run spends money.
const (
	verbList     = "list"
	verbScaffold = "scaffold"
	verbFetch    = "fetch"
	verbRun      = "run"
)

type aiTaskFlags struct {
	verb string
	// arg is the verb's positional: the site for scaffold, the URL for fetch.
	arg string

	site         string
	scenarioPath string
	fixturePath  string
	expectPath   string

	routingPath string
	modelSpec   string
	fakeBrain   bool

	jsonPath  string
	dumpDir   string
	corpusDir string
	workDir   string
	outPath   string

	logLevel  string
	logFormat string
}

func parseAITaskFlags(args []string) (aiTaskFlags, error) {
	if len(args) == 0 {
		return aiTaskFlags{}, fmt.Errorf("aitask needs a verb: %s, %s, %s or %s",
			verbList, verbScaffold, verbFetch, verbRun)
	}
	cfg := aiTaskFlags{verb: args[0]}
	switch cfg.verb {
	case verbList, verbScaffold, verbFetch, verbRun:
	default:
		return aiTaskFlags{}, fmt.Errorf("aitask: unknown verb %q — want %s, %s, %s or %s",
			cfg.verb, verbList, verbScaffold, verbFetch, verbRun)
	}

	fs := flag.NewFlagSet("worker aitask "+cfg.verb, flag.ContinueOnError)
	fs.StringVar(&cfg.site, "site", "", "invocation site as <task>/<variant> (e.g. rate_extract/pricing)")
	fs.StringVar(&cfg.scenarioPath, "scenario", "", "scenario file in the corpus format, carrying both fixture and expectation")
	fs.StringVar(&cfg.fixturePath, "fixture", "", "fixture JSON file; needs --site, and --expect for sites that validate one")
	fs.StringVar(&cfg.expectPath, "expect", "", "expected-answer JSON file, the half --fixture does not carry")
	fs.StringVar(&cfg.routingPath, "ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml")
	fs.StringVar(&cfg.modelSpec, "model", "", "direct model override, provider:model (e.g. anthropic:claude-sonnet-4-6)")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "offline fake model: drives the seam without spending anything")
	fs.StringVar(&cfg.jsonPath, "json", "", "write the machine-readable probe result here ('-' = stdout)")
	fs.StringVar(&cfg.dumpDir, "dump-request", "", "directory to write each post-stripper request into")
	fs.StringVar(&cfg.corpusDir, "corpus", corpusDirDefault, "corpus directory, read by list and scaffold")
	fs.StringVar(&cfg.workDir, "work-dir", envOr("MARGINCE_AITASK_DIR", workDirDefault),
		"gitignored directory probe artifacts are written to; they carry whatever the probed source carried")
	fs.StringVar(&cfg.outPath, "out", "", "write this verb's artifact here instead of the work directory ('-' = stdout)")
	fs.StringVar(&cfg.logLevel, "log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&cfg.logFormat, "log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")

	// stdlib flag stops at the first positional; re-parsing the remainder lets
	// the positional and the flags interleave, as siteread's seeds do.
	rest := args[1:]
	var positionals []string
	for {
		if err := fs.Parse(rest); err != nil {
			return aiTaskFlags{}, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positionals = append(positionals, rest[0])
		rest = rest[1:]
	}
	if len(positionals) > 1 {
		return aiTaskFlags{}, fmt.Errorf("aitask %s takes one positional, got %d: %s",
			cfg.verb, len(positionals), strings.Join(positionals, " "))
	}
	if len(positionals) == 1 {
		cfg.arg = positionals[0]
	}

	if err := cfg.validate(); err != nil {
		return aiTaskFlags{}, err
	}
	return cfg, nil
}

// validate refuses the combinations that could only fail later, and names the
// flag that would fix each — a probe that dies after a paid call on something
// knowable up front has spent money to say nothing.
func (c aiTaskFlags) validate() error {
	switch c.verb {
	case verbScaffold:
		if c.arg == "" && c.site == "" {
			return errors.New("aitask scaffold needs a site: <task>/<variant>, e.g. rate_extract/pricing")
		}
	case verbFetch:
		if c.arg == "" {
			return errors.New("aitask fetch needs a url")
		}
	case verbRun:
		switch {
		case c.scenarioPath == "" && c.fixturePath == "":
			return errors.New("aitask run needs --scenario or --fixture")
		case c.scenarioPath != "" && c.fixturePath != "":
			return errors.New("aitask run takes --scenario or --fixture, not both — they disagree about what is being probed")
		case c.fixturePath != "" && c.siteRef() == "":
			return errors.New("aitask run --fixture needs --site: a fixture names no site, and only the site says which code probes it")
		}
	}
	return nil
}

// artifactOut is where this verb's artifact goes: the operator's --out when
// they named one, otherwise the gitignored work directory under the given name.
func (c aiTaskFlags) artifactOut(name string) string {
	if c.outPath != "" {
		return c.outPath
	}
	return artifactPath(c.workDir, name)
}

// siteRef is the site the run is bound to, from either spelling.
func (c aiTaskFlags) siteRef() string {
	if c.site != "" {
		return c.site
	}
	return c.arg
}

// runAITaskProbe is the subcommand entry point, dispatched from run() before
// the worker flags — which would otherwise demand a DSN this never uses.
func runAITaskProbe(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := parseAITaskFlags(args)
	if err != nil {
		return err
	}
	handler, err := httpserver.LogHandler(stdout, cfg.logLevel, cfg.logFormat)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(handler))

	// The census is what turns a site name into runnable code, so every verb
	// but fetch needs it before it can do anything.
	if cfg.verb == verbFetch {
		return fetchPage(ctx, stdout, cfg.arg, cfg.artifactOut(fetchArtifactName(cfg.arg)))
	}
	census, err := compose.NewTaskCensus()
	if err != nil {
		return fmt.Errorf("aitask: %w", err)
	}
	switch cfg.verb {
	case verbList:
		return listSites(stdout, census, cfg.corpusDir)
	case verbScaffold:
		out := cfg.artifactOut(slugify(strings.ReplaceAll(cfg.siteRef(), "/", "_")) + ".yaml")
		return scaffoldSite(stdout, census, cfg.corpusDir, cfg.siteRef(), out)
	default:
		return runProbe(ctx, stdout, census, cfg)
	}
}

// listSites prints one row per registered site: what it is, how much of
// production a probe of it exercises, which tiers answer it, and whether the
// corpus already carries a scenario to scaffold from.
//
// The rows come from the census rather than a list kept here, so a site added
// to the composition is listed without this file being touched.
func listSites(w io.Writer, census *aitasks.Registry, corpusDir string) error {
	withCorpus, err := sitesWithCorpus(census, corpusDir)
	if err != nil {
		return err
	}
	sites := census.All()
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Task != sites[j].Task {
			return sites[i].Task < sites[j].Task
		}
		return sites[i].Variant < sites[j].Variant
	})

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SITE\tKIND\tSCOPE\tLADDER\tCORPUS"); err != nil {
		return err
	}
	for _, site := range sites {
		ladder := make([]string, 0, len(ai.TaskLadder(site.Task)))
		for _, tier := range ai.TaskLadder(site.Task) {
			ladder = append(ladder, string(tier))
		}
		corpus := "no"
		if withCorpus[siteKey(site)] {
			corpus = "yes"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			siteKey(site), site.Kind, site.CertifiedScope(), strings.Join(ladder, ","), corpus); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// sitesWithCorpus reports which sites the corpus already carries a scenario
// for. A corpus that cannot be read is reported as an error rather than as
// "no site has one": the two look identical in the output and want opposite
// fixes.
func sitesWithCorpus(census *aitasks.Registry, corpusDir string) (map[string]bool, error) {
	scenarios, err := aicert.LoadCorpus(corpusDir, census)
	if err != nil {
		return nil, fmt.Errorf("aitask: reading the corpus: %w", err)
	}
	have := make(map[string]bool, len(scenarios))
	for _, sc := range scenarios {
		have[sc.Task+"/"+sc.Site] = true
	}
	return have, nil
}

func siteKey(s aitasks.Site) string { return string(s.Task) + "/" + s.Variant }

// scaffoldSite prints a runnable starter scenario for one site, copied from the
// corpus. It is how a fixture's SHAPE is discovered: every site takes a
// different one, and reading the Go type is not something an operator should
// have to do to run a probe.
func scaffoldSite(w io.Writer, census *aitasks.Registry, corpusDir, siteRef, outPath string) error {
	site, err := resolveSite(census, siteRef)
	if err != nil {
		return err
	}
	scenarios, err := aicert.LoadCorpus(corpusDir, census)
	if err != nil {
		return fmt.Errorf("aitask: reading the corpus: %w", err)
	}
	for _, sc := range scenarios {
		if sc.Task != string(site.Task) || sc.Site != site.Variant {
			continue
		}
		body, err := aicert.RenderScenario(sc)
		if err != nil {
			return err
		}
		header := []byte("# Scaffolded by `worker aitask scaffold` — edit the fixture, keep the shape.\n" +
			"# This file is yours: it is NOT part of the corpus, and nothing reads it but --scenario.\n")
		return emitArtifact(w, outPath, append(header, body...),
			fmt.Sprintf("scaffolded %s from the corpus scenario %q", siteKey(site), sc.Name))
	}
	return fmt.Errorf("aitask: %s has no corpus scenario to scaffold from — hand-write one, or probe with --fixture", siteKey(site))
}

// emitArtifact writes a probe artifact to disk and reports where it went, or to
// stdout when the operator asked for "-".
//
// Disk is the default because these artifacts carry whatever the probed source
// carried — a fetched page, a real fixture — and that can be sensitive. They go
// under the gitignored work directory so a probe cannot leave customer content
// somewhere a commit would pick it up.
func emitArtifact(w io.Writer, outPath string, content []byte, what string) error {
	if outPath == "-" {
		_, err := w.Write(content)
		return err
	}
	// The destination is the operator's own --out or --work-dir, supplied on
	// their own command line in a DB-less local debug tool. There is no
	// untrusted principal here to traverse anywhere: naming where your artifact
	// lands is the flag's entire purpose.
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil { // #nosec G703 -- destination is the operator's own flag, not request input
		return fmt.Errorf("aitask: %w", err)
	}
	if err := os.WriteFile(outPath, content, 0o600); err != nil { // #nosec G703 -- destination is the operator's own flag, not request input
		return fmt.Errorf("aitask: %w", err)
	}
	_, err := fmt.Fprintf(w, "%s → %s\n", what, outPath)
	return err
}

// artifactPath is where a verb's output lands when the operator named no path:
// under the gitignored work directory, named for what produced it.
func artifactPath(workDir, name string) string {
	return filepath.Join(workDir, name)
}

// slugify reduces an arbitrary string to something safe to use as a filename.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// resolveSite turns "<task>/<variant>" into a registered site, naming the near
// matches when it cannot: a mistyped variant is the common case, and a bare
// "unknown site" leaves the operator to go read the registry.
func resolveSite(census *aitasks.Registry, siteRef string) (aitasks.Site, error) {
	task, variant, found := strings.Cut(siteRef, "/")
	if !found || task == "" || variant == "" {
		return aitasks.Site{}, fmt.Errorf("aitask: %q is not a site — want <task>/<variant>, e.g. rate_extract/pricing", siteRef)
	}
	site, ok := census.Lookup(ai.Task(task), variant)
	if ok {
		return site, nil
	}
	var near []string
	for _, s := range census.All() {
		if string(s.Task) == task {
			near = append(near, siteKey(s))
		}
	}
	if len(near) > 0 {
		sort.Strings(near)
		return aitasks.Site{}, fmt.Errorf("aitask: no site %q; %s ships %s", siteRef, task, strings.Join(near, ", "))
	}
	return aitasks.Site{}, fmt.Errorf("aitask: no site %q — `aitask list` names every site this build ships", siteRef)
}

// fetchPage runs the PRODUCTION fetch and prints the reduced text: the exact
// bytes the extraction sites are handed, post-StripTags, not the raw body. That
// is what makes assembling a fixture by hand faithful rather than approximate.
//
// The text goes to stdout and the boundary line to stderr, so the output pipes
// into a file or jq without the diagnostics contaminating it.
func fetchPage(ctx context.Context, stdout io.Writer, rawURL, outPath string) error {
	doc, err := webread.New().Fetch(ctx, rawURL)
	if err != nil {
		return fmt.Errorf("aitask fetch %s: %w", rawURL, err)
	}
	if _, err := fmt.Fprintln(stdout, fetchBoundaryLine(doc)); err != nil {
		return err
	}
	return emitArtifact(stdout, outPath, []byte(doc.Text), "fetched "+rawURL)
}

// fetchArtifactName names a fetch after the URL it came from, so two fetches in
// one session do not overwrite each other.
func fetchArtifactName(rawURL string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	return "fetch-" + slugify(trimmed) + ".txt"
}

// fetchBoundaryLine reports what the fetch produced in the terms the extraction
// path cares about. Passages is the count numberPassages would emit — a body
// that is one long line numbers to ONE passage however many bytes it carries,
// which is invisible in a byte count and decisive for whether evidence
// citation can mean anything.
func fetchBoundaryLine(doc webread.Doc) string {
	passages := 0
	for _, line := range strings.Split(doc.Text, "\n") {
		if strings.TrimSpace(line) != "" {
			passages++
		}
	}
	mediaType := doc.MediaType
	if mediaType == "" {
		mediaType = "(none declared)"
	}
	return fmt.Sprintf("fetched  media=%s  bytes=%d  passages=%d  markdown=%t",
		mediaType, len(doc.Text), passages, doc.IsMarkdown())
}
