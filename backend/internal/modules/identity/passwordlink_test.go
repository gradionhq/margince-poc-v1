// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Where a delivered credential sits in a URL is a security property, so it is
// gated rather than reviewed. Three links carry a live single-use token — the
// password reset (one hour), the member invite (seven days), and the admin-issued
// set-password link (seven days) — and each is
// only as safe as the URL it is pasted into: a token in the server-visible
// part reaches nginx access logs, is sent as a Referer on the SPA's same-origin
// /v1 calls, and becomes a Cache Storage key when a service worker caches the
// navigation.
//
// A test of `passwordLink` alone would keep passing while a caller hand-rolled a
// second link beside it, so the guard below derives the obligation from the
// package's own source instead.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPasswordLinkKeepsTheTokenOutOfTheServerVisibleURL(t *testing.T) {
	link := passwordLink("https://crm.example.test", "raw-token-value")

	// Everything before the '#' is what a server, a proxy and an access log get
	// to see. The token must not be in it.
	serverVisible, fragment, found := strings.Cut(link, "#")
	if !found {
		t.Fatalf("link carries no fragment, so the token is server-visible: %q", link)
	}
	if strings.Contains(serverVisible, "raw-token-value") {
		t.Errorf(
			"token appears in the server-visible part of the link: %q (full: %q)",
			serverVisible, link,
		)
	}
	if !strings.Contains(fragment, "raw-token-value") {
		t.Errorf("token is not in the fragment, so the link cannot work: %q", link)
	}
}

// readsBaseURL matches a READ of the field, so the assignment in
// `Handlers.WithPasswordLinkBase` is not mistaken for a link being built.
// A single `=` after the field is an ASSIGNMENT and not a read; `==` is a
// comparison and IS one, so the lookahead-free spelling has to admit the second
// while excluding the first. Getting this wrong silently narrows the guard:
// every `==` line would be dropped by the outer filter before the
// configured-check exemption below ever saw it.
var readsBaseURL = regexp.MustCompile(`h\.passwordLinkBaseURL\s*(==|[^=\s]|$)`)

// testsWhetherBaseIsConfigured matches the one OTHER thing a caller may
// legitimately do with the field: ask whether it is set at all. Two callers
// need that and neither builds a URL — /me decides whether to advertise the
// admin-issued link, and the issuing handler refuses when there is no base to
// build one from. An emptiness comparison cannot leak a token into a
// server-visible URL, which is the property this file exists to protect;
// concatenating the value is what must stay inside passwordLink.
var testsWhetherBaseIsConfigured = regexp.MustCompile(`h\.passwordLinkBaseURL\s*[!=]=\s*""`)

func TestEveryDeliveredLinkIsBuiltByPasswordLink(t *testing.T) {
	// Keyed on the BASE URL rather than on a path string, and that choice is the
	// whole strength of this test: any hand-rolled builder must read
	// `h.passwordLinkBaseURL` to have something to build from, whatever it does with the
	// path afterwards. Matching a literal like "/reset-password?token=" instead
	// would pass for an fmt.Sprintf, for a split constant, or for any other
	// spelling of the same defect — a point fix dressed as a fitness function.
	//
	// One directory, non-recursively, is the CORRECT scope and not a shortcut:
	// `passwordLinkBaseURL` is an unexported field, so no other package — including
	// `identity/internal/...` — can read it. The field's visibility is what bounds
	// the search. Widen this to the tree and it stops being a closed argument.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	scanned, checked := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// Test files may read the field in order to assert against it.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		for number, line := range strings.Split(string(source), "\n") {
			if !readsBaseURL.MatchString(line) {
				continue
			}
			checked++
			if testsWhetherBaseIsConfigured.MatchString(line) {
				continue
			}
			if !strings.Contains(line, "passwordLink(") {
				t.Errorf(
					"%s:%d uses passwordLinkBaseURL outside passwordLink — build the link "+
						"with passwordLink so the token stays in the fragment:\n\t%s",
					name, number+1, strings.TrimSpace(line),
				)
			}
		}
	}

	// A scan that matched nothing would pass while proving nothing, so both the
	// file walk and the field reads have to have found something.
	if scanned == 0 {
		t.Fatal("scanned no package source files — the guard is vacuous")
	}
	if checked == 0 {
		t.Fatal("found no read of passwordLinkBaseURL — the guard no longer matches the field")
	}
}
