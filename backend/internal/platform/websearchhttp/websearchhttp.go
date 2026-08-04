// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package websearchhttp binds a search provider to the websearch seam
// (ADR-0081 / A126).
//
// It sits beside platform/webread deliberately: that package owns FETCHING a
// page under robots and caps, this one owns FINDING the page, and the two
// halves of the same governed capability belong at the same layer.
//
// Provider selection is configuration. The operator supplies a key and gets
// search; supply none and the deployment has none, which is the sovereign
// zero-egress posture rather than a degraded one. Nothing here decides what
// may be fetched — that is websearch.MayFetch, in the port, so every consumer
// inherits one answer.
package websearchhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/websearch"
)

// defaultMaxResults is what an unbounded query takes. Small on purpose: the
// questions this product asks are answered by the first few hits, and every
// extra result is a paid unit that nobody reads.
const defaultMaxResults = 5

// requestTimeout bounds one provider call. A search that hangs must not hold
// a background pass open behind it.
const requestTimeout = 10 * time.Second

// Disabled is the client a deployment gets when it binds no provider. It
// answers ErrNoProvider so callers degrade to captured data and SAY so,
// rather than presenting an empty result set as "nothing exists".
type Disabled struct{}

// Search refuses, naming the absence. Callers degrade to captured data and SAY
// so, rather than presenting an empty result set as "nothing exists".
func (Disabled) Search(context.Context, websearch.Query) ([]websearch.Result, error) {
	return nil, websearch.ErrNoProvider
}

// Provider names the absence honestly, so a run-transparency record shows that
// no index was asked rather than that one answered nothing.
func (Disabled) Provider() string { return "none" }

// Brave calls the Brave Search API — an independent index under terms that
// permit commercial reuse, which is what ADR-0081 §2 requires of any bound
// provider. A vendor that resells another engine's results page, or whose
// corpus is bulk-collected auth-walled profiles, does not qualify however
// convenient its API.
type Brave struct {
	key    string
	client *http.Client
	now    func() time.Time
}

// NewBrave binds the adapter to a key. now is injected so a test can pin the
// read date a stored claim will age against.
func NewBrave(key string, now func() time.Time) *Brave {
	return &Brave{
		key:    key,
		client: &http.Client{Timeout: requestTimeout},
		now:    now,
	}
}

// Provider names the index that answered. A stored claim carries it in its
// source ref, so a reader knows whose corpus the citation came from.
func (b *Brave) Provider() string { return "brave" }

// Search runs one query and returns what the index asserts.
//
// It does NOT fetch any result. That separation is the seam's whole posture:
// the title and snippet returned here are usable evidence without touching
// the target site, and whether a URL may additionally be read is a later,
// separate decision made by websearch.MayFetch.
func (b *Brave) Search(ctx context.Context, q websearch.Query) ([]websearch.Result, error) {
	terms := strings.TrimSpace(q.Terms)
	if terms == "" {
		return nil, fmt.Errorf("websearch: a search needs terms")
	}
	// Every error path below names the provider and the failure and NOTHING
	// about the query: these searches are for named people, so the query
	// string is personal data and an error message is an observability
	// surface that outlives the request.
	if q.Site != "" {
		// Narrowing to one domain is the cheapest and least intrusive form of
		// the questions this product asks, so it is expressed in the query
		// rather than by filtering results after paying for them.
		terms = "site:" + q.Site + " " + terms
	}
	limit := q.MaxResults
	if limit <= 0 {
		limit = defaultMaxResults
	}

	endpoint := "https://api.search.brave.com/res/v1/web/search?" + url.Values{
		"q":     {terms},
		"count": {strconv.Itoa(limit)},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("websearch: building the request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.key)

	resp, err := b.client.Do(req)
	if err != nil {
		// The transport error is NOT wrapped. net/http returns a *url.Error
		// carrying the request URL, and this request's URL carries the query
		// — which, for the searches this product runs, is a named person and
		// their employer. Wrapping it would put that in every log line a
		// failed search produces. The failure is reported as what it is.
		return nil, fmt.Errorf("websearch: the %s request did not complete", b.Provider())
	}
	//craft:ignore swallowed-errors closing a response body after the read has no actionable failure — the read's own error is already returned, and a close error would only mask it
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The status alone. A provider error page echoes the query back, and
		// the query names a person.
		return nil, fmt.Errorf("websearch: %s answered %d", b.Provider(), resp.StatusCode)
	}

	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		// Same reasoning as the transport error above: a decode failure can
		// echo the payload, and the payload is about a person.
		return nil, fmt.Errorf("websearch: the %s answer could not be read", b.Provider())
	}

	readAt := b.now().UTC()
	out := make([]websearch.Result, 0, len(payload.Web.Results))
	for _, r := range payload.Web.Results {
		res := websearch.Result{
			Title:   strings.TrimSpace(r.Title),
			Snippet: strings.TrimSpace(r.Description),
			URL:     strings.TrimSpace(r.URL),
			// Ours, not the provider's: this is what makes a stored claim age
			// visibly instead of pretending to be current.
			RetrievedAt: readAt,
		}
		if when, err := time.Parse(time.RFC3339, r.PageAge); err == nil {
			res.PublishedAt = &when
		}
		out = append(out, res)
	}
	return out, nil
}

// FromEnv binds whichever provider the deployment configured, or Disabled
// when it configured none.
//
// The seam IS the return type, which is why this returns an interface: a
// caller must not be able to tell a bound provider from Disabled, or it would
// branch on which one it got instead of on the ErrNoProvider the port defines.
//
// Absence is a valid, supported answer here — the same posture ADR-0020 gives
// model keys. A deployment that wants no external egress simply sets nothing,
// and every consumer degrades honestly instead of erroring at request time.
// tell a bound provider from Disabled, or it would branch on which one it got
// instead of on the ErrNoProvider the port defines.
//
//nolint:ireturn // the seam IS the return type: a caller must not be able to
//nolint:ireturn // the seam is the return type — see the doc comment above
func FromEnv(now func() time.Time) (websearch.Client, bool) {
	if key := strings.TrimSpace(os.Getenv("BRAVE_SEARCH_API_KEY")); key != "" {
		return NewBrave(key, now), true
	}
	return Disabled{}, false
}
