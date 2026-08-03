// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Which stored facts contradict themselves.
//
// The site extractor picks a fact's FIELD from a closed per-page menu, and the
// CATEGORY is derived from the field — so an out-of-menu field cannot be
// generated at all. What it cannot prevent is the right field carrying the
// wrong KIND of value: a phone number on a contact page lands as `location`
// because a contact page's menu offers both, and a commercial-register number
// lands as `employee_range` because both are digits. The row passes every gate
// and is still wrong, and it is wrong in a way a human spots instantly.
//
// So this names the contradiction rather than trying to prevent it. It reads
// the value's SHAPE and reports why it disagrees with its field. It is
// deliberately narrow: each rule fires only on a shape that is unambiguous,
// because a false flag on a real fact teaches the reader to ignore the flag.
//
// It never hides or rewrites a fact. A heuristic that suppressed data would be
// worse than the miscategorization it is chasing — the evidence is still
// there, still cited, and a human decides.

import (
	"regexp"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

var (
	// The punctuation phone numbers actually use, and nothing else. Shape
	// alone is not enough — "(....)" matches it — so the digit COUNT is
	// checked separately below.
	phoneCharsOnly = regexp.MustCompile(`^[+()\d][\d\s()./+-]{5,}$`)
	digit          = regexp.MustCompile(`\d`)
	// A four-digit year in the range a company can plausibly have been founded.
	yearShaped = regexp.MustCompile(`^(1[5-9]\d{2}|20\d{2})$`)
	// A range, a bare count, or a count with a qualifier — "11-50", "200",
	// "500+", "ca. 40", "ca. 5.000 Mitarbeiter weltweit". The value must LEAD
	// with the number (after an optional qualifier), which is what separates a
	// headcount from a register number: "HRB 123456 B" leads with letters.
	// Length cannot separate them — a register number is short and a real
	// count phrase can be long.
	sizeShaped = regexp.MustCompile(
		`^(?i:(ca|approx|approx\.|about|etwa|rund|über|ueber|mehr als|more than|~|>|<|>=|<=)\.?\s*)?` +
			`\d[\d.,\s]*(\s*[-–—]\s*\d[\d.,\s]*)?\s*\+?` +
			`(\s+\p{L}[\p{L}.]*)*$`)
)

// phoneShaped is a value made only of phone punctuation AND carrying enough
// digits to be a number. Seven is the floor because a house number and a
// postcode together must not reach it, or every German street address would
// be flagged as a phone number.
func phoneShaped(v string) bool {
	return phoneCharsOnly.MatchString(v) && len(digit.FindAllString(v, -1)) >= 7
}

// emailShaped is an address by shape: exactly one @, a non-empty local part,
// and a domain carrying a dot with something on both sides of it. Deliberately
// not deliverability — this decides whether to WARN a reader, not whether to
// send.
func emailShaped(v string) bool {
	local, domain, found := strings.Cut(v, "@")
	if !found || local == "" || strings.Contains(domain, "@") {
		return false
	}
	name, tld, dotted := strings.Cut(domain, ".")
	return dotted && name != "" && tld != "" && !strings.HasSuffix(tld, ".")
}

// factSuspectReason reports why a fact's value contradicts its field, or an
// empty string when it does not. Pure, so the rules are testable without a
// database and readable as a list.
func factSuspectReason(field crmcontracts.OrganizationFactField, value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	switch field {
	case crmcontracts.OrganizationFactFieldLocation:
		// The one seen in the wild: a contact page's phone number filed as a
		// location, because that page's menu offers both fields.
		if phoneShaped(v) {
			return string(crmcontracts.OrganizationFactSuspectReasonPhoneShapedLocation)
		}
	case crmcontracts.OrganizationFactFieldPhone:
		if !phoneShaped(v) {
			return string(crmcontracts.OrganizationFactSuspectReasonNotAPhone)
		}
	case crmcontracts.OrganizationFactFieldFoundedYear:
		if !yearShaped.MatchString(v) {
			return string(crmcontracts.OrganizationFactSuspectReasonNotAYear)
		}
	case crmcontracts.OrganizationFactFieldContactEmail:
		// Shape only, not deliverability: one @, something either side of it,
		// and a dot INSIDE the domain. Anything stricter starts rejecting real
		// addresses; anything looser accepts "@." as one.
		if !emailShaped(v) {
			return string(crmcontracts.OrganizationFactSuspectReasonNotAnEmail)
		}
	case crmcontracts.OrganizationFactFieldEmployeeRange:
		// A register number IS digits, so digits alone cannot separate them.
		// What separates them is what the value LEADS with: a headcount leads
		// with its number or a qualifier, and "HRB 123456 B" leads with neither.
		if !sizeShaped.MatchString(v) {
			return string(crmcontracts.OrganizationFactSuspectReasonNotASize)
		}
	}
	return ""
}
