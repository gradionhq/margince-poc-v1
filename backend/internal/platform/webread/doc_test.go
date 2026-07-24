// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import "testing"

func TestDocIsMarkdown(t *testing.T) {
	cases := []struct {
		mediaType string
		want      bool
	}{
		{"text/markdown", true},
		{"text/x-markdown", true},
		{"text/html", false},
		{"application/json", false},
		{"", false},
	}
	for _, c := range cases {
		if got := (Doc{MediaType: c.mediaType}).IsMarkdown(); got != c.want {
			t.Errorf("Doc{%q}.IsMarkdown() = %v, want %v", c.mediaType, got, c.want)
		}
	}
}

func TestParseMediaTypeStripsParametersAndLowercases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"text/markdown; charset=utf-8", "text/markdown"},
		{"TEXT/HTML", "text/html"},
		{"", ""},
		{"not a media type ///", "not a media type ///"}, // malformed → best-effort trimmed lowercase
	}
	for _, c := range cases {
		if got := parseMediaType(c.in); got != c.want {
			t.Errorf("parseMediaType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
