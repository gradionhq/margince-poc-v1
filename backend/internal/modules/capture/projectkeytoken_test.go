// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"slices"
	"strings"
	"testing"
)

// The subject tokenizer is the whole safety of the ladder's third rung: it runs
// with no human in the loop, so what it refuses to offer the matcher matters as
// much as what it does.
func TestProjectKeyCandidatesRequiresABracketedKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		subject string
		want    []string
	}{
		{
			name:    "a bracketed key is the one candidate, lowercased",
			subject: "[ERP-27] weekly status",
			want:    []string{"erp-27"},
		},
		{
			name: "a bare word is never a key",
			// The bug this rule exists to stop: without the bracket
			// requirement a project keyed ERP takes this message, and one
			// keyed STATUS or WEEKLY takes it too.
			subject: "ERP weekly status",
			want:    nil,
		},
		{
			name: "a bare Re: never matches",
			// A project keyed RE would otherwise swallow every reply in the
			// installation — silently, in bulk, onto records that later get
			// stamped for years of retention.
			subject: "Re: anything at all",
			want:    nil,
		},
		{
			name:    "a bracketed key is not a substring of a longer word",
			subject: "[ERPNEXT] rollout",
			want:    []string{"erpnext"},
		},
		{
			name: "bracketed prose with a space is not a key",
			// project_key_shape admits no space, so a bracket group holding
			// one was never a key reference.
			subject: "[ERP 27] status",
			want:    nil,
		},
		{
			name:    "a bracketed key surrounded by whitespace is trimmed",
			subject: "[ ERP-27 ] status",
			want:    []string{"erp-27"},
		},
		{
			name:    "underscores and hyphens are legal inside a key",
			subject: "[alpha_two] and [beta-three]",
			want:    []string{"alpha_two", "beta-three"},
		},
		{
			name: "a bracketed bare number is not a candidate",
			// The letter-led rule: a subject line is full of dates, amounts
			// and order numbers.
			subject: "[2026] invoice [4711]",
			want:    nil,
		},
		{
			name:    "an empty bracket offers nothing",
			subject: "[] and [x]",
			want:    nil,
		},
		{
			name:    "a word longer than the key ceiling is not a candidate",
			subject: "[abcdefghijklmnopqrstuvwxy] done",
			want:    nil,
		},
		{
			name:    "a repeated key is offered once",
			subject: "[ERP-27] about [ERP-27]",
			want:    []string{"erp-27"},
		},
		{
			name: "an unclosed bracket names no key",
			// Otherwise a stray '[' would turn the rest of the line into a
			// candidate.
			subject: "[ERP-27 status never closed",
			want:    nil,
		},
		{
			name: "parentheses and braces are not markers",
			// Admitting them would reopen the bare-word problem under
			// different punctuation: "(re)" is prose.
			subject: "(ERP) {ERP} status",
			want:    nil,
		},
		{
			name: "a bracketed non-ASCII word is rejected whole",
			// The key column admits ASCII only, and the word must not
			// degrade into a fragment that names a different project.
			subject: "[grüße] and [ERP]",
			want:    []string{"erp"},
		},
		{
			name:    "an empty subject offers nothing",
			subject: "",
			want:    nil,
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

// A subject is attacker-supplied text, and every candidate becomes a bind in
// the matcher's query. The cap is what stops one message turning the rung into
// an unbounded query.
func TestProjectKeyCandidatesAreBounded(t *testing.T) {
	var subject strings.Builder
	for i := range maxProjectKeyCandidates * 3 {
		subject.WriteString("[key")
		subject.WriteByte(byte('a' + i%26))
		subject.WriteString(string(rune('a'+i/26)) + "] ")
	}
	if got := projectKeyCandidates(subject.String()); len(got) > maxProjectKeyCandidates {
		t.Fatalf("a subject offered %d candidates, want at most %d", len(got), maxProjectKeyCandidates)
	}
}
