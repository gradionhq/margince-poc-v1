// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// Consent enforcement end to end (B-EP07.11/.12, A22/ADR-0011): the
// purpose catalog seeds at bootstrap, recordConsent writes the
// append-only proof + audit + event, and the send path is default-deny
// per purpose — unknown blocks, a foreign-purpose grant blocks,
// withdrawal re-blocks, and the German double-opt-in norm holds.

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

type consentEnv struct {
	*env
	personID   string
	activityID string
	purposes   map[string]string // key -> id
}

func setupConsent(t *testing.T) *consentEnv {
	t.Helper()
	e := setup(t)
	e.slug = "consent-e2e"
	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name": "Consent E2E", "admin_email": "dpo@fable.test",
		"admin_display_name": "DPO", "admin_password": "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap → %d", status)
	}
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email": "dpo@fable.test", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}

	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Consent Subject",
		"emails":    []anyMap{{"email": "subject@consent.test"}},
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	var activity struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/activities", anyMap{
		"kind": "email", "subject": "Inbound question", "direction": "inbound",
		"links": []anyMap{{"entity_type": "person", "entity_id": person.ID}},
	}, nil, &activity); status != http.StatusCreated {
		t.Fatalf("log anchor activity → %d", status)
	}

	var purposeList struct {
		Data []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/consent-purposes", nil, nil, &purposeList); status != http.StatusOK {
		t.Fatalf("list purposes → %d", status)
	}
	purposes := map[string]string{}
	for _, p := range purposeList.Data {
		purposes[p.Key] = p.ID
	}
	if purposes["transactional"] == "" || purposes["marketing_email"] == "" {
		t.Fatalf("bootstrap did not seed the purpose catalog: %+v", purposeList.Data)
	}
	return &consentEnv{env: e, personID: person.ID, activityID: activity.ID, purposes: purposes}
}

func (c *consentEnv) send(t *testing.T, purpose string) (int, string) {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	status := c.call(t, "POST", "/v1/activities/"+c.activityID+"/send-email", anyMap{
		"subject": "Re: Inbound question", "body": "answer",
		"to": []string{"subject@consent.test"}, "consent_purpose": purpose,
	}, nil, &problem)
	return status, problem.Code
}

func TestConsentDefaultDenySuppressesSends(t *testing.T) {
	c := setupConsent(t)

	// Drafting is 🟢 and consent-free — it sends nothing.
	var draft struct {
		Subject string `json:"subject"`
	}
	if status := c.call(t, "POST", "/v1/activities/"+c.activityID+"/draft-email",
		anyMap{"intent": "friendly nudge"}, nil, &draft); status != http.StatusOK {
		t.Fatalf("draft → %d", status)
	}
	if draft.Subject != "Re: Inbound question" {
		t.Fatalf("draft subject = %q", draft.Subject)
	}

	// unknown state → suppressed.
	if status, code := c.send(t, "transactional"); status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("send with unknown consent → %d %q, want 409 consent_not_granted", status, code)
	}
	// An undefined purpose can authorize nothing.
	if status, code := c.send(t, "no-such-purpose"); status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("send under unknown purpose → %d %q", status, code)
	}

	// Grant transactional; the send under THAT purpose flows.
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["transactional"], "new_state": "granted", "lawful_basis": "consent",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("record consent → %d", status)
	}
	if status, code := c.send(t, "transactional"); status != http.StatusAccepted {
		t.Fatalf("granted send → %d %q, want 202", status, code)
	}
	// …but the grant is per PURPOSE: marketing stays suppressed.
	if status, code := c.send(t, "marketing_email"); status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("foreign-purpose send → %d %q, want 409", status, code)
	}

	// Withdrawal re-blocks.
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["transactional"], "new_state": "withdrawn",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("withdraw → %d", status)
	}
	if status, code := c.send(t, "transactional"); status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("post-withdrawal send → %d %q, want 409", status, code)
	}
}

// The consent gate must never be an oracle: a caller who cannot see
// the anchor gets the anchor's own refusal (404), not a consent answer.
func TestConsentGateIsNotAnOracleForUnauthorizedCallers(t *testing.T) {
	c := setupConsent(t)
	var problem struct {
		Code string `json:"code"`
	}
	status := c.call(t, "POST", "/v1/activities/00000000-0000-7000-8000-000000000001/send-email", anyMap{
		"subject": "probe", "body": "probe",
		"to": []string{"subject@consent.test"}, "consent_purpose": "transactional",
	}, nil, &problem)
	if status != http.StatusNotFound {
		t.Fatalf("send against an invisible anchor → %d %q, want 404 before any consent signal", status, problem.Code)
	}
}

func TestConsentDoubleOptInNorm(t *testing.T) {
	c := setupConsent(t)

	// marketing_email requires DOI: a bare grant is refused outright.
	var problem struct {
		Code string `json:"code"`
	}
	status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
	}, nil, &problem)
	if status != 422 {
		t.Fatalf("DOI-less marketing grant → %d, want 422", status)
	}
	// A fabricated token proves nothing: only a server-issued one confirms.
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"double_opt_in_token": "doi-token-forged",
	}, nil, nil); status != 422 {
		t.Fatalf("forged DOI grant → %d, want 422", status)
	}

	// The real round-trip: the server mints the token (the contract has
	// no mint/delivery endpoint yet, so issuance rides the store seam),
	// the confirming grant presents it, and the send flows.
	token := c.issueDOIToken(t, c.purposes["marketing_email"])
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"double_opt_in_token": token,
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("DOI grant → %d", status)
	}
	if status, code := c.send(t, "marketing_email"); status != http.StatusAccepted {
		t.Fatalf("DOI-granted send → %d %q, want 202", status, code)
	}

	// The token is single-use: after a withdrawal the consumed token
	// cannot resurrect the grant.
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "withdrawn",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("withdraw → %d", status)
	}
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"double_opt_in_token": token,
	}, nil, nil); status != 422 {
		t.Fatalf("re-grant with the consumed token → %d, want 422", status)
	}
}

// issueDOIToken mints a confirmation token over the contract surface
// (POST /people/{id}/consent/double-opt-in) as the signed-in human —
// the same call a Settings/capture surface makes before mailing the
// link out.
func (c *consentEnv) issueDOIToken(t *testing.T, purposeID string) string {
	t.Helper()
	var issued struct {
		Token     string     `json:"token"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent/double-opt-in", anyMap{
		"purpose_id": purposeID, "deliver": false,
	}, nil, &issued); status != http.StatusCreated {
		t.Fatalf("issue DOI token → %d", status)
	}
	if issued.Token == "" || issued.ExpiresAt == nil {
		t.Fatalf("DOI issuance response incomplete: %+v", issued)
	}
	return issued.Token
}

// The issuance half of the DOI round-trip (feedback/11): a purpose that
// does not require DOI refuses issuance, and a fresh token supersedes
// the unredeemed prior one — an old confirmation link in a stale mail
// can no longer confirm anything.
func TestDOIIssuanceSupersedesAndValidatesPurpose(t *testing.T) {
	c := setupConsent(t)

	// transactional does not require double opt-in → 422.
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent/double-opt-in", anyMap{
		"purpose_id": c.purposes["transactional"],
	}, nil, nil); status != 422 {
		t.Fatalf("DOI issuance for a non-DOI purpose → %d, want 422", status)
	}

	first := c.issueDOIToken(t, c.purposes["marketing_email"])
	second := c.issueDOIToken(t, c.purposes["marketing_email"])

	// The superseded first token no longer redeems…
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"double_opt_in_token": first,
	}, nil, nil); status != 422 {
		t.Fatalf("superseded token redeemed → %d, want 422", status)
	}
	// …the fresh one does.
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"double_opt_in_token": second,
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("fresh token refused")
	}

	// Issuance is an audited fact.
	var audit struct {
		Data []anyMap `json:"data"`
	}
	if status := c.call(t, "GET", "/v1/audit-log?entity_type=consent_doi_token", nil, nil, &audit); status != http.StatusOK {
		t.Fatalf("audit read → %d", status)
	}
	if len(audit.Data) != 2 {
		t.Fatalf("DOI issuances audited %d times, want exactly the 2 mints (a refused issuance writes nothing)", len(audit.Data))
	}
}

func TestConsentProofLogIsAppendOnlyAndIdempotent(t *testing.T) {
	c := setupConsent(t)
	grant := func() int {
		return c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
			"purpose_id": c.purposes["transactional"], "new_state": "granted",
		}, nil, nil)
	}
	if status := grant(); status != http.StatusOK {
		t.Fatalf("grant → %d", status)
	}
	// Re-asserting the same state is idempotent: no second proof row.
	if status := grant(); status != http.StatusOK {
		t.Fatalf("re-grant → %d", status)
	}
	var state struct {
		State []struct {
			PurposeKey string `json:"purpose_key"`
			State      string `json:"state"`
		} `json:"state"`
		Events []struct {
			NewState string `json:"new_state"`
		} `json:"events"`
	}
	if status := c.call(t, "GET", "/v1/people/"+c.personID+"/consent", nil, nil, &state); status != http.StatusOK {
		t.Fatalf("get consent → %d", status)
	}
	if len(state.Events) != 1 {
		t.Fatalf("idempotent re-grant appended a proof row: %d events", len(state.Events))
	}
	// Every tracked purpose reads back — absent ones as honest unknown.
	byKey := map[string]string{}
	for _, st := range state.State {
		byKey[st.PurposeKey] = st.State
	}
	if byKey["transactional"] != "granted" || byKey["marketing_email"] != "unknown" {
		t.Fatalf("state readback wrong: %+v", byKey)
	}
	// The consent change is audited and on the bus.
	var audits, events int
	if err := c.owner.QueryRow(t.Context(),
		`SELECT count(*) FROM audit_log WHERE action = 'consent_grant'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := c.owner.QueryRow(t.Context(),
		fmt.Sprintf(`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = '%s'`, "consent.changed")).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || events != 1 {
		t.Fatalf("audit/event counts = %d/%d, want 1/1", audits, events)
	}
}
