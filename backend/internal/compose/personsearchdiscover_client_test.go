// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/websearch"
)

var discoverNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// fakeSearch is the seam's boundary, mocked because it IS a boundary: a real
// provider would make this test cost money and depend on what a public index
// happens to hold today.
type fakeSearch struct {
	results []websearch.Result
	err     error
	// terms records what was asked, so a test can assert the query is anchored
	// rather than a bare name.
	terms string
	calls int
}

func (f *fakeSearch) Search(_ context.Context, q websearch.Query) ([]websearch.Result, error) {
	f.calls++
	f.terms = q.Terms
	return f.results, f.err
}

func (f *fakeSearch) Provider() string { return "fake" }

func result(title, url string) websearch.Result {
	return websearch.Result{Title: title, URL: url, Snippet: "…", RetrievedAt: discoverNow}
}

// A wrong profile URL on a contact is worse than none: it is a confident claim
// about a different human. The name guard is what prevents it.
func TestDiscoverProfileURLRefusesAStrangerAtTheSameCompany(t *testing.T) {
	g := &PersonAutoEnrich{search: &fakeSearch{results: []websearch.Result{
		result("Markus Weber — Head of Sales at ScaleCommerce", "https://www.linkedin.com/in/markus-weber"),
	}}}
	if _, found, err := g.discoverProfileURL(context.Background(), "Anna Weber", "ScaleCommerce"); err != nil || found {
		t.Errorf("found=%v err=%v — a different person at the same employer was accepted", found, err)
	}
}

// Punctuation inside a name is ordinary, and the guard cuts the name and the
// result text by the same rule so it stays ordinary. Splitting the name on
// whitespace alone kept "Jean-Luc" whole while the page naming him was cut at
// the hyphen, so every contact with a hyphen or an apostrophe was undiscoverable
// no matter how plainly the result named them.
func TestDiscoverProfileURLAcceptsANameWithInternalPunctuation(t *testing.T) {
	for name, res := range map[string]websearch.Result{
		"Jean-Luc Picard": result("Jean-Luc Picard — Head of Ops at ScaleCommerce",
			"https://www.linkedin.com/in/jeanlucpicard"),
		"Siobhan O'Connor": result("Siobhan O'Connor — Head of Ops at ScaleCommerce",
			"https://www.linkedin.com/in/siobhanoconnor"),
	} {
		g := &PersonAutoEnrich{search: &fakeSearch{results: []websearch.Result{res}}}
		_, found, err := g.discoverProfileURL(context.Background(), name, "ScaleCommerce")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !found {
			t.Errorf("%s: the result names this person and was refused", name)
		}
	}
}

// A company page and a job posting live on the same hosts as a profile and are
// not people. Filing one onto a contact would be a claim about something that
// is not a human at all.
func TestDiscoverProfileURLRefusesWhatIsNotAPerson(t *testing.T) {
	g := &PersonAutoEnrich{search: &fakeSearch{results: []websearch.Result{
		result("Anna Weber at ScaleCommerce", "https://www.linkedin.com/company/scalecommerce"),
		result("Anna Weber — jobs", "https://www.linkedin.com/jobs/view/123"),
	}}}
	if _, found, _ := g.discoverProfileURL(context.Background(), "Anna Weber", "ScaleCommerce"); found {
		t.Error("a company page or a job posting was filed as a person's profile")
	}
}

// The happy path: the URL is the fact worth keeping and the result text is the
// receipt — both available WITHOUT fetching the page, which is the whole point
// of the seam.
func TestDiscoverProfileURLKeepsTheAddressAndItsReceipt(t *testing.T) {
	fake := &fakeSearch{results: []websearch.Result{
		result("Anna Weber — Head of Procurement at ScaleCommerce",
			"https://www.linkedin.com/in/anna-weber?trk=public_profile"),
	}}
	g := &PersonAutoEnrich{search: fake}

	field, found, err := g.discoverProfileURL(context.Background(), "Anna Weber", "ScaleCommerce")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if field.Field != "linkedin" {
		t.Errorf("field = %q, want linkedin", field.Field)
	}
	if strings.Contains(field.Value, "trk=") {
		t.Errorf("the tracking parameter was stored: %q", field.Value)
	}
	if field.EvidenceSnippet == "" {
		t.Error("no receipt was kept; the reader has nothing to check the address against")
	}
	if !strings.HasPrefix(field.SourceRef, "web_search:fake:") {
		t.Errorf("source ref = %q, want the channel, the index and the read date", field.SourceRef)
	}
	// The query is anchored on the employer. A bare name is precisely the case
	// that returns somebody else.
	if !strings.Contains(fake.terms, "ScaleCommerce") {
		t.Errorf("the query was not anchored on the employer: %q", fake.terms)
	}
}

// Without an employer there is no query worth running, and running one would
// be a paid request for an answer known in advance to be unreliable.
func TestDiscoverProfileURLDoesNotSearchOnABareName(t *testing.T) {
	fake := &fakeSearch{}
	g := &PersonAutoEnrich{search: fake}

	if _, found, err := g.discoverProfileURL(context.Background(), "Anna Weber", ""); err != nil || found {
		t.Errorf("found=%v err=%v", found, err)
	}
	if fake.calls != 0 {
		t.Errorf("the provider was called %d time(s) for an unanchored query", fake.calls)
	}
}

// A deployment that bound no provider skips discovery silently. That is the
// sovereign posture, not an error on every contact creation.
func TestDiscoverProfileURLIsSilentWithNoProviderBound(t *testing.T) {
	g := &PersonAutoEnrich{}
	if _, found, err := g.discoverProfileURL(context.Background(), "Anna Weber", "ScaleCommerce"); err != nil || found {
		t.Errorf("found=%v err=%v, want a silent skip", found, err)
	}
}

// A search that failed is not a person that failed: the contact is already
// saved, and the discovery is an improvement that did not land.
func TestDiscoverProfileURLReportsAProviderFailure(t *testing.T) {
	g := &PersonAutoEnrich{search: &fakeSearch{err: errors.New("provider unavailable")}}
	if _, found, err := g.discoverProfileURL(context.Background(), "Anna Weber", "ScaleCommerce"); err == nil || found {
		t.Errorf("found=%v err=%v, want the failure surfaced to the caller", found, err)
	}
}
