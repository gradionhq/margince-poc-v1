// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "testing"

func TestDisplayNameFromDomain(t *testing.T) {
	cases := []struct {
		domain string
		want   string
	}{
		{"gitex.com", "Gitex"},
		{"event.gitex.com", "Gitex"},       // subdomain stripped to the registrable label
		{"eu.docusign.net", "Docusign"},    // deep subdomain
		{"acme-corp.co.uk", "Acme Corp"},   // multi-label eTLD + hyphen word-split
		{"acme_corp.com", "Acme Corp"},     // underscore word-split
		{"acme.com.au", "Acme"},            // three-label public suffix
		{"myCompany.com", "Mycompany"},     // domain case is not meaningful — normalized to lower first
		{"IBM.com", "Ibm"},                 // an acronym cannot be recovered from a case-insensitive domain
		{"", ""},                           // empty is empty
		{"  ACME.COM  ", "Acme"},           // trimmed + lowercased first
		{"localhostonly", "Localhostonly"}, // no known suffix → first label
	}
	for _, tc := range cases {
		if got := DisplayNameFromDomain(tc.domain); got != tc.want {
			t.Errorf("DisplayNameFromDomain(%q) = %q, want %q", tc.domain, got, tc.want)
		}
	}
}
