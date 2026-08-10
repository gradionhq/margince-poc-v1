// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package apps

// The admission check: what this server will and will not serve as a view.
//
// WHAT THIS CHECK IS WORTH, stated plainly, because the reassuring version would
// be false. It is a SUBSTRING RATCHET and corruption detection — not an
// integrity boundary. A computed property (`node['inner'+'HTML']`), an alias, or
// a string assembled at run time walks straight past it, and at run time it can
// equally false-positive on innocent text. What it does catch is the class that
// actually threatens this design: a document that is not the one the build
// produced — a substituted file, a proxy's error page, an ingress serving the
// app shell — because those carry the constructs a real view never has.
//
// THE REAL CONTROLS ARE ELSEWHERE, and each is stronger than this one: the
// build-time PARSED validation in frontend/scripts/vite-inline-views.ts, the
// Playwright zero-request run, TLS and origin discipline on the fetch, and the
// host-enforced content-security policy that SEP-1865 requires. This is defence
// in depth on the seam where the bytes cross a boundary.
//
// IT RUNS THE SAME TOKEN LIST AS THE BUILD CHECK, from the same file, and that
// identity is the point rather than a coincidence: a document that passes the
// build and is refused here would be an outage no lane can see, because the lane
// that builds a document never runs the server that refuses it.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"sync"
)

// forbiddenJSON is the admission vocabulary, AUTHORED in
// frontend/src/mcp-apps/forbidden.json and copied here by
// `make -C backend mcp-apps-vocab` because go:embed cannot reach outside its own
// package directory. TestTheAdmissionVocabularyMatchesTheFrontend is what stops
// the copy drifting from the original.
//
//go:embed forbidden.json
var forbiddenJSON []byte

// admissionVocabulary decodes the embedded list once.
//
// It decodes into a MAP rather than a struct with one member per class, and that
// is not laziness. The frontend validator iterates every list in the file; a Go
// struct enforces only the classes someone remembered to declare, so a fifth
// category added to the shared file would be enforced on one side of the
// boundary and silently ignored on the other — while the byte-equality test kept
// passing, because the bytes ARE identical. The names of the classes are
// documentation (offOrigin, toolCall, credential, markupSink); what this code
// needs is all of them.
//
// It returns the error rather than panicking at package init: a malformed
// vocabulary is a bad build, and admit turns it into a refusal so the failure is
// loud where it matters — on the document — instead of at a process start
// nobody is watching.
var admissionVocabulary = sync.OnceValues(func() (map[string][]string, error) {
	var rules map[string][]string
	if err := json.Unmarshal(forbiddenJSON, &rules); err != nil {
		return nil, fmt.Errorf("crmapps: decoding the admission vocabulary: %w", err)
	}
	if len(rules) == 0 {
		return nil, errors.New("crmapps: the admission vocabulary is empty, so every document would be admitted")
	}
	return rules, nil
})

// titlePattern extracts the document's own title.
//
// A narrow regexp rather than a parser: this is HTML, not a language, and one
// element is not worth a parse tree on a path that runs per fetch. The parsed
// judgement of the whole document belongs to the build-time validator, which has
// the toolchain for it.
var titlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// admit judges one document.
//
// A non-empty findings slice means REFUSE, and it names every reason rather than
// the first: an operator reading one at a time re-deploys once per finding.
//
// titleMismatch is REPORTED, never a refusal. Comparing the Go catalog's title
// to the document's is two hand-spellings across a language boundary, and a copy
// edit on one side must not take a view down.
func admit(doc string, wantTitle string) (findings []string, titleMismatch bool) {
	rules, err := admissionVocabulary()
	if err != nil {
		// Fail closed, and say so on the document rather than swallowing it: a
		// check that cannot run has not passed.
		return []string{err.Error()}, false
	}
	lowered := strings.ToLower(doc)
	for _, tokens := range rules {
		for _, token := range tokens {
			if contains(doc, lowered, token) {
				findings = append(findings, token)
			}
		}
	}
	return findings, wantTitle != "" && documentTitle(doc) != wantTitle
}

// contains matches a token against the document, case-INSENSITIVELY for the
// HTML-shaped ones.
//
// HTML element and attribute names are case-insensitive, so `<LINK` and `SRC=`
// are the same document as `<link` and `src=` — and a substituted document is
// free to shout. JavaScript identifiers are not: `XMLHttpRequest` matched
// loosely would also match text that merely reads like it, and this check
// already false-positives readily enough on prose.
//
// The rule is derived from the token's own shape rather than from a second list
// somebody keeps in step, and the frontend validator applies the same one.
func contains(doc, lowered, token string) bool {
	if strings.HasPrefix(token, "<") || strings.HasSuffix(token, "=") {
		return strings.Contains(lowered, strings.ToLower(token))
	}
	return strings.Contains(doc, token)
}

// documentTitle answers the document's declared title, unescaped, or the empty
// string when it declares none.
func documentTitle(doc string) string {
	found := titlePattern.FindStringSubmatch(doc)
	if found == nil {
		return ""
	}
	return html.UnescapeString(strings.TrimSpace(found[1]))
}
