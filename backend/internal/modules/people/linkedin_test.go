// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

func TestNormalizeLinkedInURLProducesOneSpellingPerProfile(t *testing.T) {
	// Every spelling of the same profile must reduce to the same key —
	// that identity IS the E12.11 exact-match dedupe.
	cases := map[string]string{
		"https://www.linkedin.com/in/vera-vp":                  "https://www.linkedin.com/in/vera-vp",
		"https://WWW.LinkedIn.com/in/vera-vp":                  "https://www.linkedin.com/in/vera-vp",
		"https://www.linkedin.com/in/vera-vp/":                 "https://www.linkedin.com/in/vera-vp",
		"https://www.linkedin.com/in/vera-vp?utm_source=share": "https://www.linkedin.com/in/vera-vp",
		"https://www.linkedin.com/in/vera-vp#about":            "https://www.linkedin.com/in/vera-vp",
		"http://www.linkedin.com/in/vera-vp":                   "https://www.linkedin.com/in/vera-vp",
		"www.linkedin.com/in/vera-vp":                          "https://www.linkedin.com/in/vera-vp",
		"  https://www.linkedin.com/in/vera-vp/?trk=profile  ": "https://www.linkedin.com/in/vera-vp",
		"https://www.linkedin.com:443/in/vera-vp":              "https://www.linkedin.com/in/vera-vp",
		"https://de.linkedin.com/in/vera-vp":                   "https://de.linkedin.com/in/vera-vp",
		"https://www.linkedin.com/in/Vera-VP":                  "https://www.linkedin.com/in/Vera-VP", // the slug is identity; only the host case-folds
	}
	for raw, want := range cases {
		got, err := NormalizeLinkedInURL(raw)
		if err != nil {
			t.Errorf("NormalizeLinkedInURL(%q): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeLinkedInURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizeLinkedInURLRefusesNonURLs(t *testing.T) {
	for _, raw := range []string{"", "   ", "ftp://linkedin.com/in/x", "https://", "://nope"} {
		_, err := NormalizeLinkedInURL(raw)
		var parseErr *values.ParseError
		if !errors.As(err, &parseErr) {
			t.Errorf("NormalizeLinkedInURL(%q): got %v, want a values.ParseError", raw, err)
		}
	}
}
