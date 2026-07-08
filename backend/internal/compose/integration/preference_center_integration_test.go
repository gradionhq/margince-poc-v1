// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Buyer preference center + RFC 8058 one-click unsubscribe (B-E11.32) end
// to end over the real handler stack: a marketing send carries the
// List-Unsubscribe header pair built from the CONFIGURED base (never the
// request Host), the no-login token surface recognizes the recipient, a
// one-click POST withdraws immediately through the consent write shape and
// the default-deny gate honors it on the very next send, a GET never
// withdraws, an unknown token reads as absent, and the surface is
// throttled. The withdrawal carries a distinct provenance and is audited +
// emitted like every other consent change.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// sendMarketing issues an authenticated send-email and returns the status
// plus response headers, so the List-Unsubscribe header can be asserted.
// host/xfProto let a test forge the request origin to prove the emitted
// link ignores them.
func sendMarketing(t *testing.T, e *env, activityID, purpose, host, xfProto string) (int, http.Header) {
	t.Helper()
	raw, err := json.Marshal(anyMap{
		"subject": "Newsletter", "body": "hello", "to": []string{"subject@consent.test"}, "consent_purpose": purpose,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", e.ts.URL+"/v1/activities/"+activityID+"/send-email", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-Slug", e.slug)
	// The forgeable request-origin signals a proxied deployment would carry:
	// the send must ignore ALL of them and use the configured base, or the
	// tokenized link could be pointed at an attacker's domain.
	if host != "" {
		req.Header.Set("X-Forwarded-Host", host)
		req.Header.Set("Host", host)
	}
	if xfProto != "" {
		req.Header.Set("X-Forwarded-Proto", xfProto)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("send-email: %v", err)
	}
	defer closeBody(t, resp)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header
}

// tokenFromHeader lifts the preference token out of a List-Unsubscribe
// header value: `<https://base/v1/public/preferences/TOKEN/unsubscribe?…>`.
func tokenFromHeader(t *testing.T, header string) string {
	t.Helper()
	trimmed := strings.TrimSuffix(strings.TrimPrefix(header, "<"), ">")
	u, err := url.Parse(trimmed)
	if err != nil {
		t.Fatalf("parsing List-Unsubscribe %q: %v", header, err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/v1/public/preferences/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		t.Fatalf("List-Unsubscribe path has no token: %q", u.Path)
	}
	return parts[0]
}

func grantPurpose(t *testing.T, c *consentEnv, purposeID string) {
	t.Helper()
	if status := c.call(t, "POST", "/v1/people/"+c.personID+"/consent", anyMap{
		"purpose_id": purposeID, "new_state": "granted", "lawful_basis": "consent",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("grant %s → %d", purposeID, status)
	}
}

// createNewsletterPurpose creates the non-DOI newsletter marketing
// purpose (so a grant needs no round-trip) and returns its id.
func createNewsletterPurpose(t *testing.T, c *consentEnv) string {
	t.Helper()
	var newsletter struct {
		ID string `json:"id"`
	}
	if status := c.call(t, "POST", "/v1/consent-purposes", anyMap{
		"key": "newsletter", "label": "Newsletter", "requires_double_opt_in": false,
	}, nil, &newsletter); status != http.StatusCreated {
		t.Fatalf("create newsletter purpose → %d", status)
	}
	return newsletter.ID
}

// sendAndAssertUnsubscribeHeaders covers the RFC 8058 header pair on a
// marketing send: both headers present and built from the CONFIGURED
// base, a forged request origin never reshapes the tokenized link, and
// a transactional (locked) send carries no unsubscribe header at all.
// Returns the preference token the send minted.
func sendAndAssertUnsubscribeHeaders(t *testing.T, c *consentEnv) string {
	t.Helper()
	// The marketing send carries both RFC 8058 headers, built from the
	// configured base — NOT from the request Host.
	status, hdr := sendMarketing(t, c.env, c.activityID, "newsletter", "", "")
	if status != http.StatusAccepted {
		t.Fatalf("marketing send → %d, want 202", status)
	}
	if got := hdr.Get("List-Unsubscribe-Post"); got != "List-Unsubscribe=One-Click" {
		t.Fatalf("List-Unsubscribe-Post = %q", got)
	}
	lu := hdr.Get("List-Unsubscribe")
	if !strings.HasPrefix(lu, "<https://mail.example.test/v1/public/preferences/") || !strings.Contains(lu, "purpose=newsletter") {
		t.Fatalf("List-Unsubscribe = %q, want a one-click URL on the configured base", lu)
	}
	token := tokenFromHeader(t, lu)

	// A forged Host / X-Forwarded-Proto must NOT redirect the tokenized
	// link to an attacker's domain (token-exfiltration guard).
	_, hostileHdr := sendMarketing(t, c.env, c.activityID, "newsletter", "evil.example.com", "http")
	hostileLU := hostileHdr.Get("List-Unsubscribe")
	if !strings.HasPrefix(hostileLU, "<https://mail.example.test/") || strings.Contains(hostileLU, "evil.example") {
		t.Fatalf("hostile Host reshaped the unsubscribe link: %q", hostileLU)
	}

	// A transactional (locked) send carries NO unsubscribe header.
	if _, thdr := sendMarketing(t, c.env, c.activityID, "transactional", "", ""); thdr.Get("List-Unsubscribe") != "" {
		t.Fatal("transactional send carried a List-Unsubscribe header — there is nothing to unsubscribe from")
	}
	return token
}

// prefView is the no-login preference center's response shape.
type prefView struct {
	Purposes []struct {
		Key    string `json:"key"`
		State  string `json:"state"`
		Locked bool   `json:"locked"`
	} `json:"purposes"`
}

// readPreferenceView fetches the token's preference center view.
func readPreferenceView(t *testing.T, c *consentEnv, token string) prefView {
	t.Helper()
	var v prefView
	if s := publicCall(t, c.env, "GET", "/v1/public/preferences/"+token, nil, nil, &v); s != http.StatusOK {
		t.Fatalf("preference center GET → %d", s)
	}
	return v
}

// purposeStateOf resolves one purpose's (state, locked) from the view.
func purposeStateOf(t *testing.T, v prefView, key string) (string, bool) {
	t.Helper()
	for _, p := range v.Purposes {
		if p.Key == key {
			return p.State, p.Locked
		}
	}
	t.Fatalf("purpose %q missing from the preference center", key)
	return "", false
}

// assertWithdrawalProvenanceAndWriteShape checks the one-click
// withdrawal's proof rows: idempotent on replay (one proof row), a
// distinct provenance (the preference center + the confined public
// principal — never `manual`, never a workspace user), and the standard
// audited + emitted write shape.
func assertWithdrawalProvenanceAndWriteShape(t *testing.T, c *consentEnv, token, newsletterID string) {
	t.Helper()
	// Idempotent: a repeat one-click writes no second proof row.
	if s := publicCall(t, c.env, "POST", "/v1/public/preferences/"+token+"/unsubscribe?purpose=newsletter", nil, nil, nil); s != http.StatusOK {
		t.Fatalf("idempotent unsubscribe → %d", s)
	}
	var withdrawEvents int
	if err := c.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM consent_event WHERE new_state = 'withdrawn' AND purpose_id = $1`, newsletterID).Scan(&withdrawEvents); err != nil {
		t.Fatal(err)
	}
	if withdrawEvents != 1 {
		t.Fatalf("newsletter withdrawal wrote %d proof rows, want 1 (idempotent)", withdrawEvents)
	}

	// The withdrawal's provenance is the preference center + the confined
	// public principal — never `manual`, never a workspace user.
	var source, capturedBy, actorType string
	if err := c.owner.QueryRow(context.Background(), `
		SELECT ce.source, ce.captured_by,
		       (SELECT actor_type FROM audit_log WHERE action = 'consent_withdraw' LIMIT 1)
		FROM consent_event ce WHERE ce.new_state = 'withdrawn' AND ce.purpose_id = $1 LIMIT 1`,
		newsletterID).Scan(&source, &capturedBy, &actorType); err != nil {
		t.Fatal(err)
	}
	if source != "preference_center" || capturedBy != "system:public_preferences" || actorType != "system" {
		t.Fatalf("withdrawal provenance = source %q by %q (audit actor %q), want preference_center / system:public_preferences / system",
			source, capturedBy, actorType)
	}

	// The withdrawal rides the standard write shape: audited + emitted.
	var audits, events int
	if err := c.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action = 'consent_withdraw'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := c.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'consent.changed'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if audits < 1 || events < 1 {
		t.Fatalf("write shape incomplete: %d withdraw audits, %d consent.changed events", audits, events)
	}
}

func TestPreferenceCenterOneClickUnsubscribe(t *testing.T) {
	c := setupConsent(t)

	// A live deal's transactional lane stays open throughout.
	grantPurpose(t, c, c.purposes["transactional"])

	newsletterID := createNewsletterPurpose(t, c)
	grantPurpose(t, c, newsletterID)

	token := sendAndAssertUnsubscribeHeaders(t, c)

	// The no-login preference center recognizes the recipient by token.
	view := readPreferenceView(t, c, token)
	if s, _ := purposeStateOf(t, view, "newsletter"); s != "granted" {
		t.Fatalf("newsletter shows %q before opt-out, want granted", s)
	}
	if s, locked := purposeStateOf(t, view, "transactional"); s != "granted" || !locked {
		t.Fatalf("transactional shows state=%q locked=%v, want granted+locked", s, locked)
	}

	// A GET/prefetch on the unsubscribe path must NOT withdraw (RFC 8058
	// mandates POST for exactly this — a scanner following the link must
	// not opt anyone out).
	if s := publicCall(t, c.env, "GET", "/v1/public/preferences/"+token+"/unsubscribe", nil, nil, nil); s != http.StatusMethodNotAllowed {
		t.Fatalf("GET on the unsubscribe path → %d, want 405", s)
	}
	if s, _ := purposeStateOf(t, readPreferenceView(t, c, token), "newsletter"); s != "granted" {
		t.Fatalf("a GET changed newsletter to %q — the one-click surface must be POST-only", s)
	}

	// The one-click POST withdraws immediately.
	var unsub struct {
		Unsubscribed []string `json:"unsubscribed"`
	}
	if s := publicCall(t, c.env, "POST", "/v1/public/preferences/"+token+"/unsubscribe?purpose=newsletter", nil, nil, &unsub); s != http.StatusOK {
		t.Fatalf("one-click unsubscribe → %d", s)
	}
	if len(unsub.Unsubscribed) != 1 || unsub.Unsubscribed[0] != "newsletter" {
		t.Fatalf("unsubscribed = %v, want [newsletter]", unsub.Unsubscribed)
	}
	if s, _ := purposeStateOf(t, readPreferenceView(t, c, token), "newsletter"); s != "withdrawn" {
		t.Fatalf("newsletter still %q after one-click, want withdrawn", s)
	}

	// The gate honors the opt-out on the very next send; transactional
	// (the live deal's lane) still transmits.
	if s, code := c.send(t, "newsletter"); s != http.StatusConflict || code != "consent_not_granted" {
		t.Fatalf("marketing send after opt-out → %d %q, want 409 consent_not_granted", s, code)
	}
	if s, code := c.send(t, "transactional"); s != http.StatusAccepted {
		t.Fatalf("transactional send after marketing opt-out → %d %q, want 202", s, code)
	}

	assertWithdrawalProvenanceAndWriteShape(t, c, token, newsletterID)
}

// The token is required and single-purpose: an unknown or revoked token is
// refused as absent, so probing cannot tell a real recipient from a
// fabricated one (the surface is not a consent-state oracle).
func TestPreferenceCenterTokenGuards(t *testing.T) {
	c := setupConsent(t)

	if s := publicCall(t, c.env, "GET", "/v1/public/preferences/pref_does_not_exist", nil, nil, nil); s != http.StatusNotFound {
		t.Fatalf("unknown token GET → %d, want 404", s)
	}
	if s := publicCall(t, c.env, "POST", "/v1/public/preferences/pref_does_not_exist/unsubscribe?purpose=newsletter", nil, nil, nil); s != http.StatusNotFound {
		t.Fatalf("unknown token unsubscribe → %d, want 404", s)
	}
	if s := publicCall(t, c.env, "GET", "/v1/public/preferences/", nil, nil, nil); s != http.StatusNotFound {
		t.Fatalf("empty token → %d, want 404", s)
	}
}

// A REVOKED token reads identically to an unknown one (404), so revoking
// a recipient's link cannot be turned into a "this person exists" oracle.
func TestPreferenceCenterRevokedTokenReadsAsAbsent(t *testing.T) {
	c := setupConsent(t)

	var newsletter struct {
		ID string `json:"id"`
	}
	if status := c.call(t, "POST", "/v1/consent-purposes", anyMap{
		"key": "newsletter", "label": "Newsletter", "requires_double_opt_in": false,
	}, nil, &newsletter); status != http.StatusCreated {
		t.Fatalf("create newsletter purpose → %d", status)
	}
	grantPurpose(t, c, newsletter.ID)

	_, hdr := sendMarketing(t, c.env, c.activityID, "newsletter", "", "")
	token := tokenFromHeader(t, hdr.Get("List-Unsubscribe"))

	// Live token resolves.
	if s := publicCall(t, c.env, "GET", "/v1/public/preferences/"+token, nil, nil, nil); s != http.StatusOK {
		t.Fatalf("live token GET → %d, want 200", s)
	}

	if _, err := c.owner.Exec(context.Background(),
		`UPDATE preference_token SET revoked_at = now() WHERE token = $1`, token); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	// Revoked → 404, indistinguishable from an unknown token.
	if s := publicCall(t, c.env, "GET", "/v1/public/preferences/"+token, nil, nil, nil); s != http.StatusNotFound {
		t.Fatalf("revoked token GET → %d, want 404 (must read as absent)", s)
	}
	if s := publicCall(t, c.env, "POST", "/v1/public/preferences/"+token+"/unsubscribe?purpose=newsletter", nil, nil, nil); s != http.StatusNotFound {
		t.Fatalf("revoked token unsubscribe → %d, want 404", s)
	}
}

// A granular save carrying more choices than there are tracked purposes is
// refused (422) before the per-choice loop, so a valid token cannot amplify
// one body into tens of thousands of serial transactions.
func TestPreferenceCenterRejectsOversizedChoiceArray(t *testing.T) {
	c := setupConsent(t)

	var newsletter struct {
		ID string `json:"id"`
	}
	if status := c.call(t, "POST", "/v1/consent-purposes", anyMap{
		"key": "newsletter", "label": "Newsletter", "requires_double_opt_in": false,
	}, nil, &newsletter); status != http.StatusCreated {
		t.Fatalf("create newsletter purpose → %d", status)
	}
	grantPurpose(t, c, newsletter.ID)
	_, hdr := sendMarketing(t, c.env, c.activityID, "newsletter", "", "")
	token := tokenFromHeader(t, hdr.Get("List-Unsubscribe"))

	choices := make([]anyMap, 0, 100)
	for i := 0; i < 100; i++ {
		choices = append(choices, anyMap{"purpose_key": "newsletter", "state": "withdrawn"})
	}
	if s := publicCall(t, c.env, "PUT", "/v1/public/preferences/"+token, anyMap{"choices": choices}, nil, nil); s != http.StatusUnprocessableEntity {
		t.Fatalf("oversized choices PUT → %d, want 422", s)
	}
}

// The anonymous surface is throttled per token: a flood of one-click POSTs
// meets 429 before it can hammer the consent engine.
func TestPreferenceCenterRateLimited(t *testing.T) {
	c := setupConsent(t)

	last := 0
	for i := 0; i < 21; i++ {
		last = publicCall(t, c.env, "POST", "/v1/public/preferences/pref_flood_probe/unsubscribe?purpose=newsletter", nil, nil, nil)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("21st burst unsubscribe → %d, want 429", last)
	}
}
