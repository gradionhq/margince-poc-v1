// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// sign reproduces HubSpot's v3 basis so a test can present a valid signature.
func sign(secret, method, uri string, body []byte, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(method))
	mac.Write([]byte(uri))
	mac.Write(body)
	mac.Write([]byte(ts))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature(t *testing.T) {
	const secret, method, uri, ts = "app-secret", "POST", "https://x.example/webhooks/hubspot", "1700000000000"
	body := []byte(`[{"portalId":123}]`)
	good := sign(secret, method, uri, body, ts)

	if !VerifyWebhookSignature(secret, method, uri, body, ts, good) {
		t.Error("a correctly-signed request must verify")
	}
	// Any deviation in secret, body, uri, or timestamp breaks the HMAC.
	if VerifyWebhookSignature("wrong-secret", method, uri, body, ts, good) {
		t.Error("a wrong app secret must not verify")
	}
	if VerifyWebhookSignature(secret, method, uri, []byte(`[{"portalId":999}]`), ts, good) {
		t.Error("a tampered body must not verify")
	}
	if VerifyWebhookSignature(secret, method, uri, body, "1700000009999", good) {
		t.Error("a swapped timestamp must not verify")
	}
	if VerifyWebhookSignature(secret, method, uri, body, ts, "") {
		t.Error("an empty signature must not verify")
	}
	if VerifyWebhookSignature("", method, uri, body, ts, good) {
		t.Error("an unconfigured secret must not verify (fail-closed)")
	}
}

func TestObjectClassForSubscription(t *testing.T) {
	cases := map[string]string{
		"contact.propertyChange": objectClassContacts,
		"company.creation":       objectClassCompanies,
		"deal.creation":          objectClassDeals,
		"lead.propertyChange":    objectClassLeads,
		"hs_lead.creation":       objectClassLeads,
	}
	for sub, want := range cases {
		got, ok := ObjectClassForSubscription(sub)
		if !ok || got != want {
			t.Errorf("ObjectClassForSubscription(%q) = (%q, %v), want (%q, true)", sub, got, ok, want)
		}
	}
	if _, ok := ObjectClassForSubscription("ticket.creation"); ok {
		t.Error("an unmodeled subscription type must be ok=false (dropped, not guessed)")
	}
	// A deletion is NOT re-fetchable through this lane — the poller's deletion
	// feed tombstones it; presenting it as handled would enqueue a doomed Get.
	if _, ok := ObjectClassForSubscription("deal.deletion"); ok {
		t.Error("a deletion subscription must be ok=false (poller deletion feed owns it, not a re-fetch)")
	}
	if _, ok := ObjectClassForSubscription("contact.deletion"); ok {
		t.Error("a contact deletion must be ok=false too (dropped from the re-fetch lane)")
	}
}
