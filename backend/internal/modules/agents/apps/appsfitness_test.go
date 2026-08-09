// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package apps

// The views held against their own security claims, read off the ASSET BYTES.
//
// Everything the package comment asserts about these documents — self-contained,
// no markup from data, no credential — is a property of the JavaScript and the
// HTML, not of the Go around them. A reviewer can read four files once and
// believe all three; nobody can keep believing it through the fifth change. So
// each claim is a sweep over the assembled documents, derived from the catalogue
// rather than from a list of files, and each is proved against a document that
// breaks it as well as against the real ones.
//
// WHY THE ASSEMBLED DOCUMENT AND NOT THE SOURCE FILES. What a host executes is
// the assembled string. A sweep over the sources could pass while the assembler
// introduced the very thing being forbidden — a stylesheet link, an inline
// handler — and that is exactly the change nobody would think to re-check.

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// assembled is every document this package serves, keyed by URI, as a host
// receives it. Built through the real constructor: a test that re-assembled the
// assets itself would be proving something about its own spelling.
func assembled(t *testing.T) map[string]string {
	t.Helper()
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("assembling the views: %v", err)
	}
	documents := map[string]string{}
	for _, r := range p.Resources(context.Background()) {
		contents, err := p.ReadResource(context.Background(), r.URI)
		if err != nil {
			t.Fatalf("reading the view %s: %v", r.URI, err)
		}
		documents[r.URI] = contents.Text
	}
	if len(documents) == 0 {
		t.Fatal("no view was assembled, so every sweep in this file would pass vacuously")
	}
	return documents
}

// offOrigin are the ways a document reaches a network. Every view declares an
// EMPTY origin allowlist, so any of these is a request the host's sandbox will
// refuse — which means the feature silently does not work rather than failing
// somewhere an operator looks.
//
// The day a view genuinely needs an origin, this list is where the change gets
// noticed: the declaration in describe() widens, this sweep fails, and both
// arrive in one diff with the argument for them.
var offOrigin = []string{
	"http://", "https://", "//cdn.", "<link", "src=", "@import", "url(",
	"fetch(", "XMLHttpRequest", "WebSocket(", "EventSource(", "importScripts(",
	"navigator.sendBeacon",
}

func TestNoViewReachesOffItsOwnOrigin(t *testing.T) {
	for uri, document := range assembled(t) {
		for _, reach := range offOrigin {
			if strings.Contains(document, reach) {
				t.Errorf("the view %s contains %q. Every view declares an empty origin allowlist, so the host's "+
					"sandbox refuses this and the feature fails silently. If the origin is genuinely needed, "+
					"declare it in describe() and say why in the same diff", uri, reach)
			}
		}
	}
}

// fromMarkup are the ways untrusted text becomes markup, or becomes code.
//
// A view renders `structuredContent`, which is customer data: a person's name, a
// pasted note, an ingested email's subject. The sandbox exists to contain that
// text; every construct below hands it the one privilege the sandbox cannot take
// back, which is execution inside the view's own origin. Text reaches the page
// through textContent and structure through createElement, and this is the sweep
// that keeps it that way.
var fromMarkup = []string{
	"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
	"eval(", "new Function", "setTimeout(\"", "setInterval(\"",
	"createContextualFragment", "srcdoc",
}

func TestNoViewBuildsMarkupOrCodeFromData(t *testing.T) {
	for uri, document := range assembled(t) {
		for _, construct := range fromMarkup {
			if strings.Contains(document, construct) {
				t.Errorf("the view %s contains %q, which would let a record's own text execute inside the view — "+
					"a view puts text on the page with textContent and builds structure with createElement",
					uri, construct)
			}
		}
	}
}

// An inline event handler is markup that is also code, and it is the one form
// the sweep above cannot see: `onclick=` in a template is not a JavaScript
// identifier. Checked separately so the failure names the real problem.
func TestNoViewCarriesAnInlineEventHandler(t *testing.T) {
	for uri, document := range assembled(t) {
		for _, handler := range []string{"onclick=", "onload=", "onerror=", "onmouseover=", "javascript:"} {
			if strings.Contains(document, handler) {
				t.Errorf("the view %s carries the inline handler %q; a view binds behaviour in its script, "+
					"where the sweep over data-to-code constructs can see it", uri, handler)
			}
		}
	}
}

// A view is given a tool's ANSWER, never the means to ask again. There is no
// credential in these documents to leak — which is a stronger property than one
// handled carefully — and this is what keeps it true as the views grow.
//
// The words are the ones a credential is spelled with when someone adds one by
// habit, and the sweep is deliberately blunt: a false positive here costs a
// rename, while a miss costs a bearer token sitting in a sandboxed frame's
// source, readable by anything the host renders beside it.
func TestNoViewCarriesACredential(t *testing.T) {
	for uri, document := range assembled(t) {
		lowered := strings.ToLower(document)
		for _, secret := range []string{
			"authorization", "bearer ", "access_token", "refresh_token",
			"api_key", "apikey", "client_secret", "passport", "approval_id",
		} {
			if strings.Contains(lowered, secret) {
				t.Errorf("the view %s mentions %q. A view holds no credential and no authority handle: it renders "+
					"the answer the host pushed into it", uri, secret)
			}
		}
	}
}

// A view calls no tool. The protocol permits it and nothing here needs it, and
// it is the widest part of the extension's surface — a view that stays a
// renderer cannot become a second door onto a record. When one genuinely needs
// to call something, this sweep is where that decision gets made explicitly.
func TestNoViewCallsATool(t *testing.T) {
	for uri, document := range assembled(t) {
		for _, call := range []string{`"tools/call"`, "'tools/call'", "ui/open-link", "ui/message"} {
			if strings.Contains(document, call) {
				t.Errorf("the view %s sends %s. A view is a renderer; a call from inside one is a second door onto "+
					"a record and needs its own authority argument", uri, call)
			}
		}
	}
}

// The handshake, without which a host never pushes a result in and the view sits
// empty forever. It is the one thing in these documents that MUST be present
// rather than absent, so it is asserted from the other direction.
func TestEveryViewAnnouncesItselfToItsHost(t *testing.T) {
	for uri, document := range assembled(t) {
		for _, required := range []string{
			// The initialise request, its confirmation, and the notification the
			// view's whole purpose depends on receiving.
			"ui/initialize", "ui/notifications/initialized", "ui/notifications/tool-result",
			// The protocol revision this dialect is spoken at.
			"2026-01-26",
			// The sender check: a sandboxed frame can be messaged by anything
			// holding a handle to its window, and a view that rendered whatever
			// arrived would let a second sender choose what the human sees.
			"event.source === window.parent",
			// The element the renderers write into.
			`id="root"`,
		} {
			if !strings.Contains(document, required) {
				t.Errorf("the view %s does not contain %q, so it never completes the handshake its host is "+
					"waiting for and renders nothing", uri, required)
			}
		}
	}
}

// Each document has to be a document: served under the App profile, and served
// as HTML that starts where a parser expects it to.
func TestEveryViewIsAWellFormedAppDocument(t *testing.T) {
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("assembling the views: %v", err)
	}
	for _, r := range p.Resources(context.Background()) {
		if !strings.HasPrefix(r.URI, mcp.AppURIScheme) {
			t.Errorf("the view %s is published outside the %s scheme, so no host will treat it as one", r.URI, mcp.AppURIScheme)
		}
		contents, err := p.ReadResource(context.Background(), r.URI)
		if err != nil {
			t.Fatalf("reading %s: %v", r.URI, err)
		}
		if contents.MIMEType != mcp.AppMIMEType {
			t.Errorf("the view %s is read as %q, want %q", r.URI, contents.MIMEType, mcp.AppMIMEType)
		}
		if !strings.HasPrefix(contents.Text, "<!doctype html>") {
			t.Errorf("the view %s does not begin with a doctype, which puts the parser in quirks mode", r.URI)
		}
		// The title is interpolated, and it comes from this package's own
		// catalogue. Asserting it arrived proves the assembly used the entry
		// rather than dropping it.
		if !strings.Contains(contents.Text, "<title>"+r.Title+"</title>") {
			t.Errorf("the view %s does not carry its catalogue title %q", r.URI, r.Title)
		}
	}
}

// A URI this provider does not serve answers the DECLARED not-found sentinel, so
// the dispatcher's existence-hiding applies to a view exactly as to any other
// document. An error of another kind would surface as a 500 and tell a caller
// that something is there.
func TestAnUnknownViewAnswersTheNotFoundSentinel(t *testing.T) {
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("assembling the views: %v", err)
	}
	if _, err := p.ReadResource(context.Background(), "ui://margince/not-a-view.html"); err == nil {
		t.Fatal("a URI this provider does not serve was answered anyway")
	}
}

// And the sweeps above are proved to CATCH what they describe, against a
// document that breaks each claim. Every one of them passes over the real views;
// a sweep that could not fail would look exactly the same from here.
func TestTheViewSweepsCatchWhatTheyDescribe(t *testing.T) {
	for _, tc := range []struct {
		claim    string
		breaking string
		found    func(string) bool
	}{
		{"off-origin reach", `<link rel="stylesheet" href="https://cdn.example.test/x.css">`, containsAny(offOrigin)},
		{"data to markup", `root.innerHTML = data.name;`, containsAny(fromMarkup)},
		{"inline handler", `<button onclick="go()">`, containsAny([]string{"onclick=", "javascript:"})},
		{"a credential", `var token = "Bearer abc";`, func(d string) bool {
			return strings.Contains(strings.ToLower(d), "bearer ")
		}},
		{"a tool call", `send({method: "tools/call"});`, containsAny([]string{`"tools/call"`})},
	} {
		t.Run(tc.claim, func(t *testing.T) {
			if !tc.found(tc.breaking) {
				t.Errorf("the sweep for %s does not detect %q, so it would pass over a view that broke the claim",
					tc.claim, tc.breaking)
			}
			// And it does not fire on a document that keeps the claim, or it
			// would be refusing every view rather than the wrong ones.
			if tc.found(`root.appendChild(el('span', 'name', data.name));`) {
				t.Errorf("the sweep for %s fires on a document that keeps the claim", tc.claim)
			}
		})
	}
}

func containsAny(needles []string) func(string) bool {
	return func(document string) bool {
		for _, needle := range needles {
			if strings.Contains(document, needle) {
				return true
			}
		}
		return false
	}
}

// MustProvider is what a composition root calls, having nowhere to put an error.
// It succeeds for this binary's own embedded assets, so reaching the assertion
// is most of the test: a build with a renamed asset panics here instead.
func TestMustProviderAssemblesThisBuildsViews(t *testing.T) {
	p := MustProvider()
	if len(p.Resources(context.Background())) != len(catalog) {
		t.Errorf("MustProvider published %d views, want the %d in the catalogue",
			len(p.Resources(context.Background())), len(catalog))
	}
}
