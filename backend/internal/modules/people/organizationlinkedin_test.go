// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The company LinkedIn URL normalizer (PO-DDL-N-2, ADR-0085).
//
// Two obligations meet here. The stored spelling has to be canonical, because
// organization_linkedin_url_key is unique on lower(linkedin_url) among live
// rows — two spellings of one company must not both be storable. And it has to
// satisfy organization_linkedin_url_shape, the column CHECK that refuses
// anything that is not a linkedin.com company path, so a value this function
// accepts can never be one the database then rejects.

import (
	"errors"
	"regexp"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// columnShape mirrors organization_linkedin_url_shape from migration 0191. A
// normalized value that fails it would reach the database as a constraint
// violation instead of a 422, which is the wrong answer to a bad URL.
var columnShape = regexp.MustCompile(`^https://([a-z]{2,3}\.)?linkedin\.com/company/[^/?#]+/?$`)

func TestNormalizeOrgLinkedInURLCanonicalizesWhatTheUniqueIndexCompares(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"bare host", "linkedin.com/company/voltaq", "https://linkedin.com/company/voltaq"},
		{"http upgraded", "http://www.linkedin.com/company/voltaq", "https://www.linkedin.com/company/voltaq"},
		{"tracking query dropped", "https://www.linkedin.com/company/voltaq?trk=nav", "https://www.linkedin.com/company/voltaq"},
		{"fragment dropped", "https://www.linkedin.com/company/voltaq#about", "https://www.linkedin.com/company/voltaq"},
		{"surrounding space trimmed", "  https://www.linkedin.com/company/voltaq  ", "https://www.linkedin.com/company/voltaq"},
		{"regional subdomain kept", "https://de.linkedin.com/company/voltaq", "https://de.linkedin.com/company/voltaq"},
		{"trailing slash dropped", "https://www.linkedin.com/company/voltaq/", "https://www.linkedin.com/company/voltaq"},
		// A tab of the company page is the same company. The column CHECK's slug
		// pattern admits no slash, so keeping the deeper path would hand the
		// database a value it refuses.
		{"company tab cut to the company", "https://www.linkedin.com/company/voltaq/about/", "https://www.linkedin.com/company/voltaq"},
		{"posts tab cut to the company", "https://www.linkedin.com/company/voltaq/posts/?feedView=all", "https://www.linkedin.com/company/voltaq"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeOrgLinkedInURL(tc.raw)
			if err != nil {
				t.Fatalf("normalize %q: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if !columnShape.MatchString(got) {
				t.Errorf("%q does not satisfy organization_linkedin_url_shape — the store would hand the database a value it refuses", got)
			}
		})
	}
}

func TestNormalizeOrgLinkedInURLRefusesWhatIsNotACompanyURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		code string
	}{
		{"a person profile", "https://www.linkedin.com/in/lars", "linkedin_url_not_company"},
		{"a company path with no company", "https://www.linkedin.com/company/", "linkedin_url_not_company"},
		{"the company index itself", "https://www.linkedin.com/company", "linkedin_url_not_company"},
		{"another network entirely", "https://xing.com/company/voltaq", "linkedin_url_not_linkedin"},
		// Both of these pass a suffix test and fail the column's own regex, so
		// accepting them would turn a bad URL into a constraint violation.
		{"a lookalike domain", "https://notlinkedin.com/company/voltaq", "linkedin_url_not_linkedin"},
		{"a real LinkedIn host outside the shape", "https://jobs.linkedin.com/company/voltaq", "linkedin_url_not_linkedin"},
		{"a numeric subdomain", "https://d3.linkedin.com/company/voltaq", "linkedin_url_not_linkedin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeOrgLinkedInURL(tc.raw)
			if err == nil {
				t.Fatalf("normalized %q to %q, want a refusal", tc.raw, got)
			}
			var parse *values.ParseError
			if !errors.As(err, &parse) {
				t.Fatalf("got %v, want a 422 naming the field", err)
			}
			if parse.Field != linkedInField {
				t.Errorf("field = %q, want %q", parse.Field, linkedInField)
			}
			if parse.Code != tc.code {
				t.Errorf("code = %q, want %q", parse.Code, tc.code)
			}
		})
	}
}

// A cleared field is an edit, not a mistake: unlike the person side, where the
// URL is the dedupe key, a company's LinkedIn URL is optional.
func TestOrgLinkedInPatchValueTreatsAnEmptyInputAsAClear(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		got, err := orgLinkedInPatchValue(raw)
		if err != nil {
			t.Fatalf("clear via %q: %v", raw, err)
		}
		if got != nil {
			t.Errorf("got %q, want nil — an empty input writes NULL", *got)
		}
	}
}

func TestOrgLinkedInPatchValueNormalizesBeforeItWrites(t *testing.T) {
	got, err := orgLinkedInPatchValue("  www.linkedin.com/company/voltaq?trk=nav  ")
	if err != nil {
		t.Fatalf("patch value: %v", err)
	}
	if got == nil {
		t.Fatal("a real URL must not be treated as a clear")
	}
	if *got != "https://www.linkedin.com/company/voltaq" {
		t.Errorf("got %q, want the canonical spelling", *got)
	}
}

func TestOrgLinkedInPatchValueRefusesAPersonProfile(t *testing.T) {
	if _, err := orgLinkedInPatchValue("https://www.linkedin.com/in/lars"); err == nil {
		t.Fatal("a person profile stored on a company would collide two kinds of record in one unique index")
	}
}
