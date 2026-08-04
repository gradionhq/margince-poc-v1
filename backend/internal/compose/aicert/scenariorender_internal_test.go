// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// In-package because it compares the render mirrors against the types they
// mirror, and neither the mirrors nor the comparison is anyone else's business:
// exporting them to reach from _test would put test-only surface on the package.

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

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
			source:   reflect.TypeOf(Scenario{}),
			rendered: reflect.TypeOf(scenarioForRender{}),
		},
		{
			name:     "Expectations",
			source:   reflect.TypeOf(Expectations{}),
			rendered: reflect.TypeOf(expectForRender{}),
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
