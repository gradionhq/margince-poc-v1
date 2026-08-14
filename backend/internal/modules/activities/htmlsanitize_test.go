// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// What a formatted business email is made of survives intact. A sanitiser that
// eats the formatting is one nobody can use, and the composer above it would
// then be a toolbar whose buttons do nothing.
func TestFormattingABusinessEmailNeedsSurvives(t *testing.T) {
	for name, markup := range map[string]string{
		"emphasis":   "<p>The <b>deadline</b> is <em>Friday</em>.</p>",
		"lists":      "<ul><li>One</li><li>Two</li></ul>",
		"paragraphs": "<p>First.</p><p>Second.</p>",
		"line break": "<p>One<br>Two</p>",
		"a quote":    "<blockquote>Their words.</blockquote>",
		"a rule":     "<p>Above</p><hr><p>Below</p>",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SanitizeOutboundHTML(markup)
			if err != nil {
				t.Fatalf("sanitizing failed: %v", err)
			}
			if got != markup {
				t.Fatalf("formatting changed:\n got %q\nwant %q", got, markup)
			}
		})
	}
}

// A remote image is a read receipt. This product refuses tracking pixels as a
// stated position, and a sender who embeds one has collected the signal the
// product declines to — so the element does not reach the wire.
func TestNoElementThatLoadsARemoteResourceSurvives(t *testing.T) {
	for name, markup := range map[string]string{
		"a tracking pixel": `<p>Hi</p><img src="https://track.test/o.gif?id=42" width="1" height="1">`,
		"a stylesheet":     `<link rel="stylesheet" href="https://track.test/s.css"><p>Hi</p>`,
		"a style block":    `<style>@import url(https://track.test/s.css)</style><p>Hi</p>`,
		"an iframe":        `<iframe src="https://track.test/frame"></iframe><p>Hi</p>`,
		"an object":        `<object data="https://track.test/x"></object><p>Hi</p>`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SanitizeOutboundHTML(markup)
			if err != nil {
				t.Fatalf("sanitizing failed: %v", err)
			}
			if strings.Contains(got, "track.test") {
				t.Fatalf("a remote resource survived: %q", got)
			}
			if !strings.Contains(got, "Hi") {
				t.Fatalf("the message text was lost with it: %q", got)
			}
		})
	}
}

// A script is not text a reader was meant to see, so it goes entirely — content
// included. Unwrapping it would paste the code into the message as prose.
func TestAScriptLeavesNothingBehind(t *testing.T) {
	got, err := SanitizeOutboundHTML(`<p>Before</p><script>alert(1)</script><p>After</p>`)
	if err != nil {
		t.Fatalf("sanitizing failed: %v", err)
	}
	if strings.Contains(got, "alert") || strings.Contains(got, "script") {
		t.Fatalf("a script survived in some form: %q", got)
	}
	if got != "<p>Before</p><p>After</p>" {
		t.Fatalf("the surrounding message changed: %q", got)
	}
}

// A link keeps its href only for the schemes a business email uses. Anything
// else becomes plain text carrying its own label — which is what the reader was
// going to read regardless.
func TestOnlyWebAndMailSchemesKeepTheirHref(t *testing.T) {
	for name, tc := range map[string]struct{ markup, want string }{
		"https": {`<a href="https://gradion.com">us</a>`, `<a href="https://gradion.com">us</a>`},
		"http":  {`<a href="http://gradion.com">us</a>`, `<a href="http://gradion.com">us</a>`},
		"mail":  {`<a href="mailto:x@y.test">write</a>`, `<a href="mailto:x@y.test">write</a>`},
		"javascript": {
			`<a href="javascript:alert(1)">click</a>`, `<a>click</a>`,
		},
		"data": {
			`<a href="data:text/html;base64,PHNjcmlwdD4=">click</a>`, `<a>click</a>`,
		},
		"uppercase javascript": {
			`<a href="JavaScript:alert(1)">click</a>`, `<a>click</a>`,
		},
		"whitespace-padded javascript": {
			`<a href="  javascript:alert(1)">click</a>`, `<a>click</a>`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SanitizeOutboundHTML(tc.markup)
			if err != nil {
				t.Fatalf("sanitizing failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Every attribute other than a safe href is dropped. An event handler is the
// obvious one, but style carries url() and a class means nothing in a mail
// client that never saw our stylesheet.
func TestEveryOtherAttributeIsDropped(t *testing.T) {
	got, err := SanitizeOutboundHTML(
		`<p onclick="alert(1)" style="background:url(https://track.test/x)" class="x" id="y">Text</p>`)
	if err != nil {
		t.Fatalf("sanitizing failed: %v", err)
	}
	if got != "<p>Text</p>" {
		t.Fatalf("an attribute survived: %q", got)
	}
}

// An element nobody allowed keeps its words. A sender whose <div> vanished
// still meant the sentence inside it, and silently deleting prose is worse than
// dropping a tag.
func TestAnUnknownElementKeepsItsText(t *testing.T) {
	for name, tc := range map[string]struct{ markup, want string }{
		"a div":           {`<div>Words</div>`, "Words"},
		"a table cell":    {`<table><tr><td>Cell</td></tr></table>`, "Cell"},
		"a custom tag":    {`<my-widget>Inside</my-widget>`, "Inside"},
		"a nested unwrap": {`<div><span>Deep <b>bold</b></span></div>`, "Deep <b>bold</b>"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SanitizeOutboundHTML(tc.markup)
			if err != nil {
				t.Fatalf("sanitizing failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Text that looks like markup stays text. Re-emitting it raw would let an input
// that was never parsed as an element become one on the way out.
func TestTextThatLooksLikeMarkupStaysText(t *testing.T) {
	got, err := SanitizeOutboundHTML(`<p>Use &lt;script&gt; carefully &amp; well</p>`)
	if err != nil {
		t.Fatalf("sanitizing failed: %v", err)
	}
	if got != "<p>Use &lt;script&gt; carefully &amp; well</p>" {
		t.Fatalf("escaping changed: %q", got)
	}
}

// A comment can hide markup from a reviewer reading the source while a client
// still parses it, and it carries nothing a recipient reads.
func TestCommentsDoNotSurvive(t *testing.T) {
	got, err := SanitizeOutboundHTML(`<p>A</p><!-- <script>alert(1)</script> --><p>B</p>`)
	if err != nil {
		t.Fatalf("sanitizing failed: %v", err)
	}
	if got != "<p>A</p><p>B</p>" {
		t.Fatalf("a comment left something behind: %q", got)
	}
}

// Empty in, empty out — and a plain-text send must not gain a markup part.
func TestEmptyMarkupStaysEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t"} {
		got, err := SanitizeOutboundHTML(in)
		if err != nil {
			t.Fatalf("sanitizing failed: %v", err)
		}
		if got != "" {
			t.Fatalf("%q produced %q", in, got)
		}
	}
}

// The filter runs BEFORE the signature and the footer are added, so what the
// allowlist judges is exactly what the caller sent — and the parts this product
// appends are never subject to a filter they would only ever pass.
//
// Asserted through signedHTML, which is the function the send path calls with
// the sanitizer's output: a tracking pixel in the caller's markup is gone, and
// the sign-off that arrives after it is intact.
func TestTheFilterRunsBeforeAnythingOfOursIsAdded(t *testing.T) {
	store := (&Store{}).WithSignature(&stubSignature{body: "Marek Janetzke"})

	safe, err := SanitizeOutboundHTML(
		`<p>Words</p><img src="https://track.test/o.gif">`)
	if err != nil {
		t.Fatalf("sanitizing failed: %v", err)
	}
	got, err := store.signedHTML(humanCtx(ids.NewV7()), safe, sendDeliverability{})
	if err != nil {
		t.Fatalf("signing the markup failed: %v", err)
	}
	if strings.Contains(got, "track.test") {
		t.Fatalf("a tracking pixel reached the signed body: %q", got)
	}
	if !strings.Contains(got, "Marek Janetzke") {
		t.Fatalf("the sign-off was lost: %q", got)
	}
	if !strings.Contains(got, "<p>Words</p>") {
		t.Fatalf("the message was lost: %q", got)
	}
}

// Metadata is not message text. A <title> unwrapped would move a document's
// title into the body as a sentence nobody wrote there.
func TestMetadataContentIsNotUnwrappedIntoTheMessage(t *testing.T) {
	got, err := SanitizeOutboundHTML(`<title>Secret subject</title><p>Visible</p>`)
	if err != nil {
		t.Fatalf("sanitizing failed: %v", err)
	}
	if got != "<p>Visible</p>" {
		t.Fatalf("metadata reached the message: %q", got)
	}
}

// Text a sender HID becomes visible, because the attribute that concealed it is
// not one that survives. Pinned rather than fixed: the alternative is deleting
// prose whenever an element is unfamiliar, which loses more than it protects.
// What this asserts is that the behaviour is known, not that it is desirable.
func TestHiddenTextBecomesVisibleAndThatIsTheKnownTrade(t *testing.T) {
	got, err := SanitizeOutboundHTML(`<div hidden>Hidden note</div><p>Visible</p>`)
	if err != nil {
		t.Fatalf("sanitizing failed: %v", err)
	}
	if got != "Hidden note<p>Visible</p>" {
		t.Fatalf("the unwrap rule changed: %q", got)
	}
}
