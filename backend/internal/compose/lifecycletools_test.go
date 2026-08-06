// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// companyEnricher is what makes "a tool and its route are two transports onto
// one behaviour" true for enrich: depth chooses between the same two engines
// the two REST routes call. What it must never do is pick the wrong one, or
// pick one silently when the role wired neither.

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestCompanyEnricherRoutesOnDepthAndNamesWhatIsMissing(t *testing.T) {
	// A role that wired no engines: each depth names the capability IT needs,
	// so an operator reads which flag was not declared rather than a generic
	// failure. This is the tool's half of the REST route's explicit 501.
	unwired := companyEnricher{}
	for depth, want := range map[string]string{
		agents.EnrichDepthSite: "crawl runner",
		agents.EnrichDepthPage: "model path",
	} {
		t.Run(depth, func(t *testing.T) {
			_, err := unwired.EnrichCompany(context.Background(), ids.NewV7(), "", depth)
			if err == nil {
				t.Fatalf("depth %q answered without an engine — a silent empty result where the "+
					"capability is absent", depth)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want the missing %s named", err, want)
			}
		})
	}

	// An unrecognised depth is REFUSED, not quietly served as the cheaper read:
	// the tool admits the vocabulary before this, so a value arriving here means
	// the two halves disagree, and answering a one-page scrape to a site read
	// would be a wrong answer rather than an error.
	_, err := unwired.EnrichCompany(context.Background(), ids.NewV7(), "", "everything")
	if err == nil {
		t.Fatal("an unknown depth was served")
	}
	if !strings.Contains(err.Error(), agents.EnrichDepthPage) || !strings.Contains(err.Error(), agents.EnrichDepthSite) {
		t.Errorf("err = %v, want the two depths this seam serves named", err)
	}
}
