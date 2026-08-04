// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

// scenarioForRender and expectForRender restate the field sets of Scenario and
// Expectations by hand, and RenderScenario would silently DROP a field added to
// one side only — breaking the round-trip contract with nothing failing.
//
// Derived from the types rather than listing the fields, so it is the obligation
// that is asserted and not a copy of today's answer.
func TestRenderStructsCarryEveryFieldTheyMirror(t *testing.T) {
	for _, pair := range []struct {
		name       string
		source     reflect.Type
		rendered   reflect.Type
		exemptions map[string]string
	}{
		{
			name:     "Scenario",
			source:   reflect.TypeOf(aicert.Scenario{}),
			rendered: aicert.ScenarioRenderShape(),
		},
		{
			name:     "Expectations",
			source:   reflect.TypeOf(aicert.Expectations{}),
			rendered: aicert.ExpectRenderShape(),
		},
	} {
		t.Run(pair.name, func(t *testing.T) {
			missing := yamlTags(pair.source).Difference(yamlTags(pair.rendered))
			if len(missing) > 0 {
				t.Errorf("%s has yaml field(s) %v the render shape drops — RenderScenario would emit a scenario LoadScenarioFile cannot read back",
					pair.name, missing)
			}
			extra := yamlTags(pair.rendered).Difference(yamlTags(pair.source))
			if len(extra) > 0 {
				t.Errorf("the %s render shape emits yaml field(s) %v the corpus format does not have", pair.name, extra)
			}
		})
	}
}

type tagSet map[string]bool

func (s tagSet) Difference(other tagSet) []string {
	var out []string
	for tag := range s {
		if !other[tag] {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

func yamlTags(t reflect.Type) tagSet {
	out := tagSet{}
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}
