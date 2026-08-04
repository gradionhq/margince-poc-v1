// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
)

func census(t *testing.T) *aitasks.Registry {
	t.Helper()
	r, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	return r
}

// The round trip IS the contract: whatever RenderScenario writes,
// LoadScenarioFile must read back. A scaffold nobody can run is not a starting
// point.
func TestRenderScenarioRoundTripsThroughLoadScenarioFile(t *testing.T) {
	reg := census(t)
	scenarios, err := aicert.LoadCorpus("corpus", reg)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(scenarios) == 0 {
		t.Fatal("an empty corpus would make this vacuous")
	}
	dir := t.TempDir()
	for _, sc := range scenarios {
		body, err := aicert.RenderScenario(sc)
		if err != nil {
			t.Errorf("RenderScenario(%s): %v", sc.Name, err)
			continue
		}
		// Scenario carries its fixture as JSONValue, a []byte — rendered
		// naively it becomes a list of byte VALUES rather than the mapping it
		// holds, which no loader could read back.
		if strings.Contains(string(body), "\n    - 1") {
			t.Errorf("%s rendered its fixture as a byte list:\n%s", sc.Name, body)
		}
		path := filepath.Join(dir, strings.ReplaceAll(sc.Task+"_"+sc.Site+"_"+sc.Name, "/", "_")+".yaml")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		back, err := aicert.LoadScenarioFile(path, reg)
		if err != nil {
			t.Errorf("%s does not round-trip: %v", sc.Name, err)
			continue
		}
		if back.Task != sc.Task || back.Site != sc.Site {
			t.Errorf("%s round-tripped to %s/%s", sc.Name, back.Task, back.Site)
		}
		if len(back.Fixture) == 0 {
			t.Errorf("%s lost its fixture in the round trip", sc.Name)
		}
	}
}

func TestLoadScenarioFileRefusesWhatCannotRun(t *testing.T) {
	reg := census(t)
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body, want string
	}{
		{"a site this build does not register", "task: rate_extract\nsite: nonsense\nfixture:\n  a: 1\n", "does not register"},
		{"no fixture, so the site is given nothing", "task: rate_extract\nsite: pricing\n", "no fixture"},
		{"not a scenario at all", "just: some\nother: yaml\n", "not a scenario"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")+".yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := aicert.LoadScenarioFile(path, reg)
			if err == nil {
				t.Fatalf("want a refusal mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
	if _, err := aicert.LoadScenarioFile(filepath.Join(dir, "absent.yaml"), reg); err == nil {
		t.Error("a missing file must be refused")
	}
	if _, err := aicert.LoadScenarioFile(filepath.Join(dir, "x.yaml"), nil); err == nil {
		t.Error("without a census nothing says which site runs the scenario")
	}
}

// Provenance gates what may ENTER the corpus; a scratch scenario an operator is
// probing with is not entering it, and demanding a stamp for a throwaway would
// only teach people to type a false one.
func TestLoadScenarioFileDoesNotDemandCorpusProvenance(t *testing.T) {
	reg := census(t)
	path := filepath.Join(t.TempDir(), "scratch.yaml")
	body := "task: rate_extract\nsite: pricing\nfixture:\n  provider: Aurora AI\n  page_text: |\n    Aurora Large, input $5.00 / 1M tokens.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := aicert.LoadScenarioFile(path, reg)
	if err != nil {
		t.Fatalf("a scratch scenario needs no source/sanitized_by: %v", err)
	}
	if sc.Task != "rate_extract" || sc.Site != "pricing" {
		t.Errorf("loaded %s/%s", sc.Task, sc.Site)
	}
}

// Decoding JSON into `any` routes every number through float64, which would
// render a 19-digit id as 1.234567890123457e+18 and silently change the fixture
// on its way to YAML. A round trip must return the scenario it was given.
func TestRenderScenarioPreservesExactNumbers(t *testing.T) {
	reg := census(t)
	sc := aicert.Scenario{
		Task: "rate_extract", Site: "pricing",
		Fixture: aicert.JSONValue(`{"provider":"Aurora AI","page_text":"x","big_id":1234567890123456789,"exact":0.1}`),
	}
	body, err := aicert.RenderScenario(sc)
	if err != nil {
		t.Fatalf("RenderScenario: %v", err)
	}
	if strings.Contains(string(body), "e+18") || strings.Contains(string(body), "1234567890123456800") {
		t.Errorf("a large id was rendered through float64:\n%s", body)
	}
	if !strings.Contains(string(body), "1234567890123456789") {
		t.Errorf("the id must survive exactly:\n%s", body)
	}

	path := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	back, err := aicert.LoadScenarioFile(path, reg)
	if err != nil {
		t.Fatalf("the rendered scenario must load back: %v", err)
	}
	// Both fields, not just the id: a future float conversion would change
	// exact while leaving the integer alone, and this would stay green.
	var loaded struct {
		BigID json.Number `json:"big_id"`
		Exact json.Number `json:"exact"`
	}
	if err := json.Unmarshal(back.Fixture, &loaded); err != nil {
		t.Fatalf("the loaded fixture must decode: %v", err)
	}
	if loaded.BigID.String() != "1234567890123456789" {
		t.Errorf("big_id round-tripped to %s", loaded.BigID)
	}
	if loaded.Exact.String() != "0.1" {
		t.Errorf("exact round-tripped to %s — 0.1 is not binary-exact and is the value a float conversion would move", loaded.Exact)
	}
}

// A value the corpus format cannot carry back unchanged is REFUSED, not
// silently approximated. yaml.v3 decodes an integer wider than int64 as a float
// and then rejects it as an !!int, so emitting one would produce a scaffold that
// does not load — and rounding it would produce one that loads as different data.
func TestRenderScenarioRefusesNumbersItCannotCarryBack(t *testing.T) {
	for _, tc := range []struct{ name, literal, want string }{
		{"an integer wider than int64", "123456789012345678901234567890", "wider than the corpus format"},
		{"a decimal finer than float64", "0.12345678901234567890123", "precision exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := aicert.Scenario{
				Task: "rate_extract", Site: "pricing",
				Fixture: aicert.JSONValue(`{"page_text":"x","n":` + tc.literal + `}`),
			}
			_, err := aicert.RenderScenario(sc)
			if err == nil {
				t.Fatalf("want a refusal mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "n") {
				t.Errorf("error = %q, want it to name the field and mention %q", err, tc.want)
			}
		})
	}
}

// Everything the format CAN carry stays exact, including the int64 boundary and
// ordinary decimals.
func TestRenderScenarioKeepsEveryNumberItAccepts(t *testing.T) {
	reg := census(t)
	for _, literal := range []string{"9223372036854775807", "-9223372036854775808", "1234567890123456789", "0", "1.5", "0.25", "-3.75"} {
		sc := aicert.Scenario{
			Task: "rate_extract", Site: "pricing",
			Fixture: aicert.JSONValue(`{"page_text":"x","n":` + literal + `}`),
		}
		body, err := aicert.RenderScenario(sc)
		if err != nil {
			t.Errorf("RenderScenario(%s): %v", literal, err)
			continue
		}
		if strings.Contains(string(body), `"`+literal+`"`) {
			t.Errorf("%s was emitted as a string:\n%s", literal, body)
		}
		path := filepath.Join(t.TempDir(), "s.yaml")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		back, err := aicert.LoadScenarioFile(path, reg)
		if err != nil {
			t.Errorf("%s does not load back: %v", literal, err)
			continue
		}
		if !strings.Contains(string(back.Fixture), literal) {
			t.Errorf("%s did not survive the round trip: %s", literal, back.Fixture)
		}
	}
}

// A decoder stops at the end of the first value, so trailing bytes would be
// dropped in silence — the fixture rendered would not be the fixture supplied.
func TestRenderScenarioRefusesTrailingDataAfterTheFixture(t *testing.T) {
	sc := aicert.Scenario{
		Task: "rate_extract", Site: "pricing",
		Fixture: aicert.JSONValue(`{"page_text":"x"} {"page_text":"y"}`),
	}
	_, err := aicert.RenderScenario(sc)
	if err == nil {
		t.Fatal("trailing data must be refused, not silently dropped")
	}
	if !strings.Contains(err.Error(), "trailing data") {
		t.Errorf("error = %q, want it to name the trailing data", err)
	}
}

// Equal numbers written differently are the same number. Comparing spellings
// would refuse a perfectly representable fixture over how it happens to be
// written — 1e-6 renders as 1e-06, and 1.50 as 1.5.
func TestRenderScenarioAcceptsEquivalentNumberSpellings(t *testing.T) {
	reg := census(t)
	for _, literal := range []string{"1e-6", "1E-6", "1e3", "1.50", "2.0", "-1e-7"} {
		sc := aicert.Scenario{
			Task: "rate_extract", Site: "pricing",
			Fixture: aicert.JSONValue(`{"page_text":"x","n":` + literal + `}`),
		}
		body, err := aicert.RenderScenario(sc)
		if err != nil {
			t.Errorf("RenderScenario(%s) refused a representable number: %v", literal, err)
			continue
		}
		path := filepath.Join(t.TempDir(), "s.yaml")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := aicert.LoadScenarioFile(path, reg); err != nil {
			t.Errorf("%s rendered a scenario that does not load: %v", literal, err)
		}
	}
}
