// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package apps

// The admission check, held against the documents it exists to refuse.
//
// THIS IS THE ONE PLACE A HAND-WRITTEN FIXTURE IS RIGHT, and it is worth saying
// why given rule 6: the subject under test IS the validator, so a document
// invented here is the input, not a stand-in for production. Everywhere else in
// this package the real bytes come from the build.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cleanDocument is what the build actually emits, reduced to its shape: a
// doctype, the licence header, a title, an inline stylesheet, the root element
// and an inline script. Every refusal case below is this document plus one
// thing.
const cleanDocument = `<!doctype html>
<!--
SPDX-License-Identifier: BUSL-1.1
SPDX-FileCopyrightText: 2026 Gradion
-->
<html lang="en"><head><meta charset="utf-8"><title>Morning brief</title>
<style>.row{border:1px solid var(--borderSubtle)}</style></head>
<body><main id="root"></main>
<script>
function render(root, data) { root.replaceChildren(); }
</script>
</body></html>`

func TestAdmitAcceptsATrueSelfContainedDocument(t *testing.T) {
	findings, mismatch := admit(cleanDocument, "Morning brief")
	if len(findings) != 0 {
		t.Fatalf("admit refused a clean document: %v", findings)
	}
	if mismatch {
		t.Fatal("admit reported a title mismatch on a document whose title matches")
	}
}

func TestAdmitRefusesADocumentThatReachesOffOrigin(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"a stylesheet link", `<link rel="stylesheet" href="/a.css">`},
		{"an absolute script source", `<script src="https://cdn.example/x.js"></script>`},
		{"a css import", `<style>@import url("/x.css");</style>`},
		{"a font the sandbox can never load", `<style>@font-face{src:url(/f.woff2)}</style>`},
		{"a fetch call", `<script>fetch("/v1/people")</script>`},
		{"a websocket", `<script>new WebSocket("wss://x")</script>`},
		{"a beacon", `<script>navigator.sendBeacon("/x")</script>`},
		{"dev server residue", `<script src="/@vite/client"></script>`},
		{"a source map comment", `<script>1</script><!--//# sourceMappingURL=a.map-->`},
		{"a nested frame", `<iframe></iframe>`},
		{"a base element that repoints every relative url", `<base href="https://x/">`},
		{"a meta refresh", `<meta http-equiv="refresh" content="0;url=/x">`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if findings, _ := admit(cleanDocument+tc.doc, "Morning brief"); len(findings) == 0 {
				t.Fatalf("admit accepted a document that reaches off-origin: %s", tc.doc)
			}
		})
	}
}

func TestAdmitRefusesADocumentThatCallsAToolOrCarriesACredential(t *testing.T) {
	// These two matter MORE now that the bytes cross a boundary: a substituted
	// document that calls app-visible tools through the host bridge is exactly
	// what this check exists to catch, and the off-origin list alone admits it.
	for _, tc := range []struct{ name, doc string }{
		{"a tool call", `<script>parent.postMessage({method:"tools/call"},"*")</script>`},
		{"a bearer token", `<script>const h={Authorization:"Bearer x"}</script>`},
		{"a stashed access token", `<script>const t=answer.access_token</script>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if findings, _ := admit(cleanDocument+tc.doc, "Morning brief"); len(findings) == 0 {
				t.Fatalf("admit accepted %q", tc.doc)
			}
		})
	}
}

func TestAdmitRefusesADocumentThatBuildsMarkupFromData(t *testing.T) {
	// The one privilege the sandbox cannot take back is execution inside the
	// view's own origin, and assigning customer text as markup is how a view
	// hands it over. Spelled in pieces so this file does not trip the check it
	// is testing when the whole tree is swept.
	for _, sink := range []string{"inner" + "HTML", "insertAdjacent" + "HTML", "eval("} {
		doc := cleanDocument + `<script>node.` + sink + `= x</script>`
		if findings, _ := admit(doc, "Morning brief"); len(findings) == 0 {
			t.Errorf("admit accepted a document using %s", sink)
		}
	}
}

func TestAdmitNamesEveryReasonRatherThanTheFirst(t *testing.T) {
	// An operator reading one refusal at a time re-deploys once per finding.
	findings, _ := admit(cleanDocument+`<link href="https://cdn.example/a.css">`, "Morning brief")
	if len(findings) < 2 {
		t.Fatalf("admit reported %v; a document with a link AND an absolute origin has two reasons", findings)
	}
}

func TestAdmitReportsATitleMismatchWithoutRefusing(t *testing.T) {
	// A copy edit on one side of a language boundary must not take a view down.
	doc := strings.Replace(cleanDocument, "<title>Morning brief</title>", "<title>Mornning brief</title>", 1)
	findings, mismatch := admit(doc, "Morning brief")
	if len(findings) != 0 {
		t.Fatalf("a title mismatch refused the document: %v", findings)
	}
	if !mismatch {
		t.Fatal("a title mismatch went unreported")
	}
}

func TestAdmitReportsAMissingTitleWithoutRefusing(t *testing.T) {
	doc := strings.Replace(cleanDocument, "<title>Morning brief</title>", "", 1)
	findings, mismatch := admit(doc, "Morning brief")
	if len(findings) != 0 {
		t.Fatalf("a missing title refused the document: %v", findings)
	}
	if !mismatch {
		t.Fatal("a document with no title at all went unreported")
	}
}

func TestAdmitReadsATitleThroughItsAttributesAndEntities(t *testing.T) {
	// The title is written by a different toolchain than the one that reads it,
	// so neither the attribute nor the entity form is hypothetical.
	doc := strings.Replace(cleanDocument, "<title>Morning brief</title>",
		`<title dir="ltr">Who knows &amp; who does not</title>`, 1)
	if _, mismatch := admit(doc, "Who knows & who does not"); mismatch {
		t.Fatal("admit reported a mismatch against a title it should have read")
	}
}

// TestTheAdmissionVocabularyMatchesTheFrontend fails when the two copies of
// forbidden.json differ.
//
// Two validators in two languages are a defect unless they cannot disagree: a
// document that passes the build check and is refused in production is an outage
// no CI lane can see, because the lane that builds the document never runs the
// server that refuses it. `make mcp-apps-vocab` is how the copy is made.
func TestTheAdmissionVocabularyMatchesTheFrontend(t *testing.T) {
	authored, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..",
		"frontend", "src", "mcp-apps", "forbidden.json"))
	if err != nil {
		t.Fatalf("reading the authored vocabulary: %v", err)
	}
	if string(authored) != string(forbiddenJSON) {
		t.Fatal("the embedded admission vocabulary differs from frontend/src/mcp-apps/forbidden.json. " +
			"The frontend copy is the authored one; run `make -C backend mcp-apps-vocab` and commit the result")
	}
}

func TestTheAdmissionVocabularyIsNotEmpty(t *testing.T) {
	// A vocabulary that failed to decode would admit everything while every
	// assertion above still passed, because each of them adds a token and this
	// one asks whether any token exists at all.
	rules, err := admissionVocabulary()
	if err != nil {
		t.Fatalf("decoding the embedded vocabulary: %v", err)
	}
	// Every class the FILE declares, not a list of classes named here: a fifth
	// category added upstream must be enforced rather than silently ignored,
	// and a Go-side list would be exactly the thing that ignores it.
	for _, name := range []string{"offOrigin", "toolCall", "credential", "markupSink"} {
		if len(rules[name]) == 0 {
			t.Errorf("the %s list is empty, so that whole class is admitted unchecked", name)
		}
	}
	for name, tokens := range rules {
		if len(tokens) == 0 {
			t.Errorf("the %s list is empty, so that whole class is admitted unchecked", name)
		}
	}
}

// A class the shared file grows must be enforced by BOTH validators. The
// frontend iterates every list in the file; if this side named its classes in a
// struct, a fifth one would be enforced on one side of the boundary and ignored
// on the other — while the byte-equality test kept passing, because the bytes
// really are identical.
func TestAdmitEnforcesEveryClassTheVocabularyDeclares(t *testing.T) {
	rules, err := admissionVocabulary()
	if err != nil {
		t.Fatalf("decoding the embedded vocabulary: %v", err)
	}
	for name, tokens := range rules {
		doc := cleanDocument + "<!-- " + tokens[0] + " -->"
		if findings, _ := admit(doc, "Morning brief"); len(findings) == 0 {
			t.Errorf("a document carrying %q from the %s class was admitted", tokens[0], name)
		}
	}
}

// HTML element and attribute names are case-insensitive, so a substituted
// document is free to shout — and every HTML-shaped token would miss.
func TestAdmitRefusesAnUppercasedHTMLConstruct(t *testing.T) {
	for _, doc := range []string{
		`<LINK REL="stylesheet" HREF="/a.css">`,
		`<IFRAME></IFRAME>`,
		`<IMG SRC="/track">`,
		`<BASE HREF="//elsewhere/">`,
	} {
		if findings, _ := admit(cleanDocument+doc, "Morning brief"); len(findings) == 0 {
			t.Errorf("admit accepted the uppercased construct %q", doc)
		}
	}
}

// And the case-folding is NARROW: a JavaScript identifier matched loosely would
// fire on prose that merely reads like it, and this check false-positives
// readily enough already.
func TestAdmitDoesNotCaseFoldAJavaScriptIdentifier(t *testing.T) {
	if findings, _ := admit(cleanDocument+"<!-- the xmlhttprequest era is over -->", "Morning brief"); len(findings) != 0 {
		t.Errorf("admit refused prose that merely resembles an identifier: %v", findings)
	}
}

// Every view the Go catalog names has a source directory the frontend build
// produces a document for.
//
// The basename is spelled in three uncoordinated places — this catalog's URI,
// the directory under frontend/src/mcp-apps, and the file nginx serves — and a
// rename on one side degraded honestly but only at RUN time: the view simply
// stopped being held, in a deployment, with the reason in a log line. This
// turns that into a build failure, which is the shape review-loop rule 2 asks
// for: derive the obligation from the tree rather than maintain a list.
func TestEveryCatalogViewHasASourceTheFrontendBuilds(t *testing.T) {
	for _, v := range catalog {
		name := strings.TrimSuffix(filepath.Base(v.uri), ".html")
		entry := filepath.Join("..", "..", "..", "..", "..",
			"frontend", "src", "mcp-apps", name, "index.html")
		if _, err := os.Stat(entry); err != nil {
			t.Errorf("the catalog publishes %s, so the api will fetch /mcp-apps/%s.html — but %s does not exist, "+
				"so the build produces no such document and the view is never served: %v", v.uri, name, entry, err)
		}
	}
}

// And the inverse: a view directory nobody publishes builds a document the web
// tier serves and nothing ever reads — the shape a half-finished or
// half-removed view leaves behind.
func TestEveryBuiltViewIsNamedByTheCatalog(t *testing.T) {
	sources := filepath.Join("..", "..", "..", "..", "..", "frontend", "src", "mcp-apps")
	entries, err := os.ReadDir(sources)
	if err != nil {
		t.Fatalf("reading the view sources: %v", err)
	}
	published := map[string]bool{}
	for _, v := range catalog {
		published[strings.TrimSuffix(filepath.Base(v.uri), ".html")] = true
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(sources, entry.Name(), "index.html")); err != nil {
			continue
		}
		if !published[entry.Name()] {
			t.Errorf("frontend/src/mcp-apps/%s builds a document no catalog entry names, so the web tier "+
				"serves it and nothing ever reads it", entry.Name())
		}
	}
}
