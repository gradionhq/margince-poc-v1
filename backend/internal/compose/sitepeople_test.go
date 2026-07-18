// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The site-lead identity contract: the cross-read natural key is org +
// normalized name (+ published email), stable across page moves and
// reflow. The published-only people GATE rules live with the corpus gate
// (sitecorpusread_test.go).

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSiteLeadSourceIDIsOrgStableAcrossPagesAndNameReflow(t *testing.T) {
	org := ids.NewV7()
	// The key is the ORG + name, not the page: the same person found on
	// /team or /about, or after a re-crawl moved the page, is one lead.
	teamPage := siteLeadSourceID(org, "Anna Muster", "")
	aboutPage := siteLeadSourceID(org, "  anna   MUSTER ", "")
	if teamPage != aboutPage {
		t.Fatal("the lead natural key changed on a whitespace/case reflow, or across pages of the same site")
	}
	// A different org is a different lead even for the same name.
	if teamPage == siteLeadSourceID(ids.NewV7(), "Anna Muster", "") {
		t.Fatal("the same name at two organizations collapsed to one lead key")
	}
	// Two distinct people who share a name stay distinct via published email.
	if siteLeadSourceID(org, "Anna Muster", "anna1@acme.example") ==
		siteLeadSourceID(org, "Anna Muster", "anna2@acme.example") {
		t.Fatal("two people sharing a name but not an email share one key")
	}
	if teamPage == siteLeadSourceID(org, "Bernd Beispiel", "") {
		t.Fatal("two different people share one lead natural key")
	}
	if strings.Contains(teamPage, "@") || len(teamPage) != 64 {
		t.Fatalf("source id = %q, want a bare sha256 hex digest (no PII in the key)", teamPage)
	}
}
