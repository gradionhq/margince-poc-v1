// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

func fixedClock() func() time.Time {
	at := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

func person(last string) provider.Request {
	return provider.Request{
		CorrelationID: "corr-" + last,
		Identifiers:   provider.PersonIdentifiers{FirstName: "Anna", LastName: last, CompanyName: "Example GmbH"},
		Categories:    []provider.Category{"professional_email", "mobile"},
	}
}

// The fake must answer the whole failure set, because every one of those
// branches is a run state the platform has to handle and none of them can be
// reached from a real provider on demand.
func TestOfflineProviderServesTheWholeFailureSet(t *testing.T) {
	p := NewOfflineProvider(0, fixedClock())
	ctx := context.Background()

	for _, tc := range []struct {
		last string
		want provider.Outcome
	}{
		{"Invalidcredentials", provider.OutcomeInvalidCredentials},
		{"Insufficientcredits", provider.OutcomeInsufficientCredits},
		{"Ratelimited", provider.OutcomeRateLimited},
		{"Providererror", provider.OutcomeProviderError},
		{"Ambiguous", provider.OutcomeAmbiguous},
	} {
		got, err := p.Submit(ctx, provider.Credential("k"), person(tc.last))
		if err != nil {
			t.Fatalf("%s: %v", tc.last, err)
		}
		if got.Outcome != tc.want {
			t.Errorf("%s -> %s, want %s", tc.last, got.Outcome, tc.want)
		}
		if got.ProviderJobID != "" {
			t.Errorf("%s issued a job handle for a failed submission", tc.last)
		}
	}

	// A success accepts and hands back a handle, because the transport is
	// polled: the answer arrives later, by re-reading that handle.
	ok, err := p.Submit(ctx, provider.Credential("k"), person("Muster"))
	if err != nil {
		t.Fatal(err)
	}
	if ok.Outcome != provider.OutcomeAccepted || ok.ProviderJobID == "" {
		t.Fatalf("success -> %s / %q, want accepted with a handle", ok.Outcome, ok.ProviderJobID)
	}
}

// The polled transport is the point of this fake: if it completed on the
// first poll always, nothing would ever exercise the in_progress state or the
// sweep that drives it.
func TestOfflineProviderStaysPendingUntilItsPollsAreSpent(t *testing.T) {
	p := NewOfflineProvider(2, fixedClock())
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		st, err := p.Poll(ctx, provider.Credential("k"), "job-1")
		if err != nil {
			t.Fatal(err)
		}
		if st.Outcome != provider.OutcomePending {
			t.Fatalf("poll %d -> %s, want pending", i, st.Outcome)
		}
	}
	st, err := p.Poll(ctx, provider.Credential("k"), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != provider.OutcomeCompleted || st.Result == nil {
		t.Fatalf("third poll -> %s, want completed with a result", st.Outcome)
	}

	// Re-reading a finished job answers the same thing again. This is what
	// makes claim-hand-off recovery possible without parking the payload
	// anywhere (PI-PARAM-10).
	again, err := p.Poll(ctx, provider.Credential("k"), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Outcome != provider.OutcomeCompleted || again.Result == nil {
		t.Fatalf("re-read -> %s, want the completed result again", again.Outcome)
	}
}

// The two shapes the real probe returned that a careless normalizer would get
// wrong. They are in the fake precisely so the platform's handling of them is
// exercised rather than assumed.
func TestOfflineResultMirrorsTheAwkwardPartsOfTheRealPayload(t *testing.T) {
	res := offlineResult(fixedClock()())

	byKey := map[provider.ClaimKey]json.RawMessage{}
	for _, c := range res.Claims {
		byKey[c.Key] = c.Value
	}

	var emails []map[string]any
	if err := json.Unmarshal(byKey[provider.ClaimProfessionalEmails], &emails); err != nil {
		t.Fatal(err)
	}
	if _, present := emails[0]["email_type"]; present {
		t.Error("the professional email carries an email_type; the real provider omits it, and claiming otherwise would put words in its mouth")
	}

	var history []map[string]any
	if err := json.Unmarshal(byKey[provider.ClaimJobHistory], &history); err != nil {
		t.Fatal(err)
	}
	if history[0]["linkedin_url"] != "" {
		t.Error("job history should carry the empty linkedin_url the real API sends, so normalization to absent is exercised")
	}
}

// PI-AC-9 and PI-AC-10 are both assertions that nothing was called. They are
// only provable if the fake counts.
func TestOfflineProviderCountsEveryOutboundAttempt(t *testing.T) {
	p := NewOfflineProvider(0, fixedClock())
	ctx := context.Background()
	if p.Calls() != 0 {
		t.Fatalf("a fresh provider reports %d calls", p.Calls())
	}
	if _, err := p.VerifyCredential(ctx, provider.Credential("k")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Submit(ctx, provider.Credential("k"), person("Muster")); err != nil {
		t.Fatal(err)
	}
	if p.Calls() != 2 {
		t.Errorf("counted %d calls, want 2 — a test asserting \"no provider call\" depends on this", p.Calls())
	}
}

// The descriptor must price the cascade the way the real provider bills it,
// or the reservation is wrong in exactly the case that costs the customer
// double.
func TestOfflineDescriptorPricesTheCascade(t *testing.T) {
	d := NewOfflineProvider(0, fixedClock()).Descriptor()

	// Professional + mobile only: one credit from each pool, no fallback.
	cost, err := d.WorstCase([]provider.Category{"professional_email", "mobile"})
	if err != nil {
		t.Fatal(err)
	}
	if cost["email"] != 1 || cost["mobile"] != 1 {
		t.Errorf("without the fallback: %v, want email 1 / mobile 1", cost)
	}

	// Adding the personal-email fallback reserves its two credits UP FRONT,
	// because a cascade that discovered mid-run it could not afford itself is
	// a charge nobody authorized.
	cost, err = d.WorstCase([]provider.Category{"professional_email", "mobile", "personal_email"})
	if err != nil {
		t.Fatal(err)
	}
	if cost["email"] != 3 || cost["mobile"] != 1 {
		t.Errorf("with the fallback: %v, want email 3 (1 + 2) / mobile 1", cost)
	}
}

// The no-match case has to survive the hand-off from submit to poll, because
// that is the only route it takes in production: the run is submitted in one
// process and resolved in another, with nothing but the job handle between
// them. A fake that could only produce no_match by being asked directly would
// leave the real path untested.
func TestOfflineNoMatchSurvivesSubmitThenPoll(t *testing.T) {
	p := NewOfflineProvider(0, fixedClock())
	ctx := context.Background()

	sub, err := p.Submit(ctx, provider.Credential("k"), person("Nomatch"))
	if err != nil {
		t.Fatal(err)
	}
	if sub.Outcome != provider.OutcomeAccepted {
		t.Fatalf("submit -> %s, want accepted", sub.Outcome)
	}

	st, err := p.Poll(ctx, provider.Credential("k"), sub.ProviderJobID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != provider.OutcomeNoMatch {
		t.Errorf("poll of a no-match job -> %s, want no_match", st.Outcome)
	}
}
