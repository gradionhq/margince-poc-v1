// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// OfflineProvider is the deterministic fake (PI-SEED-1): a provider that
// answers from a table instead of the network, so the whole run pipeline —
// admission, reservation, submission, polling, reconciliation, recovery — can
// be exercised on a dev stack and in the test lane without a credential, a
// budget or an internet connection.
//
// It is deliberately NOT a mock. It implements the same Adapter contract the
// real Surfe adapter does, including its polled transport, so a test that
// passes here is testing the platform rather than a stand-in for it.
//
// Its answers are keyed off the SUBJECT, not off call order, so a test asks
// for the case it wants by naming a person: "no match" is a person called
// Nomatch, an ambiguous submission is one called Ambiguous. That keeps the
// failure set reachable from an integration test that only controls its
// fixtures, and from a human clicking around a dev stack.
type OfflineProvider struct {
	// calls counts outbound attempts. PI-AC-9 and PI-AC-10 are both assertions
	// that NOTHING was called, and a counter is the only way to prove a
	// negative like that.
	calls atomic.Int64
	// pollsBeforeDone makes the polled transport real: the first N polls
	// answer pending, like Surfe's ~9 one-second polls.
	pollsBeforeDone int
	// polls counts them per provider job id. A sync.Map because the poll sweep
	// is genuinely concurrent — one process polls many runs at once, and a
	// plain map lost entries here, which reset the count to 1 on every sweep so
	// a run in the dev stack never left in_progress.
	polls sync.Map
	now   func() time.Time
}

// NewOfflineProvider builds the fake. pollsBeforeDone of 0 completes on the
// first poll, which is what most tests want; the dev stack uses 2 so the
// in-progress state is actually visible in the UI.
func NewOfflineProvider(pollsBeforeDone int, now func() time.Time) *OfflineProvider {
	return &OfflineProvider{
		pollsBeforeDone: pollsBeforeDone,
		now:             now,
	}
}

// Calls reports how many times this provider was asked to do anything
// outbound. A test asserting "no provider call occurred" reads this.
func (p *OfflineProvider) Calls() int64 { return p.calls.Load() }

// Descriptor mirrors Surfe's shape (PI-PARAM-11) so the fake exercises the
// same cost table, the same cascade and the same two pools the real adapter
// will. A fake that declared a simpler shape would let a bug in cascade
// pricing pass every test.
func (p *OfflineProvider) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		Name:        "surfe",
		Transport:   provider.TransportPolled,
		Billing:     provider.BillingPerSuccessfulResult,
		CreditPools: []provider.Pool{"email", "mobile"},
		CostTable: map[provider.Category]map[provider.Pool]int{
			"professional_email": {"email": 1},
			"personal_email":     {},
			"mobile":             {"mobile": 1},
			"linkedin_profile":   {},
			"current_employment": {},
			"job_history":        {},
		},
		Cascades: []provider.Cascade{{
			// Issued only when the professional pass returns nothing, and it
			// costs two email credits rather than one.
			Category: "personal_email",
			After:    "professional_email",
			Cost:     map[provider.Pool]int{"email": 2},
			Excludes: []provider.Category{"mobile"},
		}},
		Identifiers:  []string{"LinkedIn profile URL", "first and last name with company name or domain"},
		EgressHost:   "api.surfe.com",
		Verification: "credit-balance read",
		TermsLinks:   []provider.Link{{Label: "Terms", URL: "https://surfe.com/terms"}},
		Issuance:     provider.IssuanceSelfService,
		Categories: []provider.Category{
			"professional_email", "mobile", "linkedin_profile",
			"current_employment", "job_history", "personal_email",
		},
		Presets: map[string][]provider.Category{
			"full": {"professional_email", "mobile", "linkedin_profile",
				"current_employment", "job_history", "personal_email"},
			"professional_only": {"professional_email", "linkedin_profile",
				"current_employment", "job_history"},
		},
		DefaultPreset: "full",
	}
}

// VerifyCredential accepts any key except the ones that name a failure. The
// real adapter reads the credit balance here; so does this, because the
// descriptor says that is the verification call and a fake that skipped it
// would not prove the connect path calls it.
func (p *OfflineProvider) VerifyCredential(ctx context.Context, cred provider.Credential) (provider.Credits, error) {
	p.calls.Add(1)
	switch string(cred) {
	case "invalid":
		return provider.Credits{}, fmt.Errorf("offline provider: the credential was refused")
	case "":
		return provider.Credits{}, fmt.Errorf("offline provider: no credential presented")
	}
	return p.balances(), nil
}

// Credits is the same read, which is why the descriptor names one call for
// both.
func (p *OfflineProvider) Credits(ctx context.Context, cred provider.Credential) (provider.Credits, error) {
	p.calls.Add(1)
	return p.balances(), nil
}

func (p *OfflineProvider) balances() provider.Credits {
	return provider.Credits{
		Balances: map[provider.Pool]int{"email": 19, "mobile": 4},
		ReadAt:   p.now().UTC(),
	}
}

// Submit accepts the job and hands back a handle, like any polled provider.
// The subject's name selects which answer the later poll will give.
func (p *OfflineProvider) Submit(ctx context.Context, cred provider.Credential, req provider.Request) (provider.Submission, error) {
	p.calls.Add(1)
	switch scenarioFor(req.Identifiers) {
	case scenarioInvalidCredentials:
		return provider.Submission{Outcome: provider.OutcomeInvalidCredentials, SafeStatusCode: "credential_rejected"}, nil
	case scenarioInsufficientCredits:
		return provider.Submission{Outcome: provider.OutcomeInsufficientCredits, SafeStatusCode: "provider_out_of_credits"}, nil
	case scenarioRateLimited:
		return provider.Submission{Outcome: provider.OutcomeRateLimited, SafeStatusCode: "provider_rate_limited"}, nil
	case scenarioProviderError:
		return provider.Submission{Outcome: provider.OutcomeProviderError, SafeStatusCode: "provider_error"}, nil
	case scenarioAmbiguous:
		// The case the whole inflight_at mechanism exists for: we do not know
		// whether this landed, so the run must not be retried.
		return provider.Submission{Outcome: provider.OutcomeAmbiguous, SafeStatusCode: "submission_timeout"}, nil
	}
	// The job handle carries the scenario, because the poll that resolves this
	// run happens in another process with only the handle to go on. Keying it
	// off the correlation id alone lost the subject, which made `no_match`
	// unreachable through the real pipeline — the fake could produce it in a
	// unit test and never where it mattered.
	handle := "offline-" + req.CorrelationID
	if scenarioFor(req.Identifiers) == scenarioNoMatch {
		handle = "offline-nomatch-" + req.CorrelationID
	}
	return provider.Submission{
		Outcome:       provider.OutcomeAccepted,
		ProviderJobID: handle,
	}, nil
}

// Poll answers pending until the configured count is exhausted, then serves
// the terminal result. Re-reading a completed job by id answers the same
// result again, which is what makes the PI-PARAM-10 recovery path work: the
// platform parks no payload between attempts.
func (p *OfflineProvider) Poll(ctx context.Context, cred provider.Credential, jobID string) (provider.PollStatus, error) {
	p.calls.Add(1)
	// LoadOrStore, not load-then-store: two sweeps polling the same run at once
	// would otherwise each install their own counter and neither would climb.
	entry, _ := p.polls.LoadOrStore(jobID, &atomic.Int64{})
	counter, ok := entry.(*atomic.Int64)
	if !ok {
		return provider.PollStatus{}, fmt.Errorf("offline provider: poll counter for %s has type %T", jobID, entry)
	}
	if int(counter.Add(1)) <= p.pollsBeforeDone {
		return provider.PollStatus{Outcome: provider.OutcomePending}, nil
	}
	if strings.Contains(jobID, "nomatch") {
		return provider.PollStatus{Outcome: provider.OutcomeNoMatch, SafeStatusCode: "no_match"}, nil
	}
	return provider.PollStatus{Outcome: provider.OutcomeCompleted, Result: offlineResult(p.now().UTC())}, nil
}

// scenario names the failure the fake should produce for a subject.
type scenario int

const (
	scenarioSuccess scenario = iota
	scenarioNoMatch
	scenarioInvalidCredentials
	scenarioInsufficientCredits
	scenarioRateLimited
	scenarioProviderError
	scenarioAmbiguous
)

// scenarioFor reads the case out of the subject's name. Keyed off the SUBJECT
// rather than call order so a test picks its case by naming a fixture, and so
// two concurrent runs cannot steal each other's answer.
func scenarioFor(id provider.PersonIdentifiers) scenario {
	switch strings.ToLower(id.LastName) {
	case "nomatch":
		return scenarioNoMatch
	case "invalidcredentials":
		return scenarioInvalidCredentials
	case "insufficientcredits":
		return scenarioInsufficientCredits
	case "ratelimited":
		return scenarioRateLimited
	case "providererror":
		return scenarioProviderError
	case "ambiguous":
		return scenarioAmbiguous
	}
	return scenarioSuccess
}

// offlineResult mirrors the sanitized shape the real probe returned,
// including the two things that would otherwise be normalized away by
// accident: the professional email arrives with NO type from the provider,
// and the job-history LinkedIn fields arrive as empty strings.
func offlineResult(at time.Time) *provider.Result {
	claim := func(key provider.ClaimKey, v any) provider.Claim {
		raw, err := json.Marshal(v)
		if err != nil {
			// Marshalling a literal defined two lines above cannot fail; if it
			// somehow does, an empty claim is safer than a panic in a fake.
			return provider.Claim{Key: key, Value: json.RawMessage(`null`)}
		}
		return provider.Claim{Key: key, Value: raw}
	}
	confidence := 0.65
	return &provider.Result{
		Claims: []provider.Claim{
			claim(provider.ClaimProfessionalEmails, []map[string]any{{
				"value": "a.muster@example.com",
				// email_type absent on purpose: Surfe omitted it even under
				// the professional cascade, so the platform may label it from
				// the request policy but must never claim the provider said so.
				"validation_status": "valid",
			}}),
			claim(provider.ClaimMobilePhones, []map[string]any{{
				"value": "+49 170 0000000", "confidence": confidence,
			}}),
			claim(provider.ClaimLinkedInProfile, "https://www.linkedin.com/in/example"),
			claim(provider.ClaimCurrentEmployment, map[string]any{
				"company_name": "Example GmbH", "company_domain": "example.com",
				"job_title": "Head of Operations",
			}),
			claim(provider.ClaimJobHistory, []map[string]any{{
				"company_name": "Vorherige AG", "job_title": "Operations Manager",
				// Empty, as the real API returns them; normalizing to absent
				// is the platform's job and this is what it must handle.
				"linkedin_url": "", "started_at": "2019-01", "ended_at": "2023-06",
			}}),
			claim(provider.ClaimLocation, "Munich, Germany"),
			claim(provider.ClaimDepartments, []string{"Operations"}),
			claim(provider.ClaimSeniorities, []string{"Head"}),
		},
		PoolSpend: map[provider.Pool]int{"email": 1, "mobile": 1},
	}
}
