// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

const (
	// linkedInCompanyPrefix is the one path shape a company URL takes. A person
	// profile lives under /in/, a company under /company/ — storing one where
	// the other belongs would make the uniqueness index collide two different
	// kinds of record, so the shape is checked here and again by the column
	// constraint.
	linkedInCompanyPrefix = "/company/"
	linkedInField         = "linkedin_url"
	linkedInHost          = "linkedin.com"
)

// normalizeOrgLinkedInURL reduces a LinkedIn *company* URL to its stored
// spelling (PO-DDL-N-2, ADR-0085). The scheme, host, query and fragment rules
// are the profile normalizer's, so both sides of the product canonicalize a
// LinkedIn URL exactly once and the same way. What differs is the path: a
// company URL must name a company, and the slug must be present.
//
// The caller decides what an empty input means. Here it is a parse failure,
// because "" is not a company URL; the update path treats a cleared field as a
// clear before it ever reaches this function.
func normalizeOrgLinkedInURL(raw string) (string, error) {
	normalized, err := NormalizeLinkedInURL(raw)
	if err != nil {
		return "", err
	}
	host, path, ok := splitLinkedInHostPath(normalized)
	if !ok || !isLinkedInCompanyHost(host) {
		return "", &values.ParseError{
			Field:   linkedInField,
			Code:    "linkedin_url_not_linkedin",
			Message: "a company URL is on linkedin.com, optionally under a country subdomain",
		}
	}
	slug, isCompany := strings.CutPrefix(path, linkedInCompanyPrefix)
	// The slug is the company's identity, so a deeper path is the same company
	// seen from one of its tabs — /company/voltaq/about and /company/voltaq name
	// one record, and the unique index has to see one spelling for them. Cutting
	// to the first segment is also what keeps the value inside
	// organization_linkedin_url_shape, whose slug pattern admits no slash.
	slug, _, _ = strings.Cut(slug, "/")
	if !isCompany || slug == "" {
		return "", &values.ParseError{
			Field:   linkedInField,
			Code:    "linkedin_url_not_company",
			Message: "a company URL looks like https://www.linkedin.com/company/<name>",
		}
	}
	return "https://" + host + linkedInCompanyPrefix + slug, nil
}

// orgLinkedInPatchValue resolves what the guarded patch should write for a
// supplied LinkedIn URL: nil to clear the column, or the normalized spelling.
// Clearing is a legitimate edit — unlike the person path, where the URL is the
// dedupe key, a company's LinkedIn URL is an optional identity field.
func orgLinkedInPatchValue(raw string) (*string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil //nolint:nilnil // a cleared optional field is a NULL write, not an error
	}
	normalized, err := normalizeOrgLinkedInURL(raw)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

// isLinkedInCompanyHost answers whether a host is one the stored column can
// hold, matching organization_linkedin_url_shape's `([a-z]{2,3}\.)?linkedin\.com`
// exactly. A suffix test would not: it admits `notlinkedin.com`, and
// `jobs.linkedin.com` — a real LinkedIn host, four letters, outside the CHECK.
// Both would be accepted here and then refused by the database, turning a bad
// URL into a constraint violation where the caller deserves a 422.
func isLinkedInCompanyHost(host string) bool {
	if host == linkedInHost {
		return true
	}
	sub, found := strings.CutSuffix(host, "."+linkedInHost)
	if !found || len(sub) < 2 || len(sub) > 3 {
		return false
	}
	for _, r := range sub {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// splitLinkedInHostPath separates the already-normalized URL back into host and
// path. The normalizer guarantees the https:// prefix, so the split is on the
// first slash after it rather than a second parse.
func splitLinkedInHostPath(normalized string) (host, path string, ok bool) {
	const scheme = "https://"
	rest, found := strings.CutPrefix(normalized, scheme)
	if !found || rest == "" {
		return "", "", false
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i], rest[i:], true
	}
	return rest, "", true
}
