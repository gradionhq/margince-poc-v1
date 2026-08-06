// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The internal-vs-external decision (ADR-0082/A127, formulas §20), in one
// place for mail and calendar alike. The rule that was correct for calendar
// and absent for mail is how colleague correspondence came to be captured and
// read by the whole workspace; there is one implementation now, and both
// channels ask it the same question.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/idna"
)

// NormalizeDomain folds a mail domain to the one form the own-domain set is
// compared in: lowercased, trailing dot stripped, IDNA-encoded. A domain that
// fails IDNA is returned lowercased rather than dropped — the caller is deciding
// whether to KEEP a message, and discarding an unreadable domain here would
// silently turn a parse failure into "internal", which is the one answer that
// loses correspondence rather than keeping it.
func NormalizeDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" {
		return ""
	}
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return domain
	}
	return ascii
}

// DomainOfAddress returns the normalized domain of a mail address, or "" when
// the address carries none that can be read. An address with no readable domain
// is not internal (see InternalDomains.Covers).
func DomainOfAddress(address string) string {
	address = strings.TrimSpace(address)
	at := strings.LastIndex(address, "@")
	if at < 0 || at == len(address)-1 {
		return ""
	}
	return NormalizeDomain(address[at+1:])
}

// InternalDomains is the workspace's own mail domains, normalized once so a
// membership test is a comparison rather than a query.
type InternalDomains struct {
	domains []string
}

// NewInternalDomains normalizes and de-duplicates the registered domains.
func NewInternalDomains(raw []string) InternalDomains {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, d := range raw {
		n := NormalizeDomain(d)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return InternalDomains{domains: out}
}

// Empty reports whether the workspace has registered no own domain at all.
//
// An empty set makes NOTHING internal, so every message is captured. That is
// the honest posture rather than a fallback guess: an installation that has
// named no domain of its own is making no claim about what its people's mail
// is, and inventing one from a connected mailbox would be right in some
// workspaces and wrong in the rest.
func (d InternalDomains) Empty() bool { return len(d.domains) == 0 }

// Covers reports whether address belongs to one of the workspace's own domains.
//
// A registered domain covers its SUBDOMAINS: acme.com covers mail.acme.com.
// Exact string equality was the failure mode with teeth — it leaks the internal
// mail of every company that sends from a subdomain, and it looks exactly like
// working correctly. The suffix test includes the separating dot so acme.com
// does not cover the unrelated acme.com.example.net.
//
// An address with no readable domain is NOT covered, so the message is kept.
func (d InternalDomains) Covers(address string) bool {
	domain := DomainOfAddress(address)
	if domain == "" {
		return false
	}
	for _, own := range d.domains {
		if domain == own || strings.HasSuffix(domain, "."+own) {
			return true
		}
	}
	return false
}

// AllInternal reports whether every one of these addresses is on an own domain
// — the zero-rows condition (formulas §20).
//
// False when the set is empty, and false when there are no addresses to judge:
// both are "we cannot say this is internal", and the direction to fail in is
// toward keeping a message. One external participant makes the whole message
// external, which is what keeps the intro motion working — a colleague writing
// to a prospect with the prospect copied is correspondence, not chatter.
func (d InternalDomains) AllInternal(addresses []string) bool {
	if d.Empty() {
		return false
	}
	judged := 0
	for _, a := range addresses {
		if strings.TrimSpace(a) == "" {
			continue
		}
		if !d.Covers(a) {
			return false
		}
		judged++
	}
	return judged > 0
}

// External returns the addresses that are NOT on an own domain, in the order
// given and de-duplicated — the parties a captured message may create records
// for. It is deliberately separate from the message's author: which party WROTE
// a message and which parties are candidates for a record are two questions,
// and answering the second with the first records a prospect as the author of a
// colleague's mail (ADR-0082 §3).
func (d InternalDomains) External(addresses []string) []string {
	seen := make(map[string]bool, len(addresses))
	out := make([]string, 0, len(addresses))
	for _, a := range addresses {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] || d.Covers(a) {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// ownDomainsTx reads the workspace's registered mail domains on the capture
// transaction, so the internal decision and the write it governs see one
// consistent set.
func ownDomainsTx(ctx context.Context, tx pgx.Tx) (InternalDomains, error) {
	rows, err := tx.Query(ctx, `SELECT domain FROM workspace_email_domain`)
	if err != nil {
		return InternalDomains{}, fmt.Errorf("capture: reading the own-domain set: %w", err)
	}
	defer rows.Close()

	var raw []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return InternalDomains{}, fmt.Errorf("capture: reading the own-domain set: %w", err)
		}
		raw = append(raw, d)
	}
	if err := rows.Err(); err != nil {
		return InternalDomains{}, fmt.Errorf("capture: reading the own-domain set: %w", err)
	}
	return NewInternalDomains(raw), nil
}
