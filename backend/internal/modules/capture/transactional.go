// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The transactional / ESP suppression registry (CAP-PARAM-6, ADR-0072/A118).
// Some mail arrives over infrastructure that is not the counterparty's company:
// a DocuSign envelope from dse@eu.docusign.net, a conference blast from
// no-reply@event.gitex.com, a SendGrid relay. Naming an organization after that
// domain manufactures junk ("eu.docusign.net" as a company). This gate
// suppresses BOTH person and org derivation for such senders while KEEPING the
// activity — a DocuSign envelope is a real timeline item, it just has no CRM
// counterparty. That is the difference from the free-mail gate (CAP-PARAM-5),
// which suppresses only the org and keeps the person.
//
// Precedence is deliberate and conservative, because a false suppression hides a
// real contact:
//
//   allowlist wins → exact eSLD infra suppresses standalone →
//   subdomain-prefix rule suppresses ONLY with corroboration → otherwise keep.
//
// This is the pure decision only. The Sink runs it AFTER the correspondence-
// positive check (a known human is never suppressed) and records every
// suppression durably for observability.

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// registrableDomain normalizes a mail domain (any subdomain depth) to its
// registrable eTLD+1 via the public-suffix list: "eu.docusign.net" →
// "docusign.net", "news.acme.co.uk" → "acme.co.uk". Punycode ("xn--") labels
// pass through unchanged (already ASCII). Empty for a blank or suffix-only
// domain. This is what the exact-infra check and the allowlist key on, so a
// listing covers every subdomain of an eSLD without enumerating them.
func registrableDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(domain, ".")))
	if domain == "" {
		return ""
	}
	base, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return domain
	}
	return base
}

// transactionalBaseline is the pinned set of registrable domains (eTLD+1) that
// are mail infrastructure, never a counterparty's own company. Matched exactly
// (against the PSL-normalized eTLD+1), so every subdomain of a listed eSLD is
// covered. Additions land through the spec (CAP-PARAM-6), not ad-hoc edits; a
// deployment appends via margince.yaml capture.transactional_extra.
var transactionalBaseline = map[string]struct{}{
	"sendgrid.net":      {}, // Twilio SendGrid relay
	"sendgrid.com":      {},
	"mailgun.org":       {}, // Mailgun
	"mailgun.info":      {},
	"amazonses.com":     {}, // Amazon SES
	"mandrillapp.com":   {}, // Mailchimp Transactional
	"mcsv.net":          {}, // Mailchimp
	"mcdlv.net":         {},
	"mailchimpapp.net":  {},
	"rsgsv.net":         {}, // Mailchimp campaign
	"postmarkapp.com":   {}, // Postmark
	"sparkpostmail.com": {}, // SparkPost
	"createsend.com":    {}, // Campaign Monitor
	"cmail19.com":       {},
	"docusign.net":      {}, // DocuSign envelope infrastructure
}

// transactionalPrefixes are subdomain labels that MARK a sender subdomain as an
// email-blast lane. A prefix hit alone is NOT enough to suppress — a real
// company can live at news.acme.com — so a prefix suppresses only with
// corroboration (a List-Unsubscribe header or a machine localpart).
var transactionalPrefixes = map[string]struct{}{
	"em":      {},
	"news":    {},
	"event":   {},
	"events":  {},
	"bounce":  {},
	"bounces": {},
	"notify":  {},
	"mailer":  {},
	"mailing": {},
	"e":       {},
	"t":       {},
}

// TransactionalInput names one captured message's sender for the gate.
type TransactionalInput struct {
	// Domain is the counterparty's mail domain (any subdomain depth; lowercased
	// here). Normalized to its eTLD+1 for the exact-infra check.
	Domain string
	// Localpart is the address's local part — the machine-sender corroboration
	// for a prefix rule ("no-reply@event.acme.com").
	Localpart string
	// ListUnsubscribe reports whether the message carried a List-Unsubscribe
	// header — the primary corroboration a prefix rule needs (RFC 2369 bulk mail).
	ListUnsubscribe bool
}

// TransactionalList answers "is this sender mail infrastructure, not a
// counterparty?" against the pinned baseline plus deployment config.
type TransactionalList struct {
	extra map[string]struct{} // capture.transactional_extra additions
	never map[string]struct{} // capture.transactional_never allowlist (wins over every suppression)
}

// NewTransactionalList builds the matcher. extra appends infra eSLDs; never is
// the allowlist of registrable domains an operator declares always-legitimate
// (it wins over every suppression). Both may be nil.
func NewTransactionalList(extra, never []string) *TransactionalList {
	return &TransactionalList{
		extra: normalizedSet(extra),
		never: normalizedSet(never),
	}
}

// Suppress reports whether record creation must be suppressed for this sender,
// and a stable reason breadcrumb (recorded for observability). The activity is
// unaffected — only person/org derivation is gated.
func (l *TransactionalList) Suppress(in TransactionalInput) (bool, string) {
	base := registrableDomain(in.Domain)
	if base == "" {
		return false, ""
	}
	if _, allow := l.never[base]; allow {
		// The operator vouched for this domain — never suppress it.
		return false, ""
	}
	if _, hit := transactionalBaseline[base]; hit {
		return true, "transactional_infra:" + base
	}
	if _, hit := l.extra[base]; hit {
		return true, "transactional_infra:" + base
	}
	if prefix, ok := senderPrefix(in.Domain, base); ok {
		if _, listed := transactionalPrefixes[prefix]; listed && corroborated(in) {
			return true, "transactional_prefix:" + prefix
		}
	}
	return false, ""
}

// corroborated reports whether a prefix-rule sender carries the extra evidence a
// bare prefix cannot supply: a bulk-mail List-Unsubscribe header, or a machine
// localpart. Without it a prefix hit is not enough to suppress.
func corroborated(in TransactionalInput) bool {
	return in.ListUnsubscribe || isMachineLocalpart(in.Localpart)
}

// isMachineLocalpart flags the common no-reply / bounce local parts — the same
// tells the mail mapper drops entirely, reused here as prefix corroboration.
func isMachineLocalpart(localpart string) bool {
	local := strings.ToLower(strings.TrimSpace(localpart))
	local = strings.ReplaceAll(local, ".", "")
	local = strings.ReplaceAll(local, "-", "")
	local = strings.ReplaceAll(local, "_", "")
	switch local {
	case "noreply", "donotreply", "nreply", "mailerdaemon", "postmaster",
		"bounce", "bounces", "notifications", "notification", "mailer", "newsletter":
		return true
	}
	return false
}

// senderPrefix returns the leftmost subdomain label below the registrable
// domain — the "sender lane" a prefix rule keys on. "event.gitex.com" over base
// "gitex.com" → "event"; a bare registrable domain has no prefix.
func senderPrefix(domain, base string) (string, bool) {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(domain, ".")))
	sub := strings.TrimSuffix(domain, "."+base)
	if sub == "" || sub == domain {
		return "", false
	}
	first, _, _ := strings.Cut(sub, ".")
	return first, first != ""
}

// normalizedSet lowercases and trims a config list into a set, dropping blanks.
func normalizedSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			set[v] = struct{}{}
		}
	}
	return set
}
