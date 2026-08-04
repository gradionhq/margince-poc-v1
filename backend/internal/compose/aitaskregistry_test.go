// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the census owes a reader of a certification record: that the scope on it
// is what the case bound to the site actually reaches, and not what the site's
// shape would allow a case to reach.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
)

// The sites whose case deliberately measures less than the production path, and
// the scope each one is therefore read at. Every other site is read at its
// kind — the case drives the whole path, retries and fallbacks included.
//
// The list is written out rather than derived for the same reason NewTaskCensus
// itself is: a list derived from the cases would compare the census to itself
// and agree with whatever this build declares, including a narrowing added to
// hide a case that quietly stopped driving its path, and including one dropped
// from a case that still needs it.
// gatekit:fixture the scope each narrowed site's reading is compared against —
// expected data, not an exception granted to the site.
var narrowedSites = map[string]string{
	// One call labels a batch; every below-floor message is re-asked solo.
	"capture_classify/classify": aitasks.ScopeSingleCall,
	// A below-floor verdict is re-asked once, unbound, and that answer applies.
	"capture_counterparty_verdict/verdict": aitasks.ScopeSingleCall,
	// An unreadable verdict is asked again, and the retry's score is the score.
	"cert_judge/judge": aitasks.ScopeSingleCall,
	// A deep read calls this once per crawled page and merges the answers.
	"site_fact_extract/page_facts": aitasks.ScopeSingleCall,
	// A URL cold start extracts the legal-notice page too, and merges.
	"cold_start/field_extract": aitasks.ScopeSingleCall,
}

func TestOnlyTheCasesThatMeasureLessNarrowWhatTheyCertify(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("building the census: %v", err)
	}
	scopes := registry.Scopes()

	shipped := map[string]bool{}
	for _, site := range registry.All() {
		key := string(site.Task) + "/" + site.Variant
		shipped[key] = true
		want, narrowed := narrowedSites[key]
		if !narrowed {
			want = site.CertifiedScope()
		}
		if scopes[key] != want {
			t.Errorf("site %s is certified at scope %q, want %q", key, scopes[key], want)
		}
	}
	// A narrowing named for a site nobody ships asserts nothing, and would leave
	// the site it was renamed from checked against its kind again in silence.
	for key := range narrowedSites {
		if !shipped[key] {
			t.Errorf("a narrowed scope is named for site %s, which this build does not ship", key)
		}
	}
}
