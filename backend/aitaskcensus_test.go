// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The census as a fitness function: the contract says which AI tasks ship and
// what their invocation sites are called, and this build must register exactly
// those. A shipped task whose site nobody wrote, a site the contract never
// declared, and a planned task someone quietly implemented are all wiring
// defects that used to be invisible.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

func TestTaskCensusMatchesTheContract(t *testing.T) {
	registry, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Error(err)
	}
}

// A shipped site with no certification case can be measured by nothing: its
// record could only ever be a claim about a prompt somebody typed out, not
// about the request this build sends. Validate names the gap; this test is
// where the composition is held to it.
func TestTaskCensusBindsACaseToEverySite(t *testing.T) {
	registry, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	for _, site := range registry.All() {
		if _, bound := registry.CaseFor(site.Task, site.Variant); !bound {
			t.Errorf("site %s/%s ships with no certification case bound", site.Task, site.Variant)
		}
	}
}
