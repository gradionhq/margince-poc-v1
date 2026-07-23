// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
)

// This file is the HubSpot webhook protocol: the v3 request-signature check
// and the minimal invalidation-signal payload the overlay receiver
// (OVA-WIRE-10) consumes. It lives in the hubspot package because it is
// HubSpot's own wire format — the compose receiver stays incumbent-agnostic
// about the protocol, the same split the read/write adapters keep.

// WebhookEvent is one HubSpot webhook notification — the minimal invalidation
// signal, NOT trusted content (OVA-WIRE-10). portalId binds the tenant,
// subscriptionType selects the object class, objectId names the record.
// occurredAt orders the change (epoch millis).
type WebhookEvent struct {
	PortalID         int64  `json:"portalId"`         //nolint:tagliatelle // HubSpot's wire format (camelCase)
	ObjectID         int64  `json:"objectId"`         //nolint:tagliatelle // HubSpot's wire format (camelCase)
	SubscriptionType string `json:"subscriptionType"` //nolint:tagliatelle // HubSpot's wire format (camelCase)
	OccurredAt       int64  `json:"occurredAt"`       //nolint:tagliatelle // HubSpot's wire format (camelCase)
}

// PortalIDString renders the portalId as the decimal string the connection's
// incumbent_account_id (OVA-DDL-3) is stored as, for the tenant binding.
func (e WebhookEvent) PortalIDString() string { return strconv.FormatInt(e.PortalID, 10) }

// ObjectIDString renders the objectId as the decimal-string external id the
// mirror keys records by.
func (e WebhookEvent) ObjectIDString() string { return strconv.FormatInt(e.ObjectID, 10) }

// VerifyWebhookSignature checks HubSpot's v3 request signature: the header is
// base64(HMAC-SHA256(clientSecret, method + uri + body + timestamp)). It is a
// constant-time compare — a mismatch means the request is not from HubSpot (or
// was tampered) and is rejected fail-closed. The caller separately rejects a
// stale timestamp (replay).
func VerifyWebhookSignature(clientSecret, method, uri string, body []byte, timestamp, provided string) bool {
	if clientSecret == "" || provided == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(clientSecret))
	mac.Write([]byte(method))
	mac.Write([]byte(uri))
	mac.Write(body)
	mac.Write([]byte(timestamp))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	// hmac.Equal is constant-time; both operands are fixed-length base64 of a
	// 32-byte digest, so it leaks neither content nor length.
	return hmac.Equal([]byte(expected), []byte(provided))
}

// HubSpot's webhook subscriptionType vocabulary: the object-type prefix before
// the first dot, and the actions we act on. These are HubSpot's OWN wire names,
// deliberately kept distinct from the canonical *Target constants
// (mapping_hs.go) even where the spelling coincides — a future rename of a
// canonical Margince type must never silently drop webhook routing, so the two
// domains never share a constant.
const (
	subPrefixContact = "contact"
	subPrefixCompany = "company"
	subPrefixDeal    = "deal"
	subPrefixLead    = "lead"
	subPrefixHSLead  = "hs_lead"
	// subActionDeletion is the one action this signal lane deliberately does
	// NOT act on: a re-fetch of a deleted record 404s and would leave the
	// mirror row lingering (a doomed job that looks handled). Incumbent-side
	// deletions are the poller's deletion-feed's job (it tombstones), so a
	// deletion subscription is dropped here rather than enqueued.
	subActionDeletion = "deletion"
)

// ObjectClassForSubscription maps a HubSpot subscriptionType (e.g.
// "contact.propertyChange", "deal.creation") to the incumbent object class the
// mirror re-fetches through — the prefix before the first dot names the object
// type. ok=false means "not re-fetchable through this lane": either an
// unrecognized object type (a type the mirror does not model — dropped, never
// guessed) OR a deletion action (owned by the poller's deletion feed, not a
// re-fetch). V1 covers the object types HubSpot webhooks deliver for
// (contact/company/deal/lead); the five engagement classes have no standard
// webhook subscription and are healed by the poller.
func ObjectClassForSubscription(subscriptionType string) (string, bool) {
	prefix, action, _ := strings.Cut(subscriptionType, ".")
	if action == subActionDeletion {
		// Presented honestly as NOT handled by the re-fetch lane; the poller's
		// deletion feed tombstones incumbent-deleted records.
		return "", false
	}
	switch prefix {
	case subPrefixContact:
		return objectClassContacts, true
	case subPrefixCompany:
		return objectClassCompanies, true
	case subPrefixDeal:
		return objectClassDeals, true
	case subPrefixLead, subPrefixHSLead:
		return objectClassLeads, true
	default:
		return "", false
	}
}
