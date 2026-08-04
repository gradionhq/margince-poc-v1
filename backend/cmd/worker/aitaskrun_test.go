// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestRecordingCompleterKeepsWhatTheSeamDiscards(t *testing.T) {
	want := model.Response{Text: "reply", InputTokens: 11, OutputTokens: 22}
	rec := &recordingCompleter{
		inner: func(context.Context, model.Request) (model.Response, ai.RouteInfo, error) {
			return want, ai.RouteInfo{ModelID: "m", Degraded: true}, nil
		},
	}
	got, err := rec.Complete(context.Background(), model.Request{System: "sys", MaxTokens: 7})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Text != want.Text || got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens {
		t.Errorf("Complete returned %+v, want the inner reply %+v unaltered", got, want)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(rec.calls))
	}
	call := rec.calls[0]
	// aitasks.Trace carries neither usage nor the route, which is the whole
	// reason this wrapper exists.
	if call.Response.OutputTokens != 22 || call.Request.MaxTokens != 7 || !call.Route.Degraded {
		t.Errorf("recorded %+v, want the request, the usage and the route kept", call)
	}
}

func TestRecordingCompleterKeepsAFailedCall(t *testing.T) {
	rec := &recordingCompleter{
		inner: func(context.Context, model.Request) (model.Response, ai.RouteInfo, error) {
			return model.Response{}, ai.RouteInfo{}, errors.New("the provider refused")
		},
	}
	if _, err := rec.Complete(context.Background(), model.Request{}); err == nil {
		t.Fatal("Complete swallowed the inner failure")
	}
	if len(rec.calls) != 1 || rec.calls[0].Err == "" {
		// A call that never completed still has to appear: a failure on the
		// third of four calls is diagnosed from the two that worked.
		t.Errorf("a failed call must still be recorded, got %+v", rec.calls)
	}
}

func TestResolveSiteNamesTheNearMatches(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if _, err := resolveSite(census, "rate_extract/nonsense"); err == nil {
		t.Fatal("an unregistered variant must be refused")
	} else if !strings.Contains(err.Error(), "pricing") {
		t.Errorf("error %q should name the variants rate_extract does ship", err)
	}
	if _, err := resolveSite(census, "rate_extract"); err == nil {
		t.Fatal("a site reference without a variant must be refused")
	}
	if _, err := resolveSite(census, "no_such_task/x"); err == nil {
		t.Fatal("an unknown task must be refused")
	}
}

func TestReadJSONFileRefusesWhatIsNotJSON(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSONFile(bad, "fixture"); err == nil {
		t.Fatal("invalid JSON must be refused before a paid call, not after")
	}
	if _, err := readJSONFile(filepath.Join(dir, "absent.json"), "fixture"); err == nil {
		t.Fatal("a missing file must be refused")
	}
}

func TestProbeCompleterWantsExactlyOneBinding(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  aiTaskFlags
	}{
		{"none at all", aiTaskFlags{}},
		{"a routing file and the fake, which disagree about what answers", aiTaskFlags{routingPath: "r.yaml", fakeBrain: true}},
		{"a routing file and a pinned model", aiTaskFlags{routingPath: "r.yaml", modelSpec: "p:m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := probeCompleter(tc.cfg, ai.TaskRateExtract); err == nil {
				t.Fatal("want a refusal naming the three flags")
			}
		})
	}
	if _, banner, err := probeCompleter(aiTaskFlags{fakeBrain: true}, ai.TaskRateExtract); err != nil {
		t.Fatalf("the fake alone is a complete binding: %v", err)
	} else if !strings.Contains(banner, "fake") {
		t.Errorf("banner %q must name the fake, so a run is never mistaken for a paid one", banner)
	}
}

func TestProbeCompleterRefusesAMalformedModelOverride(t *testing.T) {
	if _, _, err := probeCompleter(aiTaskFlags{modelSpec: "justamodel"}, ai.TaskRateExtract); err == nil {
		t.Fatal("--model wants provider:model and must say so")
	}
}

// The end-to-end proof that the probe drives the production seam: a real site,
// a real fixture, the real Prepare/Run/Evaluate path — over the offline fake, so
// the lane spends nothing and needs no network.
func TestProbeRunsASiteEndToEndOverTheFake(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.json")
	body, err := json.Marshal(map[string]string{
		"provider":  "Aurora AI",
		"page_text": "Aurora AI — Aurora Large (model id: aurora-large). Input $5.00 / 1M tokens, output $25.00 / 1M tokens.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, body, 0o600); err != nil {
		t.Fatal(err)
	}
	expect := filepath.Join(dir, "expect.json")
	if err := os.WriteFile(expect, []byte(`{"aurora-large":{"input_per_mtok":"5","output_per_mtok":"25","cache_read_per_mtok":"0","cache_write_per_mtok":"0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonOut := filepath.Join(dir, "result.json")

	var out strings.Builder
	err = runAITaskProbe(context.Background(), []string{
		"run", "--site", "rate_extract/pricing",
		"--fixture", fixture, "--expect", expect,
		"--ai-fake", "--corpus", testCorpusDir(),
		"--json", jsonOut, "--dump-request", dir,
	}, &out)
	// The fake answers nothing usable, so the VALIDATOR rejects the reply — a
	// measurement, not a harness failure, and therefore not an error.
	if err != nil {
		t.Fatalf("the probe itself must not fail on an unusable reply: %v", err)
	}
	report := out.String()
	if !strings.Contains(report, "rate_extract/pricing") || !strings.Contains(report, "call 1") {
		t.Errorf("the report must name the site and the call it made:\n%s", report)
	}

	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("--json wrote nothing: %v", err)
	}
	var res probeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("--json must be machine-readable: %v", err)
	}
	if res.Site != "rate_extract/pricing" || len(res.Calls) != 1 {
		t.Errorf("json result = %+v, want one call against rate_extract/pricing", res)
	}
	if res.Outcome == nil {
		t.Error("the production validator's outcome must reach the json result")
	}

	dumped, err := filepath.Glob(filepath.Join(dir, "*.request.json"))
	if err != nil || len(dumped) != 1 {
		t.Fatalf("--dump-request wrote %v (err %v), want one request", dumped, err)
	}
	dumpedRaw, err := os.ReadFile(dumped[0])
	if err != nil {
		t.Fatalf("reading the dumped request: %v", err)
	}
	var req probeRequest
	if err := json.Unmarshal(dumpedRaw, &req); err != nil {
		t.Fatalf("a dumped request must be readable JSON — it is what a prompt edit is diffed against: %v", err)
	}
	if req.System == "" || len(req.Messages) == 0 {
		t.Error("the dumped request must carry the system prompt and payload the site built")
	}
}

func TestProbeCompleterReportsAnUnreadableRoutingFile(t *testing.T) {
	cfg := aiTaskFlags{routingPath: filepath.Join(t.TempDir(), "absent.yaml")}
	if _, _, err := probeCompleter(cfg, ai.TaskRateExtract); err == nil {
		t.Fatal("an unreadable routing file must be refused, not silently ignored")
	}
}

func TestProbeJSONToStdoutWritesNoFile(t *testing.T) {
	var out strings.Builder
	res := probeResult{Site: "rate_extract/pricing"}
	if err := writeProbeJSON(&out, "-", res); err != nil {
		t.Fatalf("writeProbeJSON: %v", err)
	}
	var back probeResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &back); err != nil {
		t.Fatalf("--json - must emit decodable JSON: %v", err)
	}
	if back.Site != res.Site {
		t.Errorf("round-tripped %q, want %q", back.Site, res.Site)
	}
}

func TestEmitArtifactReportsAnUnwritableTarget(t *testing.T) {
	// A path whose parent is an existing FILE cannot be created, and the
	// operator has to hear that rather than lose the artifact silently.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := emitArtifact(&out, filepath.Join(blocker, "child.txt"), []byte("body"), "wrote"); err == nil {
		t.Fatal("an unwritable target must be reported")
	}
}

// The verbs are dispatched from one entry point, so the dispatch itself is
// exercised rather than only the functions behind it.
func TestProbeEntryPointDispatchesTheReadOnlyVerbs(t *testing.T) {
	var listed strings.Builder
	if err := runAITaskProbe(context.Background(), []string{"list", "--corpus", testCorpusDir()}, &listed); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listed.String(), "rate_extract/pricing") {
		t.Errorf("list did not reach listSites:\n%s", listed.String())
	}

	var scaffolded strings.Builder
	err := runAITaskProbe(context.Background(), []string{
		"scaffold", "rate_extract/pricing", "--corpus", testCorpusDir(), "--out", "-",
	}, &scaffolded)
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if !strings.Contains(scaffolded.String(), "site: pricing") {
		t.Errorf("scaffold did not reach scaffoldSite:\n%s", scaffolded.String())
	}
}

func TestProbeRefusesAScenarioThatCarriesNoFixture(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(path, []byte("name: x\ntask: rate_extract\nsite: pricing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadScenarioInput(census, path); err == nil {
		t.Fatal("a scenario with no fixture gives the site nothing, and must be refused")
	}
}

// Every registered site must be reachable by the probe. Deriving the list from
// the census rather than keeping one here is what makes a site added to the
// composition unable to become silently unprobeable.
func TestEveryRegisteredSiteIsListedAndScaffoldable(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	sites := census.All()
	if len(sites) == 0 {
		t.Fatal("an empty census would make every assertion below vacuous")
	}

	var listed strings.Builder
	if err := listSites(&listed, census, testCorpusDir()); err != nil {
		t.Fatalf("listSites: %v", err)
	}
	dir := t.TempDir()
	for _, site := range sites {
		ref := siteKey(site)
		if !strings.Contains(listed.String(), ref) {
			t.Errorf("%s is not listed — a site nobody can find is a site nobody can probe", ref)
		}
		var out strings.Builder
		target := filepath.Join(dir, strings.ReplaceAll(ref, "/", "_")+".yaml")
		if err := scaffoldSite(&out, census, testCorpusDir(), ref, target); err != nil {
			t.Errorf("%s cannot be scaffolded: %v", ref, err)
			continue
		}
		// A scaffold nobody can run back is not a starting point.
		if _, err := loadScenarioInput(census, target); err != nil {
			t.Errorf("%s scaffolds a scenario the probe then refuses: %v", ref, err)
		}
	}
}

// Nothing has stripped the request by the time the probe records it: the
// credential pass runs inside each provider adapter as it marshals its own wire
// payload, and never mutates model.Request. So a dump built straight from the
// request would carry secrets verbatim, whatever a comment claimed. This test
// fails if the probe stops stripping what it writes down.
func TestProbeStripsCredentialsOutOfEverythingItWritesDown(t *testing.T) {
	const secret = "sk-liveKEYmaterialThatMustNeverBeWritten1234"

	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.json")
	body, err := json.Marshal(map[string]string{
		"provider":  "Aurora AI",
		"page_text": "Aurora Large (model id: aurora-large). Input $5.00 / 1M tokens, output $25.00 / 1M tokens. Contact support with " + secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, body, 0o600); err != nil {
		t.Fatal(err)
	}
	expect := filepath.Join(dir, "expect.json")
	if err := os.WriteFile(expect, []byte(`{"aurora-large":{"input_per_mtok":"5","output_per_mtok":"25","cache_read_per_mtok":"0","cache_write_per_mtok":"0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonOut := filepath.Join(dir, "result.json")

	var out strings.Builder
	if err := runAITaskProbe(context.Background(), []string{
		"run", "--site", "rate_extract/pricing",
		"--fixture", fixture, "--expect", expect,
		"--ai-fake", "--corpus", testCorpusDir(),
		"--json", jsonOut, "--dump-request", dir,
	}, &out); err != nil {
		t.Fatalf("probe: %v", err)
	}

	// The fixture on disk is the operator's own and untouched; what the probe
	// WRITES is what must be clean.
	dumped, err := filepath.Glob(filepath.Join(dir, "*.request.json"))
	if err != nil || len(dumped) != 1 {
		t.Fatalf("--dump-request wrote %v (err %v)", dumped, err)
	}
	for _, path := range []string{dumped[0], jsonOut} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s carries the credential verbatim", filepath.Base(path))
		}
		// The surrounding page must still be there — a redaction that ate the
		// payload would pass the check above while making the dump useless.
		if !strings.Contains(string(raw), "aurora-large") {
			t.Errorf("%s lost the page it was supposed to record", filepath.Base(path))
		}
	}
}

// Prepare is handed the RAW fixture and Evaluate the raw reply, and both quote
// what they rejected. Those messages reach stdout and the --json artifact, so
// they pass the same credential gate the request and the reply do.
func TestProbeStripsCredentialsOutOfAFailureItReports(t *testing.T) {
	const secret = "sk-liveKEYmaterialThatMustNeverBeWritten1234"

	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.json")
	// A fixture the site REFUSES: page_text is the shape it takes, so a number
	// here makes Prepare quote the value it could not read.
	body := []byte(`{"provider":"Aurora AI","page_text":"` + secret + `","tracked_currencies":5}`)
	if err := os.WriteFile(fixture, body, 0o600); err != nil {
		t.Fatal(err)
	}
	jsonOut := filepath.Join(dir, "result.json")

	var out strings.Builder
	// The probe reports a harness failure, so a non-nil error here is expected.
	_ = runAITaskProbe(context.Background(), []string{
		"run", "--site", "rate_extract/pricing", "--fixture", fixture,
		"--ai-fake", "--corpus", testCorpusDir(), "--json", jsonOut,
	}, &out)

	if strings.Contains(out.String(), secret) {
		t.Errorf("the report printed the credential:\n%s", out.String())
	}
	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("--json wrote nothing even on a failure: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("the --json artifact carries the credential a refusal quoted")
	}
	var res probeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("--json must stay decodable on a failure: %v", err)
	}
	if res.Failure == "" {
		t.Error("a refusal must still be reported — redaction is not silence")
	}
}
