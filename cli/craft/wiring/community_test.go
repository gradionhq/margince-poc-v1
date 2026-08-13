// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package wiring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The community-health files GitHub resolves from the repository root. Each one
// is a promise to somebody arriving from outside, so its absence is a defect in
// the same sense a missing gate is: nothing else in the tree notices.
var communityHealthFiles = []string{
	"README.md",
	"LICENSE",
	"CODE_OF_CONDUCT.md",
	"CONTRIBUTING.md",
	"SECURITY.md",
	"SUPPORT.md",
	"CODEOWNERS",
	".github/PULL_REQUEST_TEMPLATE.md",
	".github/ISSUE_TEMPLATE/config.yml",
}

func TestCommunityHealthFilesArePresent(t *testing.T) {
	for _, rel := range communityHealthFiles {
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err != nil {
			t.Errorf("community-health file %s is not readable: %v", rel, err)
		}
	}
}

// issueForms are the templates a reporter picks between. The set is asserted by
// name because config.yml turns blank issues off: a form deleted without its
// replacement leaves that kind of report with nowhere to go at all.
var issueForms = []string{
	"bug_report.yml",
	"follow_up.yml",
	"capability_gap.yml",
	"documentation.yml",
}

func TestIssueFormsCarryThePublicRepositoryWarning(t *testing.T) {
	for _, form := range issueForms {
		doc := readRepoFile(t, filepath.Join(".github/ISSUE_TEMPLATE", form))
		// Every form makes the reporter confirm this before submitting, because
		// an issue body is public permanently — including in its edit history.
		for _, want := range []string{"public", "secrets", "required: true"} {
			if !strings.Contains(doc, want) {
				t.Errorf("issue form %s missing %q: the hygiene confirmation must be present and required", form, want)
			}
		}
	}
}

func TestBlankIssuesAreOffAndSecurityIsRoutedPrivately(t *testing.T) {
	cfg := readRepoFile(t, ".github/ISSUE_TEMPLATE/config.yml")
	if !strings.Contains(cfg, "blank_issues_enabled: false") {
		t.Error("config.yml must disable blank issues so every report arrives through a form")
	}
	// A vulnerability filed as a public issue is disclosed to every deployment
	// before a fix exists, so the advisory route is offered at the point of choice.
	if !strings.Contains(cfg, "/security/advisories/new") {
		t.Error("config.yml must link the private advisory form so a vulnerability never needs a public issue")
	}
}

func TestCodeOfConductNamesAReachableEnforcementContact(t *testing.T) {
	doc := readRepoFile(t, "CODE_OF_CONDUCT.md")
	// The Contributor Covenant ships with a bracketed placeholder here. Shipping
	// that placeholder is worse than shipping no policy: it invites a report and
	// then drops it.
	for _, placeholder := range []string{"[INSERT CONTACT METHOD]", "[INSERT EMAIL ADDRESS]"} {
		if strings.Contains(doc, placeholder) {
			t.Errorf("CODE_OF_CONDUCT.md still carries the %s placeholder", placeholder)
		}
	}
	if !strings.Contains(doc, "conduct@gradion.com") {
		t.Error("CODE_OF_CONDUCT.md must name the enforcement contact so a report has somewhere to go")
	}
	// Conduct and vulnerability reports take different routes; the policy says so
	// rather than letting a reporter guess and pick the public one.
	if !strings.Contains(doc, "SECURITY.md") {
		t.Error("CODE_OF_CONDUCT.md must point security findings at SECURITY.md, not the conduct address")
	}
}

func TestSecurityPolicyOffersAPrivateRouteAndATimeframe(t *testing.T) {
	doc := readRepoFile(t, "SECURITY.md")
	if !strings.Contains(doc, "/security/advisories/new") {
		t.Error("SECURITY.md must link the advisory form directly; naming the tab alone assumes the reporter finds it")
	}
	// An acknowledgement window is the difference between a private report and
	// silence that pushes a reporter towards public disclosure.
	if !strings.Contains(doc, "business days") {
		t.Error("SECURITY.md must state how long acknowledgement takes")
	}
}

func TestCodeownersHasACatchAllOwner(t *testing.T) {
	doc := readRepoFile(t, "CODEOWNERS")
	for _, line := range strings.Split(doc, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "*" {
			return
		}
	}
	// Without one, a pull request touching any unlisted path requests no reviewer
	// and can sit unnoticed while looking like it is waiting on nobody.
	t.Error("CODEOWNERS must declare a `*` catch-all owner")
}
