// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The sender-name heuristic: does this domain's own label say it belongs to a
// person rather than a company? It answers only when the triage crawl could not
// — an unreachable site, a domain that resolves to nothing — because a read
// site is always better evidence than a name coincidence.
//
// It is the last line before falling back to creating a company. Getting it
// wrong in one direction costs a real one-person consultancy its organization
// row; in the other it manufactures a company named after a human. So the rule
// is deliberately narrow: the domain's registrable label must BE somebody's
// name, spelled the way people spell their own domains.

import "strings"

// DomainPerson is one human the workspace already records on a domain: their
// name as captured, and the local part of the address they wrote from. Both
// matter — "Phu Nguyen" writing from richard@richardnguyen.me is only explained
// by joining the local part to the surname.
type DomainPerson struct {
	FullName   string
	EmailLocal string
}

// domainPersonalSimilarity is how close a spelling has to be to a name before
// it counts as that name. High on purpose: "steireif" IS Alexander Steireif's
// surname and that domain is a real agency, so the heuristic must not also
// start matching near-misses — it already stands on thin evidence.
const domainPersonalSimilarity = 0.94

// DomainLooksPersonal reports whether the domain's registrable label is a
// natural person's name. label is that label alone ("herpertz" from
// herpertz.net), not the full domain.
//
// The answer is yes only when EVERY known person on the domain is explained by
// it. One person whose name is the domain is a personal domain; two people with
// different family names sharing it is a company however well the first one
// matches, because a company is exactly what a shared domain is.
//
// With nobody known it answers no: there is then no name to compare, and
// refusing an organization on no evidence at all would be a guess.
func DomainLooksPersonal(label string, persons []DomainPerson) bool {
	label = normalizeName(strings.Join(strings.FieldsFunc(label, isNameSeparator), ""))
	if label == "" || len(persons) == 0 {
		return false
	}
	for _, p := range persons {
		if !personExplainsLabel(label, p) {
			return false
		}
	}
	return true
}

// personExplainsLabel reports whether this one human's name accounts for the
// domain label, in any of the spellings people actually register: the surname
// alone, the full name run together either way round, or — the case a header
// display name alone never explains — the address's own local part joined to
// the surname.
func personExplainsLabel(label string, p DomainPerson) bool {
	for _, candidate := range personLabelCandidates(p) {
		if candidate == "" {
			continue
		}
		if candidate == label || jaroWinkler(candidate, label) >= domainPersonalSimilarity {
			return true
		}
	}
	return false
}

// personLabelCandidates spells one person's name every way a domain label
// plausibly renders it. Order does not matter; the caller takes any match.
//
// The local part NEVER stands alone as a candidate. A role address on its own
// domain — ffpv@ffpv.de — would otherwise read as a personal domain, when a
// mailbox named after the company is if anything the opposite signal. It counts
// only joined to a name part, which is the case a display name cannot explain
// by itself.
func personLabelCandidates(p DomainPerson) []string {
	parts := strings.Fields(nameWithoutAffiliation(p.FullName))
	// A one-word name is not a person's name, it is a label — `"Acme"
	// <info@acme.example>` is how a company signs its own mail. Matching it
	// against the domain would read the company's own name as a human's and
	// refuse the company its record.
	if len(parts) < 2 {
		return nil
	}
	first, last := parts[0], parts[len(parts)-1]
	out := []string{last, first + last, last + first}
	if local := normalizeName(strings.Join(strings.FieldsFunc(p.EmailLocal, isNameSeparator), "")); local != "" {
		// richard@richardnguyen.me against "Phu Nguyen": the address carries
		// the given name the header does not.
		out = append(out, local+last, last+local)
	}
	return out
}

// nameWithoutAffiliation keeps the part of a display name that is the person,
// dropping the employer people staple onto it — "Guido Frings - FFPV",
// "Stergios Gaidatzis (Cloud Motion)", "Ed Sander @ ChinaTechTrip". Without
// this the company reads as the surname, and a domain named after the COMPANY
// would then look like it was named after the person.
func nameWithoutAffiliation(name string) string {
	normalized := normalizeName(name)
	for _, sep := range []string{" - ", " – ", " — ", " | ", " • ", " · ", " @ ", "(", ",", "/"} {
		if head, _, found := strings.Cut(normalized, sep); found {
			normalized = strings.TrimSpace(head)
		}
	}
	return normalized
}

// isNameSeparator splits the punctuation a domain label or a mail local part
// uses where a name has a space: "acme-corp", "first.last", "first_last".
func isNameSeparator(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '+' }
