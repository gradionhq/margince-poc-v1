// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "testing"

func TestTransactionalListSuppress(t *testing.T) {
	// extra adds one infra eSLD; never allowlists a domain that WOULD otherwise
	// be suppressed by a corroborated prefix rule.
	list := NewTransactionalList([]string{"customship.io"}, []string{"news.myrealbrand.com", "myrealbrand.com"})

	cases := []struct {
		name         string
		in           TransactionalInput
		wantSuppress bool
		wantReason   string
	}{
		{
			name:         "exact infra eSLD suppresses standalone, no corroboration needed",
			in:           TransactionalInput{Domain: "eu.docusign.net", Localpart: "dse"},
			wantSuppress: true,
			wantReason:   "transactional_infra:docusign.net",
		},
		{
			name:         "sendgrid relay suppressed",
			in:           TransactionalInput{Domain: "bounces.sendgrid.net", Localpart: "bounce"},
			wantSuppress: true,
			wantReason:   "transactional_infra:sendgrid.net",
		},
		{
			name:         "config-extra infra eSLD suppressed",
			in:           TransactionalInput{Domain: "mail.customship.io", Localpart: "orders"},
			wantSuppress: true,
			wantReason:   "transactional_infra:customship.io",
		},
		{
			name:         "prefix domain WITH List-Unsubscribe corroboration is suppressed",
			in:           TransactionalInput{Domain: "event.gitex.com", Localpart: "hello", ListUnsubscribe: true},
			wantSuppress: true,
			wantReason:   "transactional_prefix:event",
		},
		{
			name:         "prefix domain WITH machine-localpart corroboration is suppressed",
			in:           TransactionalInput{Domain: "news.gitex.com", Localpart: "no-reply"},
			wantSuppress: true,
			wantReason:   "transactional_prefix:news",
		},
		{
			name:         "prefix domain WITHOUT corroboration is NOT suppressed (a real company can live at event.gitex.com)",
			in:           TransactionalInput{Domain: "event.gitex.com", Localpart: "sales"},
			wantSuppress: false,
		},
		{
			name:         "ordinary company mail is never suppressed",
			in:           TransactionalInput{Domain: "mail.acme.com", Localpart: "jane", ListUnsubscribe: true},
			wantSuppress: false,
		},
		{
			name:         "bare registrable domain has no prefix, never suppressed",
			in:           TransactionalInput{Domain: "gitex.com", Localpart: "sales", ListUnsubscribe: true},
			wantSuppress: false,
		},
		{
			name:         "allowlist wins over a corroborated prefix rule",
			in:           TransactionalInput{Domain: "news.myrealbrand.com", Localpart: "no-reply", ListUnsubscribe: true},
			wantSuppress: false,
		},
		{
			name:         "empty domain is a no-op",
			in:           TransactionalInput{Domain: "", Localpart: "x"},
			wantSuppress: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSuppress, gotReason := list.Suppress(tc.in)
			if gotSuppress != tc.wantSuppress {
				t.Fatalf("Suppress(%+v) = %v, want %v", tc.in, gotSuppress, tc.wantSuppress)
			}
			if tc.wantSuppress && gotReason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", gotReason, tc.wantReason)
			}
			if !tc.wantSuppress && gotReason != "" {
				t.Fatalf("reason = %q, want empty on a non-suppression", gotReason)
			}
		})
	}
}

func TestIsMachineLocalpart(t *testing.T) {
	machine := []string{"no-reply", "noreply", "no.reply", "donotreply", "bounce", "bounces", "notifications", "mailer-daemon", "postmaster", "newsletter"}
	for _, l := range machine {
		if !isMachineLocalpart(l) {
			t.Errorf("isMachineLocalpart(%q) = false, want true", l)
		}
	}
	human := []string{"jane", "jane.doe", "sales", "info", "j.smith"}
	for _, l := range human {
		if isMachineLocalpart(l) {
			t.Errorf("isMachineLocalpart(%q) = true, want false", l)
		}
	}
}
