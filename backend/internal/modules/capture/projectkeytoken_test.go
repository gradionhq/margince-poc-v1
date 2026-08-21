// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"slices"
	"testing"
)

// The subject tokenizer is the whole safety of the ladder's third rung: it runs
// with no human in the loop, so what it refuses to offer the matcher matters as
// much as what it does.
func TestProjectKeyCandidatesReadsWholeTokensOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		subject string
		want    []string
	}{
		{
			name:    "a bracketed key is one token, lowercased",
			subject: "[ERP-27] weekly status",
			want:    []string{"erp-27", "weekly", "status"},
		},
		{
			name: "a key is never a substring of a longer word",
			// ERPNEXT must not offer "erp": a project keyed ERP is not what
			// this subject is about, and nothing downstream would catch it.
			subject: "ERPNEXT rollout",
			want:    []string{"erpnext", "rollout"},
		},
		{
			name:    "underscores and hyphens stay inside a token",
			subject: "re: alpha_two and beta-three",
			want:    []string{"re", "alpha_two", "and", "beta-three"},
		},
		{
			name: "a bare number is not a candidate",
			// The letter-led rule exists for exactly this: a subject line is
			// full of dates, amounts and order numbers.
			subject: "invoice 2026 for 4711 EUR",
			want:    []string{"invoice", "for", "eur"},
		},
		{
			name:    "a one-character word is below the key floor",
			subject: "a b re: ok",
			want:    []string{"re", "ok"},
		},
		{
			name: "a word longer than the key ceiling is not a candidate",
			// 25 characters, one past what project_key_shape admits.
			subject: "abcdefghijklmnopqrstuvwxy done",
			want:    []string{"done"},
		},
		{
			name:    "a repeated key is offered once",
			subject: "ERP-27: about ERP-27",
			want:    []string{"erp-27", "about"},
		},
		{
			name:    "a subject with nothing key-shaped offers nothing",
			subject: "4711 -- 2026/06/04 (!)",
			want:    nil,
		},
		{
			name:    "an empty subject offers nothing",
			subject: "",
			want:    nil,
		},
		{
			name: "a non-ASCII word is rejected whole, not cut into fragments",
			// The key column admits ASCII only, so "grüße" can be no key — and
			// it must not degrade into "gr", which would be evidence for a
			// project keyed GR that the subject never mentioned.
			subject: "grüße ERP",
			want:    []string{"erp"},
		},
		{
			name: "punctuation around a word is trimmed, inside it is not",
			// The trim is what lets a key survive the shapes a subject wraps it
			// in; a key-legal hyphen inside the word must not be touched.
			subject: `("ERP-27"), please`,
			want:    []string{"erp-27", "please"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := projectKeyCandidates(tc.subject)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("projectKeyCandidates(%q) = %q, want %q", tc.subject, got, tc.want)
			}
		})
	}
}
