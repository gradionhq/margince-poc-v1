// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package surfe

// The adapter against RECORDED wire shapes, taken from Surfe's published API
// reference. No network: what these prove is that the translation in both
// directions matches the documented contract, which is the only thing this
// package is responsible for.
//
// The awkward parts of the real payload are the point — an absent emailType
// under the professional cascade, empty strings where the vendor has no
// value, month-precision job dates — because those are what a naive mapping
// gets wrong and what the person page then renders as blanks or lies.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// recorder answers with a canned status and body, and keeps the request so a
// test can assert what actually left.
type recorder struct {
	status int
	body   string
	err    error
	seen   *http.Request
	sent   string
}

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	r.seen = req
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		r.sent = string(raw)
	}
	if r.err != nil {
		return nil, r.err
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     http.Header{},
	}, nil
}

func testAdapter(rec *recorder) *Adapter {
	return New(func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) }).WithHTTPDoer(rec)
}

func aRequest() provider.Request {
	return provider.Request{
		CorrelationID: "corr-1",
		Identifiers: provider.PersonIdentifiers{
			FirstName: "Anna", LastName: "Muster", CompanyDomain: "example.com",
		},
		Categories: []provider.Category{"professional_email", "mobile", "job_history"},
	}
}

// What leaves the installation is exactly the closed identifier set, under
// the documented body shape, with the correlation id as the opaque handle.
func TestSubmitSendsTheDocumentedBodyAndNothingElse(t *testing.T) {
	rec := &recorder{status: http.StatusAccepted, body: `{"enrichmentID":"enr-9"}`}
	sub, err := testAdapter(rec).Submit(context.Background(), provider.Credential("k"), aRequest())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Outcome != provider.OutcomeAccepted || sub.ProviderJobID != "enr-9" {
		t.Fatalf("submission = %+v, want accepted with the enrichment id", sub)
	}
	if got := rec.seen.URL.String(); got != "https://api.surfe.com/v2/people/enrich" {
		t.Errorf("posted to %q, want the documented enrich endpoint", got)
	}
	if got := rec.seen.Header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("Authorization = %q, want the bearer form the API documents", got)
	}

	var body enrichRequest
	if err := json.Unmarshal([]byte(rec.sent), &body); err != nil {
		t.Fatalf("the request body is not the documented shape: %v", err)
	}
	if len(body.People) != 1 {
		t.Fatalf("sent %d people, want exactly one — a run is one subject, and batching would tie their fates together", len(body.People))
	}
	person := body.People[0]
	if person.FirstName != "Anna" || person.LastName != "Muster" || person.CompanyDomain != "example.com" {
		t.Errorf("identifiers = %+v, want the ones the run froze", person)
	}
	if person.ExternalID != "corr-1" {
		t.Error("the correlation id did not ride as externalID: a returned result cannot be matched back to its run")
	}
	// The request asked for no LinkedIn category, so the flag must be off —
	// a flag beyond the request spends a credit the reservation does not
	// cover.
	if body.Include.LinkedInURL {
		t.Error("linkedInUrl was requested though the run did not freeze that category")
	}
	if !body.Include.Email || !body.Include.Mobile || !body.Include.JobHistory {
		t.Errorf("include = %+v, want the three categories the run froze", body.Include)
	}
	// No cascade was permitted, so the professional-only policy must reach
	// the vendor rather than widening to personal.
	if body.EnrichmentOptions.AcceptedEmailType != "professional" {
		t.Errorf("acceptedEmailType = %q, want professional — the run permitted no personal fallback", body.EnrichmentOptions.AcceptedEmailType)
	}
}

// The cascade the run froze is what widens the vendor's email policy.
func TestAPermittedCascadeWidensTheEmailPolicy(t *testing.T) {
	rec := &recorder{status: http.StatusAccepted, body: `{"enrichmentID":"enr-9"}`}
	req := aRequest()
	req.Cascades = []provider.Cascade{{Category: "personal_email", After: "professional_email"}}
	if _, err := testAdapter(rec).Submit(context.Background(), provider.Credential("k"), req); err != nil {
		t.Fatal(err)
	}
	var body enrichRequest
	if err := json.Unmarshal([]byte(rec.sent), &body); err != nil {
		t.Fatal(err)
	}
	if body.EnrichmentOptions.AcceptedEmailType != "personal" {
		t.Errorf("acceptedEmailType = %q, want personal — the run's cascade permits the fallback", body.EnrichmentOptions.AcceptedEmailType)
	}
}

// A transport failure after the request left is AMBIGUOUS, never a retry:
// the charge may already have happened.
func TestATransportFailureIsAmbiguousRatherThanAFailure(t *testing.T) {
	rec := &recorder{err: context.DeadlineExceeded}
	sub, err := testAdapter(rec).Submit(context.Background(), provider.Credential("k"), aRequest())
	if err != nil {
		t.Fatalf("a timeout surfaced as an error rather than an outcome: %v", err)
	}
	if sub.Outcome != provider.OutcomeAmbiguous {
		t.Errorf("outcome = %s, want ambiguous — retrying a request that may have landed is how one charge becomes two", sub.Outcome)
	}
}

// An accepted submission with no handle is unknown, not success: the work
// may be running and can never be read back.
func TestAcceptedWithNoHandleIsAmbiguous(t *testing.T) {
	rec := &recorder{status: http.StatusAccepted, body: `{"enrichmentID":""}`}
	sub, err := testAdapter(rec).Submit(context.Background(), provider.Credential("k"), aRequest())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Outcome != provider.OutcomeAmbiguous {
		t.Errorf("outcome = %s, want ambiguous: accepted work with no handle can never be polled", sub.Outcome)
	}
}

// Every vendor refusal maps onto the closed product vocabulary, and the body
// is never echoed.
func TestVendorRefusalsMapOntoClosedProductCodes(t *testing.T) {
	for _, c := range []struct {
		status  int
		outcome provider.Outcome
		code    string
	}{
		{http.StatusUnauthorized, provider.OutcomeInvalidCredentials, "credential_rejected"},
		{http.StatusForbidden, provider.OutcomeInvalidCredentials, "credential_rejected"},
		{http.StatusPaymentRequired, provider.OutcomeInsufficientCredits, "provider_out_of_credits"},
		{http.StatusTooManyRequests, provider.OutcomeRateLimited, "provider_rate_limited"},
		{http.StatusInternalServerError, provider.OutcomeProviderError, "provider_error"},
	} {
		rec := &recorder{status: c.status, body: `{"message":"key sk-live-SECRET is invalid"}`}
		sub, err := testAdapter(rec).Submit(context.Background(), provider.Credential("k"), aRequest())
		if err != nil {
			t.Fatalf("status %d: %v", c.status, err)
		}
		if sub.Outcome != c.outcome || sub.SafeStatusCode != c.code {
			t.Errorf("status %d → %s/%s, want %s/%s", c.status, sub.Outcome, sub.SafeStatusCode, c.outcome, c.code)
		}
		if strings.Contains(sub.SafeStatusCode, "SECRET") {
			t.Error("the vendor's body reached the safe status code, which may quote the key we just sent")
		}
	}
}

// A poll that is still running costs nothing and asserts nothing.
func TestPollReportsPendingUntilTheVendorCompletes(t *testing.T) {
	rec := &recorder{status: http.StatusOK, body: `{"status":"IN_PROGRESS","percentCompleted":40,"people":[]}`}
	status, err := testAdapter(rec).Poll(context.Background(), provider.Credential("k"), "enr-9")
	if err != nil {
		t.Fatal(err)
	}
	if status.Outcome != provider.OutcomePending {
		t.Errorf("outcome = %s, want pending", status.Outcome)
	}
}

// completedBody is the shape the reference documents, including the parts a
// naive mapping gets wrong: an absent emailType, empty-string job-history
// fields, and month-precision dates.
const completedBody = `{
  "enrichmentID": "enr-9",
  "status": "COMPLETED",
  "people": [{
    "status": "COMPLETED",
    "firstName": "Anna", "lastName": "Muster",
    "emails": [{"email": "a.muster@example.com", "validationStatus": "valid"}],
    "mobilePhones": [{"mobilePhone": "+49 170 0000000", "confidenceScore": 0.65}],
    "linkedInUrl": "https://www.linkedin.com/in/example",
    "jobTitle": "Head of Operations",
    "companyName": "Example GmbH", "companyDomain": "example.com",
    "jobHistory": [
      {"companyName": "Vorherige AG", "jobTitle": "Operations Manager",
       "linkedInURL": "", "startDate": "2019-01", "endDate": "2023-06"},
      {"companyName": "", "jobTitle": "Intern", "linkedInURL": "", "startDate": "", "endDate": ""}
    ],
    "location": "Munich, Germany", "city": "Munich", "country": "Germany",
    "departments": ["Operations"], "seniorities": ["Head"]
  }]
}`

func claimByKey(t *testing.T, claims []provider.Claim, key provider.ClaimKey) provider.Claim {
	t.Helper()
	for _, c := range claims {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("no %s claim in the normalized result", key)
	return provider.Claim{}
}

func TestPollNormalizesTheAwkwardPartsOfTheRealPayload(t *testing.T) {
	rec := &recorder{status: http.StatusOK, body: completedBody}
	status, err := testAdapter(rec).Poll(context.Background(), provider.Credential("k"), "enr-9")
	if err != nil {
		t.Fatal(err)
	}
	if status.Outcome != provider.OutcomeCompleted || status.Result == nil {
		t.Fatalf("outcome = %s with result %v, want completed with a result", status.Outcome, status.Result)
	}

	// An address with NO emailType is professional by REQUEST, and the
	// adapter must not invent the label — it leaves email_type absent and
	// the reader marks it as requested-cascade.
	var emails []map[string]any
	if err := json.Unmarshal(claimByKey(t, status.Result.Claims, provider.ClaimProfessionalEmails).Value, &emails); err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 || emails[0]["value"] != "a.muster@example.com" {
		t.Fatalf("professional emails = %+v, want the one address returned", emails)
	}
	if _, claimed := emails[0]["email_type"]; claimed {
		t.Error("the adapter asserted an email_type the vendor did not return — a request-context label must never masquerade as the provider's")
	}

	// Empty strings are absent, and an entry with no employer is dropped.
	var history []map[string]any
	if err := json.Unmarshal(claimByKey(t, status.Result.Claims, provider.ClaimJobHistory).Value, &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("job history has %d entries, want 1 — the entry with no company name renders as a blank line", len(history))
	}
	if _, present := history[0]["linkedin_url"]; present {
		t.Error("an empty-string LinkedIn URL was stored as a value: the vendor's empty string is an absent field, not a blank link")
	}
	if history[0]["started_at"] != "2019-01" {
		t.Errorf("started_at = %v, want the month-precision date the vendor sends", history[0]["started_at"])
	}

	// What the run is charged for is derived from what came BACK, which is
	// what per-successful-result billing means.
	if status.Result.PoolSpend["email"] != 1 || status.Result.PoolSpend["mobile"] != 1 {
		t.Errorf("pool spend = %v, want one credit per returned value", status.Result.PoolSpend)
	}
}

// A completed enrichment that found nothing is a no-match, which releases
// the whole reservation on per-successful-result billing.
func TestACompletedEnrichmentWithNothingFoundIsANoMatch(t *testing.T) {
	rec := &recorder{status: http.StatusOK, body: `{"status":"COMPLETED","people":[{"status":"NOT_FOUND","firstName":"Anna"}]}`}
	status, err := testAdapter(rec).Poll(context.Background(), provider.Credential("k"), "enr-9")
	if err != nil {
		t.Fatal(err)
	}
	if status.Outcome != provider.OutcomeNoMatch {
		t.Errorf("outcome = %s, want no_match — an empty completion must release the hold, not read as a purchase", status.Outcome)
	}
}

// The credential check is the credit read, and a refused key is its own
// error so Connect can say "that key does not work".
func TestCredentialVerificationReadsTheBalanceAndNamesARefusal(t *testing.T) {
	rec := &recorder{status: http.StatusOK, body: `{"totalEmail":19,"totalMobile":4,"totalSearch":100}`}
	credits, err := testAdapter(rec).VerifyCredential(context.Background(), provider.Credential("k"))
	if err != nil {
		t.Fatal(err)
	}
	if credits.Balances["email"] != 19 || credits.Balances["mobile"] != 4 {
		t.Errorf("balances = %v, want the two pools the descriptor declares", credits.Balances)
	}
	if got := rec.seen.URL.Path; got != "/v1/credits" {
		t.Errorf("read %q, want the documented credits endpoint", got)
	}

	refused := &recorder{status: http.StatusUnauthorized, body: `{"message":"unauthorized"}`}
	if _, err := testAdapter(refused).VerifyCredential(context.Background(), provider.Credential("bad")); err == nil {
		t.Error("a refused key verified successfully, so it would be sealed and stored")
	}
}

// The credential must not leave the declared host. Go copies the
// Authorization header across a redirect to any SUBDOMAIN of the origin, so
// without an explicit refusal a 302 from api.surfe.com to anything.surfe.com
// replays the customer's key to whoever answered.
func TestARedirectIsRefusedSoTheKeyCannotFollowIt(t *testing.T) {
	var calls int
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"totalEmail":1,"totalMobile":1}`))
	}))
	defer final.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// The REAL client and its real redirect policy — the thing under test.
	a := New(time.Now)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, redirector.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer SUPERSECRET")
	resp, err := a.client.Do(req)
	if err == nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("closing the unexpected response: %v", closeErr)
		}
		t.Fatal("the redirect was followed: the customer's key just reached a host the descriptor never declared")
	}
	if calls != 1 {
		t.Errorf("%d servers were called, want 1 — only the declared host may be reached", calls)
	}
}

// An unrecognized status is AMBIGUOUS, never a definite refusal: the request
// reached the vendor and the work may be running and billable, so releasing
// the hold would be the double-charge PI-AC-4 exists to prevent.
func TestAnUnrecognizedStatusParksRatherThanReleasingTheHold(t *testing.T) {
	for _, status := range []int{
		http.StatusCreated,   // an ordinary answer for a job-creation endpoint
		http.StatusNoContent, // what a proxy or WAF in front of the vendor emits
		http.StatusRequestTimeout,
		http.StatusConflict,
	} {
		rec := &recorder{status: status, body: `{}`}
		sub, err := testAdapter(rec).Submit(context.Background(), provider.Credential("k"), aRequest())
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if sub.Outcome != provider.OutcomeAmbiguous {
			t.Errorf("status %d → %s, want ambiguous: a definite refusal releases the credit hold on work the vendor may have done and charged for",
				status, sub.Outcome)
		}
	}
}

// A poll whose body cannot be read settles nothing, so it reads as pending
// rather than failing the whole workspace's sweep on every tick.
func TestAnUnreadablePollReadsAsPendingRatherThanFailingTheSweep(t *testing.T) {
	rec := &recorder{status: http.StatusOK, body: `{"status":"COMPLE`}
	status, err := testAdapter(rec).Poll(context.Background(), provider.Credential("k"), "enr-9")
	if err != nil {
		t.Fatalf("an unreadable poll surfaced as an error, which fails the drain for every other run in the workspace: %v", err)
	}
	if status.Outcome != provider.OutcomePending {
		t.Errorf("outcome = %s, want pending — an unread poll is an unfinished one, and the run's expiry is what ends it", status.Outcome)
	}
}

// Surfe returns EVERY address it found from the one lookup the run paid for.
// Charging per value would bill a well-documented person more than a thinly
// documented one for the same question, and would exceed the reservation.
func TestSpendIsOnePerPoolHoweverManyValuesComeBack(t *testing.T) {
	body := `{"status":"COMPLETED","people":[{
		"emails":[{"email":"a@example.com"},{"email":"anna@example.com"},{"email":"a.muster@example.com"}],
		"mobilePhones":[{"mobilePhone":"+49 1"},{"mobilePhone":"+49 2"}]}]}`
	rec := &recorder{status: http.StatusOK, body: body}
	status, err := testAdapter(rec).Poll(context.Background(), provider.Credential("k"), "enr-9")
	if err != nil {
		t.Fatal(err)
	}
	if status.Result == nil {
		t.Fatal("a completed enrichment returned no result")
	}
	if status.Result.PoolSpend["email"] != 1 || status.Result.PoolSpend["mobile"] != 1 {
		t.Errorf("pool spend = %v, want one credit per pool — three addresses came from ONE paid lookup, and charging per value would exceed the reservation the platform took",
			status.Result.PoolSpend)
	}
}

// The descriptor's declared egress host is what the platform discloses to
// the customer, so the adapter's own calls must honour it.
func TestEveryCallGoesToTheDeclaredEgressHost(t *testing.T) {
	a := New(time.Now)
	if a.Descriptor().EgressHost != egressHost {
		t.Fatalf("descriptor declares %q but the adapter calls %q", a.Descriptor().EgressHost, egressHost)
	}
	rec := &recorder{status: http.StatusOK, body: `{"totalEmail":1,"totalMobile":1}`}
	if _, err := testAdapter(rec).Credits(context.Background(), provider.Credential("k")); err != nil {
		t.Fatal(err)
	}
	if rec.seen.URL.Host != egressHost {
		t.Errorf("called %q, want the declared egress host", rec.seen.URL.Host)
	}
}
