// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Org display-name derivation (ADR-0072/A118, PO-F-2a). The capture
// auto-create path must not name an organization by its raw mail eSLD —
// "gitex.com" reads as a defect where "Gitex" reads as a name. This derives a
// readable, honest name from the domain's registrable label alone; it invents
// nothing beyond capitalizing the label the domain already states. A richer
// source (a dossier's site-stated name, a corroborated signature) may later
// overwrite it, gated by organization.name_source (nameSourceDomain marks a
// name still provisional).

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// The organization.name_source provenance values (0118). Ordered weakest to
// strongest: a stronger source may overwrite a weaker one, never the reverse,
// and never 'human'.
const (
	nameSourceHuman     = "human"
	nameSourceDossier   = "dossier"
	nameSourceSignature = "signature"
	nameSourceDomain    = "domain"
)

// DisplayNameFromDomain turns a mail domain into a readable organization name
// by title-casing its registrable label: "gitex.com" → "Gitex",
// "acme-corp.co.uk" → "Acme Corp", "eu.docusign.net" → "Docusign". It uses the
// public-suffix list so a multi-label eTLD (co.uk, com.au) never leaks into the
// name. Falls back to the raw (lowercased) domain only when no registrable
// label can be found — an honest last resort, never a fabrication. The result
// is stamped with name_source='domain' by the caller: provisional, overwritable.
func DisplayNameFromDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(domain, ".")))
	if domain == "" {
		return ""
	}
	label := registrableLabel(domain)
	if label == "" {
		return domain
	}
	return titleizeLabel(label)
}

// registrableLabel returns the single label immediately left of the public
// suffix — the part a human reads as the company. "eu.docusign.net" →
// "docusign"; "acme.co.uk" → "acme". Empty when the domain is a bare public
// suffix or otherwise has no registrable label.
func registrableLabel(domain string) string {
	etldPlusOne, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		// No known suffix (a bare hostname, an intranet label): take the first
		// label as the best available name.
		if first, _, ok := strings.Cut(domain, "."); ok {
			return first
		}
		return domain
	}
	label, _, _ := strings.Cut(etldPlusOne, ".")
	return label
}

// titleizeLabel renders a registrable label as a display name: word-split on
// '-' and '_', each word capitalized. "acme-corp" → "Acme Corp". A label with
// no separators is simply capitalized ("gitex" → "Gitex").
func titleizeLabel(label string) string {
	words := strings.FieldsFunc(label, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		words[i] = capitalizeFirst(w)
	}
	if len(words) == 0 {
		return capitalizeFirst(label)
	}
	return strings.Join(words, " ")
}

// capitalizeFirst upper-cases the first rune and leaves the rest untouched.
// The input is always an already-lowercased domain label (mail domains are
// case-insensitive, so the domain's own casing carries no signal and is
// discarded at DisplayNameFromDomain's entry) — leaving the tail as-is is
// simply the cheapest correct thing, not a bid to preserve inner case.
func capitalizeFirst(w string) string {
	if w == "" {
		return ""
	}
	r := []rune(w)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
