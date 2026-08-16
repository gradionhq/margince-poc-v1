// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The page-fact lane's PEOPLE gate, split from the fact and entity gates it
// shares a call with (sitepagefacts.go). A person is the one claim whose
// truth is a relationship rather than a value: the page has to say that THIS
// name holds THIS role, and a name and a role printed near each other are
// routinely two different people's. So this gate works from a quote the model
// copies out of the page — checked verbatim, then checked for reaching over
// somebody else — rather than from where the two strings happen to sit.

import "strings"

// attributionQuote checks the model's claim that the page gives THIS person
// THIS role, and returns the page's own words that say so.
//
// The question a role gate has to answer is attribution, and attribution is
// not a distance. Earlier spellings of this check asked whether the role
// appeared near the name — inside the cited passage, then within one passage
// of it — and every version of that question has the same wrong answer on a
// page that names two people:
//
//	Geschäftsführer Anna Muster … Prokurist Bernd Beispiel
//
// "Prokurist" is near "Anna Muster" by any measure, and it is Bernd's.
// Refining the measure does not help; the information needed to tell the two
// apart is which words the page puts between them, which is what a reader
// uses and what the model is asked for here.
//
// So the model quotes the span joining role to name, and this verifies the
// quote is the page's own text, unedited. That makes the claim checkable
// without the gate having to know how German imprints, English team pages or
// Vietnamese about pages lay themselves out. A model that pads the quote to
// bridge two people is caught by the verbatim check; one that trims the
// people in between is caught by the same check, because the page does not
// read that way either.
//
// The role is held to the same bar as the name. A model that retypes it
// rather than copying it loses the person — billiger.de's reader produced
// "Geschäftsföhrer" for "Geschäftsführer" on one run in three — and that is
// the safe direction to fail, because the next read gets them and a
// near-enough match would be the gate ceasing to gate.
func attributionQuote(claim pageFactsPerson, name, role, pageText string) (string, bool) {
	quote := strings.TrimSpace(claim.Q)
	if quote == "" || !strings.Contains(pageText, quote) {
		return "", false
	}
	quoteNorm := normalizeEvidence(quote)
	if !strings.Contains(quoteNorm, normalizeEvidence(name)) ||
		!strings.Contains(quoteNorm, normalizeEvidence(role)) {
		return "", false
	}
	return quote, true
}

// reachesPastAnother answers whether an attribution quote collects its role
// from the far side of somebody the model did not declare it reaches over.
//
// A verbatim quote is honest about the words but not yet about the claim.
// Both of these are real page text:
//
//	Geschäftsführer: Dr. Thilo Gans Bernd Vermaaten   (one label, two officers)
//	Geschäftsführer Anna Muster … Prokurist Bernd     (two people, two titles)
//
// and they are the same shape — label, somebody else, the gated name. No rule
// over the characters tells them apart, and the ones tried (punctuation,
// company suffixes, passage distance, repeated words) each mistook one site's
// layout for a law.
//
// So the model declares it: alongside the quote it lists every other person
// printed inside, and an undeclared companion refuses the claim.
//
// The declaration says one specific thing — these people share the label the
// quote starts at — so it is checked against the rest of the same reply. A
// declared companion the reply gives a DIFFERENT role is a contradiction: the
// model cannot both list Anna Muster under "Geschäftsführer" for Bernd's sake
// and report her as something else, or report Bernd's own Prokurist title, so
// the misattribution has no consistent reply that survives. The officer run
// has one and it is the truth: Gans and Vermaaten, both Geschäftsführer,
// each declaring the other.
func reachesPastAnother(claim pageFactsPerson, quote, name, role string, claims []pageFactsPerson) bool {
	quoteNorm := normalizeEvidence(quote)
	selfNorm := normalizeEvidence(name)
	roleNorm := normalizeEvidence(role)
	declared := declaredCompanions(claim.W)
	for _, other := range claims {
		otherNorm := normalizeEvidence(strings.TrimSpace(other.N))
		// A name containing the gated one, or contained by it, is the same
		// person under a longer spelling ("Anna Muster-Schmidt").
		if otherNorm == "" || strings.Contains(otherNorm, selfNorm) || strings.Contains(selfNorm, otherNorm) {
			continue
		}
		if !strings.Contains(quoteNorm, otherNorm) {
			continue
		}
		if !declared[otherNorm] {
			return true
		}
		// Declared as sharing the label, so the reply must not give them a
		// different one.
		if normalizeEvidence(strings.TrimSpace(other.R)) != roleNorm {
			return true
		}
	}
	return false
}

// declaredCompanions is the set of names the model says its quote reaches
// over, normalized for comparison against the page's own spelling.
func declaredCompanions(list string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(list, ";") {
		if name := normalizeEvidence(strings.TrimSpace(part)); name != "" {
			out[name] = true
		}
	}
	return out
}

func gatePagePeople(parsed pageFactsReply, page crawlPage, idx snippetIndex, drop func(lane, field, value, reason string)) []sitePerson {
	var out []sitePerson
	personIndex := map[string]int{}
	for _, p := range parsed.People {
		name := strings.TrimSpace(p.N)
		role := strings.TrimSpace(p.R)
		if name == "" || role == "" {
			drop(lanePeople, p.N, p.R, dropEmptyValue)
			continue
		}
		// The citation still has to land: it is what ties the claim to a
		// passage this call actually showed the model, so a name lifted from
		// somewhere else cannot enter. The attribution quote below then
		// REPLACES that passage as the stored evidence, because the quote is
		// the span that shows the role belongs to this person.
		if _, namedOK := idx.nameInCited(p.E, name); !namedOK {
			drop(lanePeople, name, role, dropValueNotInSnippet)
			continue
		}
		// The page must ATTRIBUTE this role to this name, not merely print
		// the two near each other. The model quotes the words that do it and
		// the quote is checked against the page; a quote that reaches over
		// somebody else to collect a title is refused by reachesPastAnother.
		evidence, quoted := attributionQuote(p, name, role, page.Text)
		if !quoted || reachesPastAnother(p, evidence, name, role, parsed.People) {
			drop(lanePeople, name, role, dropNameRoleUnlinked)
			continue
		}
		// A lead nobody can contact is not a lead. The page has to have
		// PRINTED an address: without one the proposal asks a human to
		// confirm a name they then have no way to act on, and every one of
		// those crowds the queue that real proposals share.
		//
		// This gates what we PROPOSE, not what a lead may be — a lead
		// created by any other route may still carry no email (LEADS-DDL,
		// uq_lead_email_dedupe is partial for exactly that reason).
		publishedEmail := verbatimOrEmpty(p.M, page.Text)
		if publishedEmail == "" {
			drop(lanePeople, name, role, dropNoPublishedEmail)
			continue
		}
		person := sitePerson{
			Name:            name,
			Role:            role,
			PublishedEmail:  publishedEmail,
			LinkedinURL:     verbatimOrEmpty(p.L, page.Text),
			EvidenceSnippet: evidence,
			SourceURL:       page.URL,
			Confidence:      gatedConfidence,
		}
		key := normalizedPersonName(name)
		if prior, dup := personIndex[key]; dup {
			// Two claims for one person, and the reply's ORDER must not decide
			// which survives. The tighter evidence wins: a role stated right
			// beside the name needs fewer of the page's words than one
			// collected from further off, so the shorter quote is the closer
			// reading. That is also what settles a reply claiming somebody
			// twice — once with a borrowed title over a long span, once with
			// their own over a short one.
			if len(person.EvidenceSnippet) < len(out[prior].EvidenceSnippet) {
				drop(lanePeople, out[prior].Name, out[prior].Role, dropDuplicate)
				out[prior] = person
				continue
			}
			drop(lanePeople, name, role, dropDuplicate)
			continue
		}
		personIndex[key] = len(out)
		out = append(out, person)
	}
	return out
}
