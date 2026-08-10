// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

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

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

type consentEnv struct {
	*apptest.AppEnv
	personID   string
	activityID string
	purposes   map[string]string // key -> id
}

func setupConsent(t *testing.T) *consentEnv {
	t.Helper()
	e := apptest.SetupApp(t)
	e.Slug = "consent-e2e"
	apptest.BootstrapWorkspaceSession(t, e, "Consent E2E", "dpo@fable.test", "Admin")

	var person struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": "Consent Subject",
		"emails":    []apptest.AnyMap{{"email": "subject@consent.test"}},
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	var activity struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "email", "subject": "Inbound question", "direction": "inbound",
		"links": []apptest.AnyMap{{"entity_type": "person", "entity_id": person.ID}},
	}, nil, &activity); status != http.StatusCreated {
		t.Fatalf("log anchor activity → %d", status)
	}

	var purposeList struct {
		Data []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/consent-purposes", nil, nil, &purposeList); status != http.StatusOK {
		t.Fatalf("list purposes → %d", status)
	}
	purposes := map[string]string{}
	for _, p := range purposeList.Data {
		purposes[p.Key] = p.ID
	}
	if purposes["transactional"] == "" || purposes["marketing_email"] == "" ||
		purposes["business_correspondence"] == "" {
		t.Fatalf("bootstrap did not seed the purpose catalog: %+v", purposeList.Data)
	}
	return &consentEnv{AppEnv: e, personID: person.ID, activityID: activity.ID, purposes: purposes}
}

func (c *consentEnv) send(t *testing.T, purpose string) (int, string) {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	status := c.Call(t, "POST", "/v1/activities/"+c.activityID+"/send-email", apptest.AnyMap{
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
	if status := c.Call(t, "POST", "/v1/activities/"+c.activityID+"/draft-email",
		apptest.AnyMap{"intent": "friendly nudge"}, nil, &draft); status != http.StatusOK {
		t.Fatalf("draft → %d", status)
	}
	if draft.Subject != "Re: Inbound question" {
		t.Fatalf("draft subject = %q", draft.Subject)
	}

	// A consent-CLASS purpose with no recorded decision is suppressed. The
	// purpose under test is marketing rather than transactional: ADR-0098
	// classes transactional and business correspondence as never
	// consent-gated, so neither can carry the default-deny claim any more.
	if status, code := c.send(t, "marketing_email"); status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("send with unknown consent → %d %q, want 409 consent_not_granted", status, code)
	}
	// An undefined purpose can authorize nothing.
	if status, code := c.send(t, "no-such-purpose"); status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("send under unknown purpose → %d %q", status, code)
	}

	// Grant marketing through the round-trip its purpose demands; the send
	// under THAT purpose then flows.
	token := c.issueDOIToken(t, c.purposes["marketing_email"])
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"lawful_basis": "consent", "double_opt_in_token": token,
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("record consent → %d", status)
	}
	if status, code := c.send(t, "marketing_email"); status != http.StatusAccepted {
		t.Fatalf("granted send → %d %q, want 202", status, code)
	}

	// Withdrawal re-blocks, and it does so through the objection rule that
	// overrides every other basis.
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "withdrawn",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("withdraw → %d", status)
	}
	if status, code := c.send(t, "marketing_email"); status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("post-withdrawal send → %d %q, want 409", status, code)
	}
}

// Answering somebody is not advertising to them (ADR-0098 D1/D2).
//
// This is the rule that ADR-0011's blanket default-deny got wrong: under it a
// rep answering an inbound question was formally a consent violation until
// somebody recorded a grant, which is legally wrong and which every rep
// correctly ignored. Correspondence is allowed on a recorded qualifying event
// and needs no consent object at all — while transactional mail, whose basis is
// the contract itself, needs neither.
func TestCorrespondenceAndTransactionalAreNotConsentGated(t *testing.T) {
	c := setupConsent(t)

	// The fixture's person wrote to us: setupConsent captures an INBOUND
	// activity from them, which is the qualifying event correspondence needs.
	if status, code := c.send(t, "transactional"); status != http.StatusAccepted {
		t.Fatalf("transactional send → %d %q, want 202 — the contract is the basis, not consent", status, code)
	}
	if status, code := c.send(t, "business_correspondence"); status != http.StatusAccepted {
		t.Fatalf("correspondence send → %d %q, want 202 — they wrote to us first", status, code)
	}
}

// An objection is absolute: Art 21(2)-(3) admits no balancing, so a withdrawal
// on the correspondence purpose outranks the qualifying event that would
// otherwise allow it. There is no override toggle, and there must be no path
// through the class model that reaches past a suppression.
func TestAnObjectionOverridesAQualifyingEvent(t *testing.T) {
	c := setupConsent(t)

	if status, _ := c.send(t, "business_correspondence"); status != http.StatusAccepted {
		t.Fatal("the fixture's inbound message should allow correspondence before the objection")
	}
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
		"purpose_id": c.purposes["business_correspondence"], "new_state": "withdrawn",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("record the objection → %d", status)
	}
	if status, code := c.send(t, "business_correspondence"); status != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("post-objection correspondence → %d %q, want 409 — an objection overrides the qualifying event", status, code)
	}
}

// The consent gate must never be an oracle: a caller who cannot see
// the anchor gets the anchor's own refusal (404), not a consent answer.
func TestConsentGateIsNotAnOracleForUnauthorizedCallers(t *testing.T) {
	c := setupConsent(t)
	var problem struct {
		Code string `json:"code"`
	}
	status := c.Call(t, "POST", "/v1/activities/00000000-0000-7000-8000-000000000001/send-email", apptest.AnyMap{
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
	status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
	}, nil, &problem)
	if status != 422 {
		t.Fatalf("DOI-less marketing grant → %d, want 422", status)
	}
	// A fabricated token proves nothing: only a server-issued one confirms.
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"double_opt_in_token": "doi-token-forged",
	}, nil, nil); status != 422 {
		t.Fatalf("forged DOI grant → %d, want 422", status)
	}

	// The real round-trip: the server mints the token (the contract has
	// no mint/delivery endpoint yet, so issuance rides the store seam),
	// the confirming grant presents it, and the send flows.
	token := c.issueDOIToken(t, c.purposes["marketing_email"])
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
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
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "withdrawn",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("withdraw → %d", status)
	}
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
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
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent/double-opt-in", apptest.AnyMap{
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
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent/double-opt-in", apptest.AnyMap{
		"purpose_id": c.purposes["transactional"],
	}, nil, nil); status != 422 {
		t.Fatalf("DOI issuance for a non-DOI purpose → %d, want 422", status)
	}

	first := c.issueDOIToken(t, c.purposes["marketing_email"])
	second := c.issueDOIToken(t, c.purposes["marketing_email"])

	// The superseded first token no longer redeems…
	if status := c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"double_opt_in_token": first,
	}, nil, nil); status != 422 {
		t.Fatalf("superseded token redeemed → %d, want 422", status)
	}
	// …the fresh one does.
	if c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
		"purpose_id": c.purposes["marketing_email"], "new_state": "granted",
		"double_opt_in_token": second,
	}, nil, nil) != http.StatusOK {
		t.Fatalf("fresh token refused")
	}

	// Issuance is an audited fact.
	var audit struct {
		Data []apptest.AnyMap `json:"data"`
	}
	if status := c.Call(t, "GET", "/v1/audit-log?entity_type=consent_doi_token", nil, nil, &audit); status != http.StatusOK {
		t.Fatalf("audit read → %d", status)
	}
	if len(audit.Data) != 2 {
		t.Fatalf("DOI issuances audited %d times, want exactly the 2 mints (a refused issuance writes nothing)", len(audit.Data))
	}
}

func TestConsentProofLogIsAppendOnlyAndIdempotent(t *testing.T) {
	c := setupConsent(t)
	grant := func() int {
		return c.Call(t, "POST", "/v1/people/"+c.personID+"/consent", apptest.AnyMap{
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
	if status := c.Call(t, "GET", "/v1/people/"+c.personID+"/consent", nil, nil, &state); status != http.StatusOK {
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
	if err := c.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM audit_log WHERE action = 'consent_grant'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := c.Owner.QueryRow(t.Context(),
		fmt.Sprintf(`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = '%s'`, "consent.changed")).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || events != 1 {
		t.Fatalf("audit/event counts = %d/%d, want 1/1", audits, events)
	}
}
