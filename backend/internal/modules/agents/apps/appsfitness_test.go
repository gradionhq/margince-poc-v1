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
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
		// Comments stripped: every sweep below asks what the document DOES, and
		// a comment explaining a forbidden construct contains the construct.
		// See code.go — the required-call-site sweep is unsound without this,
		// because a deleted call leaves an explanation that satisfies a
		// substring check.
		documents[r.URI] = Code(contents.Text)
	}
	if len(documents) == 0 {
		t.Fatal("no view was assembled, so every sweep in this file would pass vacuously")
	}
	return documents
}

// assembledRaw is every served document with its commentary intact. See
// TestNoViewReachesOffItsOwnOrigin for why one sweep needs the raw bytes.
func assembledRaw(t *testing.T) map[string]string {
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
		t.Fatal("no view was assembled, so this sweep would pass vacuously")
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
	"navigator.sendBeacon", "RTCPeerConnection", "location.replace", "navigator.geolocation",
}

// This sweep reads the RAW document, not the stripped one — the only sweep here
// that does, and deliberately.
//
// A scheme separator IS a comment opener to the stripper: an unquoted
// `https://host/x` leaves `https:` behind and takes `//host/x` — and the rest of
// that line — with it, so the very token being searched for is what the strip
// destroys. Reading raw cannot miss that. The cost is that prose naming a URL
// would fail this sweep, which is a false positive a reviewer fixes by rewording,
// and is the direction to be wrong in.
func TestNoViewReachesOffItsOwnOrigin(t *testing.T) {
	for uri, document := range assembledRaw(t) {
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
//
// WHAT THIS LIST CANNOT DO, said plainly so nobody reads it as a proof. A
// substring scan catches the spellings it knows. It does not catch a computed
// property (`node['inner' + 'HTML']`), an aliased reference, or a sink invented
// after this list was written. So it is a RATCHET, not a boundary: it makes the
// known sinks impossible to add by habit, and the review of a view's renderer is
// still the thing that decides whether it is safe. The list grows when the
// platform grows one — setHTML and setHTMLUnsafe are here because they shipped
// after the obvious ones and would otherwise have been a silent hole.
var fromMarkup = []string{
	"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
	"setHTML", "setHTMLUnsafe", "createContextualFragment", "DOMParser",
	"eval(", "new Function", "setTimeout(\"", "setInterval(\"",
	"srcdoc", "location.href", "location.assign", "import(",
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
			"token", "secret", "credential",
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
		for _, call := range []string{`"tools/call"`, "'tools/call'", "tools/", "ui/open-link", "ui/message"} {
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
			//
			// Spelled as the CALL SITE spells them, not as bare tokens: a token
			// is satisfied by a comment mentioning it, so a sweep on
			// "ui/notifications/initialized" alone would stay green after the
			// send was deleted and the explanation left behind. Which is not
			// hypothetical — the prose in these very assets had to be reworded
			// once already for tripping the sweep above.
			`method: 'ui/initialize'`,
			`method: 'ui/notifications/initialized'`,
			`=== 'ui/notifications/tool-result'`,
			// The protocol revision this dialect is spoken at.
			"2026-01-26",
			// The sender check: a sandboxed frame can be messaged by anything
			// holding a handle to its window, and a view that rendered whatever
			// arrived would let a second sender choose what the human sees.
			"event.source !== window.parent",
			// And the origin pinning that the sender check cannot provide: the
			// host's origin is unknowable until it answers, learned from that
			// answer, and then required of every later message and used as the
			// target of every later send.
			"hostOrigin = event.origin",
			"event.origin === hostOrigin",
			// The id MATCH, not merely that an initialise was sent: a bridge that
			// accepted any message carrying a result as the handshake response
			// would keep every other string in this list and still be wrong.
			"message.id === initializeID",
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
	_, err = p.ReadResource(context.Background(), "ui://margince/not-a-view.html")
	if err == nil {
		t.Fatal("a URI this provider does not serve was answered anyway")
	}
	// The DECLARED sentinel, not merely some error. Anything else surfaces as a
	// 500 and tells the caller something is there — which is the whole point of
	// this assertion, and an `err != nil` check would pass against it.
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("an unserved view answered %v, want the declared not-found sentinel", err)
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

// The comment stripper, which every sweep above now depends on. A stripper that
// removed too much would hide exactly the constructs they exist to find, so its
// erring-toward-keeping rule is asserted rather than described.
func TestCodeSeparatesWhatADocumentDoesFromWhatItSays(t *testing.T) {
	for _, tc := range []struct {
		name  string
		asset string
		want  string
	}{
		{"a line comment goes", "keep();\n// innerHTML is forbidden\nkeep2();", "keep();\n\nkeep2();"},
		{"a block comment goes", "a();/* eval( */b();", "a();\nb();"},
		{"a trailing line comment goes", "a(); // note", "a(); \n"},
		{
			// The case that makes a naive stripper dangerous: a `//` inside a
			// string is not a comment, and eating the rest of the line would
			// hide whatever followed it.
			name:  "a slash pair inside a string is not a comment",
			asset: `var u = "x//y"; innerHTML;`,
			want:  `var u = "x//y"; innerHTML;`,
		},
		{
			name:  "an escaped quote does not end the string",
			asset: `var s = 'it\'s // fine'; eval(x);`,
			want:  `var s = 'it\'s // fine'; eval(x);`,
		},
		{
			name:  "a template literal is a string",
			asset: "var t = `a // b`; innerHTML;",
			want:  "var t = `a // b`; innerHTML;",
		},
		{
			// Erring toward keeping: an unterminated block comment must not
			// swallow the remainder of the asset.
			name:  "an unterminated block comment keeps what follows",
			asset: "a(); /* oops innerHTML",
			want:  "a(); \n oops innerHTML",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Code(tc.asset); got != tc.want {
				t.Errorf("Code(%q) =\n  %q\nwant\n  %q", tc.asset, got, tc.want)
			}
		})
	}
}

// And the whole reason it exists: a required call site, deleted with its
// explanation left behind, must not satisfy the handshake sweep.
func TestADeletedCallSiteIsNotSatisfiedByItsOwnComment(t *testing.T) {
	const removed = "// send({ method: 'ui/notifications/initialized', params: {} }) — removed"
	if strings.Contains(Code(removed), "method: 'ui/notifications/initialized'") {
		t.Error("a commented-out call still reads as a call, so the handshake sweep would stay green after " +
			"the send was deleted")
	}
	const present = "send({ method: 'ui/notifications/initialized', params: {} });"
	if !strings.Contains(Code(present), "method: 'ui/notifications/initialized'") {
		t.Error("a real call site was stripped, which would fail the handshake sweep against a correct view")
	}
}

// The opening message is the only one sent to a wildcard target, and it must
// stay that way.
//
// A view cannot know its host's origin before the host answers — reading it
// throws cross-origin — so the specification prescribes '*' there. Every LATER
// send is pinned to the origin the answer arrived from, which is the control a
// static scan of the wildcard cannot see. This asserts the pinning exists rather
// than the wildcard's absence, because the wildcard is correct exactly once.
func TestOnlyTheOpeningMessageIsSentToAWildcardTarget(t *testing.T) {
	for uri, document := range assembled(t) {
		// The target is a VARIABLE, resolved per send. A literal '*' at the
		// postMessage call would mean every message goes to any origin.
		if !strings.Contains(document, "window.parent.postMessage({ jsonrpc: '2.0', ...message }, target)") {
			t.Errorf("the view %s does not send through a resolved target, so its later messages are not pinned "+
				"to the origin the host answered from", uri)
		}
		// Every spelling of a literal wildcard target, not just the one that was
		// there when this was written: quotes either way, and any spacing before
		// the closing paren.
		for _, wildcard := range []string{`, '*')`, `, "*")`, `,'*')`, `,"*")`, `, '*' )`, `, "*" )`} {
			if strings.Contains(document, wildcard) {
				t.Errorf("the view %s posts to a literal wildcard target (%s), so a message meant for its host "+
					"would be delivered to whatever origin the parent frame currently holds", uri, wildcard)
			}
		}
	}
}

// Code cannot lex JavaScript, and every sweep in this file depends on it. So the
// assets are held to the constructs it CAN read: a regex literal carrying a quote
// or a `/*`, or a line terminator it does not know, desynchronises the scan and
// hides whatever follows — silently, in the one direction that matters.
//
// This is the check that turns that latent hole into a loud one. An asset that
// trips it is not necessarily unsafe; it is unreadable to the sweeps, which from
// a gate's point of view is the same thing.
func TestEveryAssetStaysWithinWhatTheStripperCanRead(t *testing.T) {
	for _, name := range []string{"bridge.js", "app.css", "accountbrief.js", "relationshipmap.js"} {
		raw, err := assets.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if why := AssumptionsHold(name, string(raw)); why != "" {
			t.Errorf("%s %s — rewrite it, or teach code.go the construct before relying on the sweeps here", name, why)
		}
	}
}

// And the check itself detects each construct, or it is a guard nobody has seen
// fire. Every input below is one the browser reads differently from the scanner.
func TestTheStripperRefusesWhatItCannotLex(t *testing.T) {
	for _, tc := range []struct{ name, asset string }{
		{"a regex literal carrying a quote desynchronises the scan", `s.replace(/"/g, ''); var u = "x";`},
		{
			// The case a residue check cannot see: two regex quotes cancel, so the
			// scan ends balanced while the text between them was read as code.
			"two regex quotes cancel, leaving balanced state and a desynchronised middle",
			`a.replace(/"/g, ''); var s = "keep // this"; b.replace(/"/g, '');`,
		},
		{"a regex carrying a comment opener", `s.split(/[/*]/);`},
		{"a bare division, which cannot be told from a regex", `var r = total / count;`},
		{"a lone carriage return ends a line comment for a browser", "// note\rel.innerHTML = payload;"},
		{"U+2028 ends a line comment for a browser, invisibly", "// note el.innerHTML = payload;"},
		{"U+2029 likewise", "// note el.innerHTML = payload;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if AssumptionsHold("x.js", tc.asset) == "" {
				t.Errorf("AssumptionsHold accepted %q, which the scanner reads differently from a browser", tc.asset)
			}
		})
	}
	// And it accepts what it genuinely can read, or it would refuse every asset.
	if why := AssumptionsHold("x.js", "var s = 'it\\'s // fine'; /* block */ el.textContent = x;\n"); why != "" {
		t.Errorf("AssumptionsHold refused a script it can read: %s", why)
	}
	// A stylesheet keeps its shorthand slash: CSS has no regex literals, so the
	// rule that makes a script's slash dangerous does not apply to one.
	if why := AssumptionsHold("x.css", "body { font: 14px/1.5 sans-serif; } /* fine */\n"); why != "" {
		t.Errorf("AssumptionsHold refused a stylesheet for an ordinary shorthand slash: %s", why)
	}
}

// The policy on the READ, asserted against the REAL provider.
//
// This is the assertion whose absence let a defect through: with only the
// transport test's stub supplying `UI` on its own contents, deleting
// `UI: sandbox()` from ReadResource left every test green — and a host that
// fetched a view by URI without listing first would have received a document with
// no sandbox policy at all, which is the exact prefetch case the policy was moved
// onto the contents for.
func TestTheRealProviderSendsItsPolicyWithTheDocument(t *testing.T) {
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("assembling the views: %v", err)
	}
	for _, r := range p.Resources(context.Background()) {
		contents, err := p.ReadResource(context.Background(), r.URI)
		if err != nil {
			t.Fatalf("reading %s: %v", r.URI, err)
		}
		if contents.UI == nil {
			t.Errorf("the view %s is read WITHOUT its sandbox policy, so a host that fetched it by URI has a "+
				"document and no rules to sandbox it under", r.URI)
			continue
		}
		// And it must be the SAME policy the catalogue advertised, since the two
		// reach the host through different calls and a host may make either.
		//
		// Compared through the rendered JSON rather than as structs: the policy
		// holds slices, so it is not comparable with ==, and what has to agree is
		// what the host READS rather than how it is spelled in Go.
		if r.UI == nil {
			t.Errorf("the view %s is read with a policy but advertised without one", r.URI)
			continue
		}
		advertised, err := json.Marshal(r.UI)
		if err != nil {
			t.Fatalf("encoding the advertised policy for %s: %v", r.URI, err)
		}
		served, err := json.Marshal(contents.UI)
		if err != nil {
			t.Fatalf("encoding the served policy for %s: %v", r.URI, err)
		}
		if string(advertised) != string(served) {
			t.Errorf("the view %s advertises %s but is read with %s — a host is told one policy and sent another",
				r.URI, advertised, served)
		}
	}
}
