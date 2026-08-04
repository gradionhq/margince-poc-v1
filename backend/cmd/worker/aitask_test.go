// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

// testCorpusDir resolves the corpus from THIS package's directory, which is
// where `go test` runs the binary — the flag default is relative to backend/,
// the cwd the make targets use, and would miss from here.
func testCorpusDir() string {
	return filepath.Join("..", "..", "internal", "compose", "aicert", "corpus")
}

func TestAITaskFlagsRefuseWhatCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no verb at all", nil, "verb"},
		{"a verb nobody serves", []string{"probe"}, "unknown verb"},
		{"scaffold with no site", []string{"scaffold"}, "site"},
		{"fetch with no url", []string{"fetch"}, "url"},
		{"run with neither input", []string{"run"}, "--scenario or --fixture"},
		{
			"run with both inputs, which disagree about what is being probed",
			[]string{"run", "--scenario", "s.yaml", "--fixture", "f.json"},
			"not both",
		},
		{"run from a fixture with no site to bind it to", []string{"run", "--fixture", "f.json"}, "--site"},
		{
			"a scenario already carries its expectation, so --expect would be ignored",
			[]string{"run", "--scenario", "s.yaml", "--expect", "e.json"},
			"already carries its expectation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAITaskFlags(tc.args)
			if err == nil {
				t.Fatalf("parseAITaskFlags(%q) = nil error, want one mentioning %q", tc.args, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("parseAITaskFlags(%q) error = %q, want it to mention %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestAITaskFlagsAcceptTheTwoRunnableInputs(t *testing.T) {
	fromScenario, err := parseAITaskFlags([]string{"run", "--scenario", "s.yaml"})
	if err != nil {
		t.Fatalf("a scenario alone is a complete probe input: %v", err)
	}
	if fromScenario.verb != "run" || fromScenario.scenarioPath != "s.yaml" {
		t.Errorf("parsed %+v, want verb=run scenarioPath=s.yaml", fromScenario)
	}

	fromFixture, err := parseAITaskFlags([]string{
		"run", "--site", "rate_extract/pricing", "--fixture", "f.json",
	})
	if err != nil {
		t.Fatalf("a fixture bound to a site is a complete probe input: %v", err)
	}
	if fromFixture.site != "rate_extract/pricing" || fromFixture.fixturePath != "f.json" {
		t.Errorf("parsed %+v, want site=rate_extract/pricing fixturePath=f.json", fromFixture)
	}
}

// Probe artifacts carry whatever the probed source carried, so they default
// into the gitignored work directory rather than the cwd — a fetched page must
// not be able to land somewhere a commit would pick it up.
func TestArtifactsDefaultIntoTheGitignoredWorkDir(t *testing.T) {
	cfg, err := parseAITaskFlags([]string{"fetch", "https://example.test/pricing"})
	if err != nil {
		t.Fatalf("parseAITaskFlags: %v", err)
	}
	if cfg.workDir != workDirDefault {
		t.Errorf("workDir = %q, want the gitignored default %q", cfg.workDir, workDirDefault)
	}
	got := cfg.artifactOut(fetchArtifactName(cfg.arg))
	want := filepath.Join(workDirDefault, "fetch-example.test_pricing.txt")
	if got != want {
		t.Errorf("artifactOut = %q, want %q", got, want)
	}

	// An explicit --out wins, so an operator who wants it elsewhere is not
	// fought with.
	chosen, err := parseAITaskFlags([]string{"fetch", "https://example.test/x", "--out", "/tmp/here.txt"})
	if err != nil {
		t.Fatalf("parseAITaskFlags: %v", err)
	}
	if got := chosen.artifactOut("ignored.txt"); got != "/tmp/here.txt" {
		t.Errorf("artifactOut = %q, want the operator's --out", got)
	}
}

func TestFetchArtifactNamesDistinguishTwoFetches(t *testing.T) {
	first := fetchArtifactName("https://openrouter.ai/api/v1/models")
	second := fetchArtifactName("https://ai.google.dev/gemini-api/docs/pricing")
	if first == second {
		t.Fatalf("two sources produced the same artifact name %q — one would overwrite the other", first)
	}
	if strings.ContainsAny(first, "/:") {
		t.Errorf("artifact name %q is not safe to use as a filename", first)
	}
}

func TestSlugifyKeepsWhatIsSafeAndReplacesTheRest(t *testing.T) {
	for in, want := range map[string]string{
		"openrouter.ai/api/v1/models": "openrouter.ai_api_v1_models",
		"rate_extract/pricing":        "rate_extract_pricing",
		"a b":                         "a_b",
		"__trimmed__":                 "trimmed",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// "-" is the escape hatch for piping, and it must not touch the disk.
func TestScaffoldToStdoutWritesNoFile(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	var out strings.Builder
	if err := scaffoldSite(&out, census, testCorpusDir(), "rate_extract/pricing", "-"); err != nil {
		t.Fatalf("scaffoldSite: %v", err)
	}
	if !strings.Contains(out.String(), "task: rate_extract") {
		t.Errorf("stdout scaffold must carry the scenario itself:\n%s", out.String())
	}
	if strings.Contains(out.String(), "→") {
		t.Error("nothing was written to disk, so no path may be reported")
	}
}

// The probe's reach is the census, so the listing is checked against the census
// itself rather than a hand-kept list: a site added to the composition appears
// here without this test being edited, and one that stops being listed fails it.
func TestAITaskListNamesEveryRegisteredSite(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	var out strings.Builder
	if err := listSites(&out, census, testCorpusDir()); err != nil {
		t.Fatalf("listSites: %v", err)
	}
	for _, site := range census.All() {
		want := string(site.Task) + "/" + site.Variant
		if !strings.Contains(out.String(), want) {
			t.Errorf("list omits %s — a site nobody can find is a site nobody can probe", want)
		}
	}
}

func TestAITaskListCarriesTheLadderAndScope(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	var out strings.Builder
	if err := listSites(&out, census, testCorpusDir()); err != nil {
		t.Fatalf("listSites: %v", err)
	}
	var row string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "rate_extract/pricing") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatal("no row for rate_extract/pricing")
	}
	// The ladder and the scope are the two things that say what a probe of this
	// site would actually exercise, so neither may be dropped from the row.
	for _, want := range []string{"one_shot", "full_invocation", "premium", "cheap_cloud"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q is missing %q", row, want)
		}
	}
}

// The work directory's safety is a property of .gitignore, not of the constant.
// Asserting only the literal would stay green if the ignore rule were dropped —
// exactly the change that would start committing fetched pages.
func TestTheWorkDirIsActuallyIgnored(t *testing.T) {
	out, err := exec.Command("git", "-C", "../..", "check-ignore", "-q", workDirDefault+"/probe.txt").CombinedOutput()
	if err == nil {
		return // exit 0: the path is ignored, which is what this pins
	}
	// check-ignore exits 1 for "not ignored" and anything else for "git could
	// not answer" — no binary, not a repository, a module-cache copy. Reporting
	// the second as a security regression would be a false alarm.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Skipf("git cannot answer here (%v); the ignore rule is unverifiable in this checkout\n%s", err, out)
	}
	t.Fatalf("git does not ignore %s: probe artifacts carry whatever the source carried and must never be committable", workDirDefault)
}
