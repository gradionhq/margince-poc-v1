// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/websearch"
)

// A search response is external input on its way to a stored field, so what
// it may write into a person's record is asserted rather than assumed.
func TestCanonicalProfileURLRefusesWhatMustNotBeStored(t *testing.T) {
	refused := map[string]string{
		"credentials in the URL would be stored verbatim": "https://placeholder-user:placeholder-pass@linkedin.com/in/anna",
		"plaintext is not an address worth recording":     "http://linkedin.com/in/anna",
		"an unparseable address has no host":              "://nonsense",
		"too long to be a profile address":                "https://linkedin.com/in/" + strings.Repeat("a", 400),
	}
	for why, raw := range refused {
		if _, ok := canonicalProfileURL(raw); ok {
			t.Errorf("canonicalProfileURL(%q) accepted it — %s", raw, why)
		}
	}
}

// Tracking parameters describe the search that found the page, not the
// person, so they are dropped rather than filed onto the record.
func TestCanonicalProfileURLKeepsOnlySchemeHostAndPath(t *testing.T) {
	got, ok := canonicalProfileURL("https://www.linkedin.com/in/anna-weber?trk=public_profile&sid=xyz#about")
	if !ok {
		t.Fatal("a normal profile address was refused")
	}
	if want := "https://www.linkedin.com/in/anna-weber"; got != want {
		t.Errorf("canonicalProfileURL = %q, want %q", got, want)
	}
}

// A company page and a job posting live on the same hosts as a profile and
// are not people. Filing one onto a contact would be a confident claim about
// something that is not a human at all.
func TestIsProfileURLAcceptsOnlyPersonalProfiles(t *testing.T) {
	if isProfileURL("https://www.linkedin.com/company/scalecommerce") {
		t.Error("a company page was accepted as a person's profile")
	}
	if isProfileURL("https://www.linkedin.com/jobs/view/123") {
		t.Error("a job posting was accepted as a person's profile")
	}
	if !isProfileURL("https://www.linkedin.com/in/anna-weber") {
		t.Error("a personal profile was refused")
	}
}

// The name guard is what stops a stranger's profile being filed onto a
// contact who merely shares an employer.
func TestMentionsNameRequiresEveryPartOfTheName(t *testing.T) {
	result := websearch.Result{
		Title:   "Markus Weber — Head of Sales at ScaleCommerce",
		Snippet: "Markus leads the sales team.",
		URL:     "https://www.linkedin.com/in/markus-weber",
	}
	if mentionsName(result, "Anna Weber") {
		t.Error("a different person at the same company was accepted as a match")
	}
	if !mentionsName(result, "Markus Weber") {
		t.Error("the named person was not recognised in their own result")
	}
}
