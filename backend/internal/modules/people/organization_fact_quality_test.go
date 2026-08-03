// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The rules exist because the extractor can pick the right FIELD and still
// carry the wrong KIND of value, and both halves matter: a rule that misses
// the real miscategorisation is useless, and one that fires on a good fact
// teaches the reader to ignore the flag.
func TestFactSuspectReasonNamesTheContradictionAndStaysQuietOtherwise(t *testing.T) {
	cases := []struct {
		name  string
		field crmcontracts.OrganizationFactField
		value string
		want  string
	}{
		// The one seen in the wild: a contact page offers both fields, so its
		// phone number can land as the company's location.
		{"a phone number filed as a location", crmcontracts.OrganizationFactFieldLocation, "+49 30 1234567", "phone_shaped_location"},
		{"a real street address", crmcontracts.OrganizationFactFieldLocation, "Ritterstraße 12, 10969 Berlin", ""},
		{"a bare country", crmcontracts.OrganizationFactFieldLocation, "Germany", ""},
		// A house number and a postcode together must not reach the digit
		// threshold, or every German address would be flagged.
		{"an address that is mostly digits", crmcontracts.OrganizationFactFieldLocation, "Hauptstr. 5, 80331", ""},

		{"a phone that is prose", crmcontracts.OrganizationFactFieldPhone, "call us anytime", "not_a_phone"},
		{"a phone in national format", crmcontracts.OrganizationFactFieldPhone, "030 / 1234-567", ""},

		{"a founding year that is a register number", crmcontracts.OrganizationFactFieldFoundedYear, "HRB 123456", "not_a_year"},
		{"a plausible founding year", crmcontracts.OrganizationFactFieldFoundedYear, "1998", ""},
		{"a year outside any company's life", crmcontracts.OrganizationFactFieldFoundedYear, "3025", "not_a_year"},

		{"an email with no at sign", crmcontracts.OrganizationFactFieldContactEmail, "info at scale.example", "not_an_email"},
		{"an ordinary address", crmcontracts.OrganizationFactFieldContactEmail, "info@scale.example", ""},
		// Punctuation alone satisfies "has an @ and has a dot" and is still
		// not an address by any reading.
		{"punctuation that is not an address", crmcontracts.OrganizationFactFieldContactEmail, "@.", "not_an_email"},
		{"an address with no local part", crmcontracts.OrganizationFactFieldContactEmail, "@scale.example", "not_an_email"},
		{"an address with no dot in the domain", crmcontracts.OrganizationFactFieldContactEmail, "info@localhost", "not_an_email"},
		{"an address with two at signs", crmcontracts.OrganizationFactFieldContactEmail, "a@b@scale.example", "not_an_email"},
		{"a plus-tagged address", crmcontracts.OrganizationFactFieldContactEmail, "sales+eu@scale.example", ""},

		// A register number IS digits, so digits alone cannot separate it from
		// a headcount — what does is what the value leads with.
		{"a register number filed as a headcount", crmcontracts.OrganizationFactFieldEmployeeRange, "HRB 123456 B, Amtsgericht Berlin", "not_a_size"},
		// Short enough to pass a length rule, and still a register number.
		{"a short register number", crmcontracts.OrganizationFactFieldEmployeeRange, "HRB 123456", "not_a_size"},
		{"a band", crmcontracts.OrganizationFactFieldEmployeeRange, "11-50", ""},
		{"an open-ended count", crmcontracts.OrganizationFactFieldEmployeeRange, "500+", ""},
		{"a qualified count", crmcontracts.OrganizationFactFieldEmployeeRange, "ca. 40", ""},
		// Long enough to fail a length rule, and still an honest headcount.
		{"a spelled-out count phrase", crmcontracts.OrganizationFactFieldEmployeeRange, "ca. 5.000 Mitarbeiter weltweit", ""},
		{"a headcount with no digits at all", crmcontracts.OrganizationFactFieldEmployeeRange, "several dozen", "not_a_size"},

		// Fields with no rule stay silent rather than guessing.
		{"a service, which has no shape to check", crmcontracts.OrganizationFactFieldService, "Managed hosting", ""},
		// Phone punctuation with no digits behind it is not a phone number,
		// and must not drag a location into the flag.
		{"punctuation that is not a number", crmcontracts.OrganizationFactFieldLocation, "(.....)", ""},
		{"a short internal extension is not enough", crmcontracts.OrganizationFactFieldLocation, "12-345", ""},
		{"an empty value is nothing to judge", crmcontracts.OrganizationFactFieldPhone, "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := factSuspectReason(tc.field, tc.value); got != tc.want {
				t.Errorf("factSuspectReason(%s, %q) = %q, want %q", tc.field, tc.value, got, tc.want)
			}
		})
	}
}
