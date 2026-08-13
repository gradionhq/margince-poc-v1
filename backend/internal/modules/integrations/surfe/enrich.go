// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package surfe

// The four calls the Adapter contract names, over Surfe's v2 asynchronous
// bulk-enrichment API:
//
//	POST /v2/people/enrich      → an enrichment id (202)
//	GET  /v2/people/enrich/{id} → IN_PROGRESS, or COMPLETED with the people
//	GET  /v1/credits            → the balance, which is also the credential check
//
// Every failure classifies into the port's closed Outcome vocabulary. The one
// that matters most is OutcomeAmbiguous: a timeout or an unreadable answer
// AFTER the request left means the charge may have happened, so the run parks
// with its reservation held rather than retrying into a second charge.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// The wire shapes, named as SURFE names them. The tags must match its
// camelCase format exactly, or the request is rejected and the response
// silently decodes to zero values — the vendor's keys are not ours to case.
//
//nolint:tagliatelle // the vendor's wire format, not this repo's convention.
type (
	enrichRequest struct {
		EnrichmentOptions enrichOptions `json:"enrichmentOptions"`
		Include           includeFlags  `json:"include"`
		People            []wirePerson  `json:"people"`
	}
	enrichOptions struct {
		// AcceptedEmailType is how the frozen cascade reaches the vendor:
		// "professional" asks for a work address only, and the platform
		// widens to "personal" only where the policy permits that fallback.
		AcceptedEmailType string `json:"acceptedEmailType,omitempty"`
		// SkipMobileEnrichmentIfNoEmailFound spends no mobile credit on a
		// subject the vendor could not place at all.
		SkipMobileEnrichmentIfNoEmailFound bool `json:"skipMobileEnrichmentIfNoEmailFound"`
	}
	includeFlags struct {
		Email       bool `json:"email"`
		JobHistory  bool `json:"jobHistory"`
		LinkedInURL bool `json:"linkedInUrl"`
		Mobile      bool `json:"mobile"`
	}
	wirePerson struct {
		FirstName     string `json:"firstName,omitempty"`
		LastName      string `json:"lastName,omitempty"`
		LinkedInURL   string `json:"linkedinUrl,omitempty"`
		CompanyName   string `json:"companyName,omitempty"`
		CompanyDomain string `json:"companyDomain,omitempty"`
		ExternalID    string `json:"externalID,omitempty"`
	}
	enrichAccepted struct {
		EnrichmentID string `json:"enrichmentID"`
	}
	enrichResult struct {
		Status string       `json:"status"`
		People []wireResult `json:"people"`
	}
	wireResult struct {
		Status        string       `json:"status"`
		FirstName     string       `json:"firstName"`
		LastName      string       `json:"lastName"`
		Emails        []wireEmail  `json:"emails"`
		MobilePhones  []wireMobile `json:"mobilePhones"`
		LinkedInURL   string       `json:"linkedInUrl"`
		JobTitle      string       `json:"jobTitle"`
		CompanyName   string       `json:"companyName"`
		CompanyDomain string       `json:"companyDomain"`
		JobHistory    []wireJob    `json:"jobHistory"`
		Location      string       `json:"location"`
		City          string       `json:"city"`
		State         string       `json:"state"`
		Country       string       `json:"country"`
		Departments   []string     `json:"departments"`
		Seniorities   []string     `json:"seniorities"`
	}
	wireEmail struct {
		Email string `json:"email"`
		// EmailType may be ABSENT even under the professional cascade. The
		// platform then labels the address from what it ASKED for and marks
		// the source — it never claims the provider said so.
		EmailType        string `json:"emailType"`
		ValidationStatus string `json:"validationStatus"`
	}
	wireMobile struct {
		MobilePhone     string   `json:"mobilePhone"`
		ConfidenceScore *float64 `json:"confidenceScore"`
	}
	wireJob struct {
		CompanyName string `json:"companyName"`
		JobTitle    string `json:"jobTitle"`
		LinkedInURL string `json:"linkedInURL"`
		StartDate   string `json:"startDate"`
		EndDate     string `json:"endDate"`
	}
	creditsResult struct {
		TotalEmail  int `json:"totalEmail"`
		TotalMobile int `json:"totalMobile"`
	}
)

// statusCompleted is the only vendor status this adapter tests for.
// Anything else — IN_PROGRESS, or a state they add later — reads as pending,
// which costs nothing and asks again; treating an unfamiliar status as
// finished would settle a run against an answer that had not arrived.
const statusCompleted = "COMPLETED"

// ErrCredentialRefused reports that Surfe rejected the key. Its own error so
// Connect can answer "that key does not work" rather than a generic failure.
var ErrCredentialRefused = errors.New("surfe: the credential was refused")

// VerifyCredential reads the credit balance, which is the cheapest call that
// proves a key works — and the same call Credits makes, which is why the
// descriptor names one for both.
func (a *Adapter) VerifyCredential(ctx context.Context, cred provider.Credential) (provider.Credits, error) {
	return a.Credits(ctx, cred)
}

// Credits reads the provider's own balance. Reported as THEIR number and
// never as ours: the customer may spend the same credits through Surfe's own
// app, so this is a reading, not an accounting.
func (a *Adapter) Credits(ctx context.Context, cred provider.Credential) (provider.Credits, error) {
	var out creditsResult
	status, err := a.call(ctx, cred, http.MethodGet, "/v1/credits", nil, &out)
	if err != nil {
		return provider.Credits{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return provider.Credits{}, ErrCredentialRefused
	}
	if status != http.StatusOK {
		return provider.Credits{}, fmt.Errorf("surfe: the credit read answered %d", status)
	}
	return provider.Credits{
		Balances: map[provider.Pool]int{poolEmail: out.TotalEmail, poolMobile: out.TotalMobile},
		ReadAt:   a.now().UTC(),
	}, nil
}

// Submit starts one person's enrichment.
//
// The correlation id rides as externalID: it is an opaque handle carrying no
// subject identity, which is what makes it safe to hand a third party, and it
// is how a returned result is matched back to its run.
func (a *Adapter) Submit(ctx context.Context, cred provider.Credential, req provider.Request) (provider.Submission, error) {
	body := enrichRequest{
		EnrichmentOptions: enrichOptions{
			AcceptedEmailType: acceptedEmailType(req),
			// A subject the vendor cannot place at all yields no mobile
			// either, and asking for one would spend a mobile credit on a
			// lookup already known to have failed.
			SkipMobileEnrichmentIfNoEmailFound: true,
		},
		Include: includeFor(req.Categories),
		People: []wirePerson{{
			FirstName:     req.Identifiers.FirstName,
			LastName:      req.Identifiers.LastName,
			LinkedInURL:   req.Identifiers.LinkedInURL,
			CompanyName:   req.Identifiers.CompanyName,
			CompanyDomain: req.Identifiers.CompanyDomain,
			ExternalID:    req.CorrelationID,
		}},
	}
	var accepted enrichAccepted
	status, err := a.call(ctx, cred, http.MethodPost, "/v2/people/enrich", body, &accepted)
	if err != nil {
		// The request LEFT and its fate is unknown — a timeout, a dropped
		// connection, an unreadable answer. That is an OUTCOME, not a
		// failure: returning the error would let the job retry, and a retry
		// is how one ambiguous charge becomes two certain ones (PI-AC-4).
		// The caller parks the run with its reservation held.
		//
		//nolint:nilerr // the error IS the outcome here; see above.
		return provider.Submission{Outcome: provider.OutcomeAmbiguous, SafeStatusCode: "submission_timeout"}, nil
	}
	if status == http.StatusOK || status == http.StatusAccepted {
		if accepted.EnrichmentID == "" {
			// Accepted with no handle to poll: the work may be running and
			// we can never read it back, which is exactly unknown.
			return provider.Submission{Outcome: provider.OutcomeAmbiguous, SafeStatusCode: "no_job_handle"}, nil
		}
		return provider.Submission{Outcome: provider.OutcomeAccepted, ProviderJobID: accepted.EnrichmentID}, nil
	}
	outcome, safeCode := refusalFor(status)
	return provider.Submission{Outcome: outcome, SafeStatusCode: safeCode}, nil
}

// Poll re-reads a submitted enrichment by its handle. It is also the recovery
// read: a completed enrichment re-serves the same result, which is what lets
// the platform park no payload between hand-off attempts.
func (a *Adapter) Poll(ctx context.Context, cred provider.Credential, providerJobID string) (provider.PollStatus, error) {
	var out enrichResult
	status, err := a.call(ctx, cred, http.MethodGet, "/v2/people/enrich/"+providerJobID, nil, &out)
	if err != nil {
		// A failed poll costs nothing and settles nothing, so it reads as
		// PENDING rather than as an error: the sweep asks again on its next
		// tick and the run's own expiry bounds how long that can go on.
		//
		// Returning the error instead would put one unreadable response into
		// the sweep's joined error on every tick — failing the whole
		// workspace's drain job, for a run that is merely unfinished.
		//
		//nolint:nilerr // the failure IS the outcome: an unread poll is an
		// unfinished one, and the expiry is what ends it.
		return provider.PollStatus{Outcome: provider.OutcomePending}, nil
	}
	if status != http.StatusOK {
		outcome, safeCode := refusalFor(status)
		return provider.PollStatus{Outcome: outcome, SafeStatusCode: safeCode}, nil
	}
	if out.Status != statusCompleted {
		return provider.PollStatus{Outcome: provider.OutcomePending}, nil
	}
	if len(out.People) == 0 {
		return provider.PollStatus{Outcome: provider.OutcomeNoMatch, SafeStatusCode: "no_match"}, nil
	}
	person := out.People[0]
	claims, err := claimsFor(person)
	if err != nil {
		// A result we cannot encode is NOT a no-match: the run completed and
		// was charged. Surfacing the error leaves it in progress for the
		// sweep to re-poll rather than releasing a hold against work the
		// provider actually did.
		return provider.PollStatus{}, err
	}
	if len(claims) == 0 {
		// Completed, and the vendor found nothing worth returning. A
		// no-match rather than an empty success: on per-successful-result
		// billing it releases the whole reservation.
		return provider.PollStatus{Outcome: provider.OutcomeNoMatch, SafeStatusCode: "no_match"}, nil
	}
	return provider.PollStatus{
		Outcome: provider.OutcomeCompleted,
		Result:  &provider.Result{Claims: claims, PoolSpend: spendFor(person)},
	}, nil
}

// acceptedEmailType translates the frozen policy into the vendor's one knob.
// Personal is asked for only where the run's own cascade permits it; the
// absence of that cascade means the customer chose professional-only, and
// widening it here would buy a category they refused.
func acceptedEmailType(req provider.Request) string {
	for _, c := range req.Cascades {
		if c.Category == categoryPersonalEmail {
			return emailTypePersonal
		}
	}
	return emailTypeProfessional
}

// includeFor maps the requested categories onto the vendor's four flags.
// Nothing is asked for that the run did not freeze: a flag set beyond the
// request would spend a credit the reservation does not cover.
func includeFor(categories []provider.Category) includeFlags {
	var flags includeFlags
	for _, c := range categories {
		switch c {
		case categoryProfessionalEmail, categoryPersonalEmail:
			flags.Email = true
		case categoryMobile:
			flags.Mobile = true
		case categoryLinkedInProfile:
			flags.LinkedInURL = true
		case categoryJobHistory:
			flags.JobHistory = true
		}
	}
	return flags
}

// refusalFor classifies a vendor status into the port's closed vocabulary.
//
// The named cases are the ones that PROVE no work was done, and only those
// release the credit hold. Everything else — including an unrecognized 2xx, a
// 3xx from a proxy, a 408 or a 409 — is AMBIGUOUS: the request reached the
// vendor, the enrichment may be running and billable, and calling that a
// definite refusal would release a hold on work the customer may have been
// charged for. That is the double-charge PI-AC-4 exists to prevent, reached
// without any retry at all.
//
// Closed on the safe side, in other words: the platform's definiteRefusals
// set is what releases money, so this must never hand it a status it merely
// failed to recognize.
//
// The status code is the ONLY thing read. The body may echo a fragment of the
// key we just sent, and a customer-visible reason must be a closed product
// code rather than a vendor's prose.
func refusalFor(status int) (provider.Outcome, string) {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return provider.OutcomeInvalidCredentials, "credential_rejected"
	case status == http.StatusPaymentRequired:
		return provider.OutcomeInsufficientCredits, "provider_out_of_credits"
	case status == http.StatusTooManyRequests:
		return provider.OutcomeRateLimited, "provider_rate_limited"
	case status == http.StatusBadRequest, status == http.StatusNotFound,
		status == http.StatusUnprocessableEntity:
		// The vendor rejected the REQUEST rather than doing work on it: a
		// malformed body, an unknown handle, an unsellable selection. No
		// enrichment ran, so the hold is released.
		return provider.OutcomeProviderError, "provider_error"
	case status >= 500:
		// The vendor's own fault, and a definite refusal: a 5xx means it did
		// not complete the work.
		return provider.OutcomeProviderError, "provider_error"
	default:
		return provider.OutcomeAmbiguous, "unexpected_status"
	}
}

// monthOrDate reads the vendor's job dates. The documentation says ISO 8601;
// the observed values are month-precision ("2019-01"), so both are accepted
// and anything else is absent rather than guessed.
func monthOrDate(value string) string {
	if value == "" {
		return ""
	}
	for _, layout := range []string{"2006-01", time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.Format("2006-01")
		}
	}
	return ""
}
