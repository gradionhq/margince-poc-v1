// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"net/url"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// NormalizeLinkedInURL reduces a LinkedIn profile URL to the one stored
// spelling the E12.11 exact-match dedupe key compares on: https scheme,
// lowercased host, no query, no fragment, no trailing slash. Parsed once
// at the seam (the values.ParseEmail stance), so the dedupe probe, the
// insert, and the audit image all see the same key.
func NormalizeLinkedInURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", &values.ParseError{Field: "linkedin_url", Code: "linkedin_url_empty",
			Message: "a LinkedIn profile URL is required"}
	}
	// A pasted profile often arrives without a scheme; the key is the
	// host+path identity, so default the scheme rather than refuse.
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		return "", &values.ParseError{Field: "linkedin_url", Code: "linkedin_url_malformed",
			Message: "not a resolvable profile URL"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", &values.ParseError{Field: "linkedin_url", Code: "linkedin_url_malformed",
			Message: "a profile URL uses http or https"}
	}
	// http and https address the same profile; canonicalizing to https
	// keeps the dedupe key one spelling per identity. Ports, query and
	// fragment carry tracking noise, never identity.
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	return "https://" + strings.ToLower(u.Hostname()) + path, nil
}
