// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package surfe is the live Surfe adapter: the one place in this build that
// speaks a vendor's wire protocol (ADR-0101, ARCH-SEAM-17).
//
// It translates a frozen request into Surfe's v2 asynchronous bulk-enrichment
// API and its answer back into the bounded shape the platform stores. It
// decides no policy, widens nothing that was requested, and never sees the
// database — everything it knows about a subject arrives in the Request the
// caller built.
//
// One person per request, though the endpoint accepts up to ten thousand.
// The platform's unit of spend, of consent and of erasure is the RUN, and a
// run is one subject: batching would tie several subjects' fates together, so
// one refusal or one ambiguous answer would put every person in the batch
// into the same unknown state.
package surfe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

const (
	// baseURL is the ONLY host this adapter may reach, and the descriptor
	// below declares the same one. The check before each request compares
	// against the descriptor, so the two cannot drift apart silently.
	baseURL = "https://api.surfe.com"
	// egressHost is what an outbound request's URL must resolve to.
	egressHost = "api.surfe.com"

	// requestTimeout bounds ONE call. Generous for a vendor round trip,
	// far below the job's own two-minute ceiling, and finite because a hung
	// vendor must not pin a worker: an expired call answers "ambiguous",
	// which holds the reservation rather than retrying a possible charge.
	requestTimeout = 10 * time.Second
	// maxResponseBytes caps how much of a response is read. The bodies are
	// small and bounded by construction (one person), so a stream that keeps
	// going is a fault, not a large answer.
	maxResponseBytes = 1 << 20
)

// The vocabulary this adapter speaks on both sides of the seam: the credit
// pools Surfe meters, and the categories the platform sells against them.
// Spelled once because the descriptor, the include-flag mapping and the spend
// arithmetic must all name the same things — a typo in any one of them would
// price or request a category that silently does not exist.
const (
	poolEmail  provider.Pool = "email"
	poolMobile provider.Pool = "mobile"

	categoryProfessionalEmail provider.Category = "professional_email"
	categoryPersonalEmail     provider.Category = "personal_email"
	categoryMobile            provider.Category = "mobile"
	categoryLinkedInProfile   provider.Category = "linkedin_profile"
	categoryCurrentEmployment provider.Category = "current_employment"
	categoryJobHistory        provider.Category = "job_history"
)

// The vendor's own email-type vocabulary, which is not ours: "personal" is
// what Surfe calls the fallback, and it appears both in what we ASK for and
// in what it returns.
const (
	emailTypePersonal     = "personal"
	emailTypeProfessional = "professional"
)

// HTTPDoer is the transport seam, so a test can answer with a recorded body
// instead of reaching the network — the same shape webhooks.HTTPDoer takes.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Adapter speaks Surfe. It holds no credential: the key arrives per call,
// resolved from the vault at the execution boundary and never stored on
// anything that outlives the request.
type Adapter struct {
	client HTTPDoer
	now    func() time.Time
}

// New builds the adapter. Surfe is a known vendor host reached over TLS, so
// this is a timeout-bounded client rather than the netguard-wrapped one an
// arbitrary customer-supplied URL needs — but it REFUSES REDIRECTS, which is
// not optional here and is what every other outbound caller in this tree does
// (identity's OAuth metadata fetch, the agent app fetch, webread).
//
// Go copies the Authorization header across a redirect to any subdomain of
// the origin, so without this a 302 from api.surfe.com to anything.surfe.com
// replays the customer's API key to whoever answered — a compromised vendor
// edge, a hijacked subdomain, a DNS takeover. The egress-host check below
// runs once, on the request this code builds; the standard library never
// consults it again for a redirect hop, so the check alone does not hold.
//
// A redirect is also simply a fault: Surfe's documented API does not issue
// one, and the descriptor promises the customer this adapter talks to exactly
// one host.
func New(now func() time.Time) *Adapter {
	return &Adapter{
		client: &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("surfe: refusing a redirect to %q: the credential must not leave the declared egress host", req.URL.Host)
			},
		},
		now: now,
	}
}

// WithHTTPDoer swaps the transport, for tests that answer from a recorded
// body rather than the network.
func (a *Adapter) WithHTTPDoer(doer HTTPDoer) *Adapter {
	a.client = doer
	return a
}

var _ provider.Adapter = (*Adapter)(nil)

// Descriptor is what the platform knows about Surfe without calling it.
//
// The cost table and the cascade come from the live calibration the offline
// fake already encodes: a professional email costs one email credit, a mobile
// costs one mobile credit, and the personal-email fallback costs two email
// credits and cannot carry a mobile. The categories that cost nothing are
// still declared, because a run must be able to REQUEST them.
func (a *Adapter) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		Name:        "surfe",
		Transport:   provider.TransportPolled,
		Billing:     provider.BillingPerSuccessfulResult,
		CreditPools: []provider.Pool{poolEmail, poolMobile},
		CostTable: map[provider.Category]map[provider.Pool]int{
			categoryProfessionalEmail: {poolEmail: 1},
			categoryPersonalEmail:     {},
			categoryMobile:            {poolMobile: 1},
			categoryLinkedInProfile:   {},
			categoryCurrentEmployment: {},
			categoryJobHistory:        {},
		},
		Cascades: []provider.Cascade{{
			Category: categoryPersonalEmail,
			After:    categoryProfessionalEmail,
			Cost:     map[provider.Pool]int{poolEmail: 2},
			Excludes: []provider.Category{categoryMobile},
		}},
		Identifiers:  []string{"LinkedIn profile URL", "first and last name with company name or domain"},
		EgressHost:   egressHost,
		Verification: "credit-balance read",
		TermsLinks: []provider.Link{
			{Label: "Terms", URL: "https://surfe.com/terms"},
			{Label: "Privacy", URL: "https://surfe.com/privacy"},
		},
		Issuance: provider.IssuanceSelfService,
		Categories: []provider.Category{
			categoryProfessionalEmail, categoryMobile, categoryLinkedInProfile,
			categoryCurrentEmployment, categoryJobHistory, categoryPersonalEmail,
		},
		Presets: map[string][]provider.Category{
			"full": {
				categoryProfessionalEmail, categoryMobile, categoryLinkedInProfile,
				categoryCurrentEmployment, categoryJobHistory, categoryPersonalEmail,
			},
			"professional_only": {
				categoryProfessionalEmail, categoryLinkedInProfile,
				categoryCurrentEmployment, categoryJobHistory,
			},
		},
		DefaultPreset: "full",
	}
}

// call issues one request and decodes its body. The egress host is checked
// HERE, immediately before the request leaves, because nothing else in the
// platform enforces the descriptor's declared host — the check belongs where
// the URL is final.
// The body and the decode target are the only two things a caller varies, and
// both are JSON shapes this package declares privately above — a constrained
// type would name a union of six structs that exists nowhere else, and the
// encoding library takes `any` regardless.
//
//craft:ignore naked-any the encoding/json boundary takes any; both arguments are this package's own wire structs.
func (a *Adapter) call(ctx context.Context, cred provider.Credential, method, path string, body, out any) (int, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("surfe: encoding the request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, payload)
	if err != nil {
		return 0, fmt.Errorf("surfe: building the request: %w", err)
	}
	if req.URL.Host != egressHost {
		// Unreachable while baseURL is a constant, and deliberately checked
		// anyway: this adapter's whole disclosure to the customer is "it
		// talks to api.surfe.com", and that promise should be enforced at
		// the point of egress rather than by reading the source.
		return 0, fmt.Errorf("surfe: refusing to call %q, which is not the declared egress host", req.URL.Host)
	}
	req.Header.Set("Authorization", "Bearer "+string(cred))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("surfe: %s %s: %w", method, path, err)
	}
	// Read, then close, both checked. Not a deferred close: a deferred one
	// either discards its error or needs a named return to report it, and the
	// body here is small and bounded, so reading it inline is simpler than
	// either. Every return below this point has already closed.
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if closeErr := resp.Body.Close(); closeErr != nil && readErr == nil {
		// Reported only where the read itself succeeded: a teardown fault
		// must not overwrite the answer the caller actually needs.
		return resp.StatusCode, fmt.Errorf("surfe: closing the response: %w", closeErr)
	}
	if readErr != nil {
		return resp.StatusCode, fmt.Errorf("surfe: reading the response: %w", readErr)
	}
	if len(raw) == 0 {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// The vendor's body is NOT echoed: it may quote a fragment of the
		// key we just sent, and an error a customer sees must carry a closed
		// product reason instead.
		return resp.StatusCode, fmt.Errorf("surfe: the response was not the documented JSON shape")
	}
	return resp.StatusCode, nil
}
