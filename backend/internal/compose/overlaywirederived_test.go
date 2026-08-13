// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The wire's DERIVED slots: the ones the mirror carries no key for, computed
// from the mirrored collections the same body already publishes. A derived
// slot has two ways to be wrong that a mapped one does not — it can compute
// from the wrong row, and it can invent a value where the native path
// publishes none — so each is held here to the answer the native read path
// gives for the same facts.
//
// The payloads are canonical rather than seeded through the HubSpot mapping,
// which lands exactly one domain row: what is under test is which of SEVERAL
// rows the derivation picks, a shape no incumbent fixture can pose. That the
// derivation survives the real ingest pipeline end to end is the wire
// reachability gate's assertion, on the same binding.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// website_url is DERIVED from the primary domain row and never stored
// (ADR-0085), so a mirrored company owes the value a native one publishes —
// including which row counts: the one flagged primary, wherever it sits in the
// collection.
func TestOverlayWireOrganizationDerivesWebsiteURLFromThePrimaryDomain(t *testing.T) {
	rec := wireRecord(t, datasource.EntityOrganization, map[string]any{
		"display_name": "Acme",
		"organization_domain": []map[string]any{
			{"domain": "acme.de", "position": 0},
			{"domain": "acme.io", "is_primary": true, "position": 1},
		},
	})
	org, err := overlayWireOrganization(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireOrganization: %v", err)
	}
	if org.WebsiteUrl == nil {
		t.Fatal("WebsiteUrl = nil, want the primary domain rendered as a URL")
	}
	if *org.WebsiteUrl != "https://acme.io" {
		t.Errorf("WebsiteUrl = %q, want https://acme.io — the primary row, not the leading one", *org.WebsiteUrl)
	}
}

// Which domain is the company's is the mapping's assertion; a reader that fell
// back to the first row would publish a host no mapping ever nominated, and
// the native path publishes nothing at all on those same rows.
func TestOverlayWireOrganizationOmitsWebsiteURLWithoutAPrimaryDomain(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"no row claims the flag": {
			"display_name":        "Acme",
			"organization_domain": []map[string]any{{"domain": "acme.de", "position": 0}},
		},
		"no domain rows at all": {"display_name": "Acme"},
	} {
		t.Run(name, func(t *testing.T) {
			org, err := overlayWireOrganization(wireCtx(), wireRecord(t, datasource.EntityOrganization, fields))
			if err != nil {
				t.Fatalf("overlayWireOrganization: %v", err)
			}
			if org.WebsiteUrl != nil {
				t.Errorf("WebsiteUrl = %q, want absent — no mirrored row claims to be primary, so there is no "+
					"domain to render and the native path publishes none either", *org.WebsiteUrl)
			}
		})
	}
}
