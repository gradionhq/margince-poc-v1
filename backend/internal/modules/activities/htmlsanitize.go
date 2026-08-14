// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// What of a caller's markup is allowed onto the wire.
//
// The html_body a caller supplies is transmitted to a recipient's mail client,
// not rendered in our own document, so this is not an XSS defence — a mail
// client is the party that decides what it will run. It is a defence against
// this product being the instrument of something else:
//
//   - A remote image is a read receipt. This product refuses tracking pixels as
//     a stated position (sequences-and-deliverability.md: "the only engagement
//     signal is a real reply"), and a sender who embeds one has collected the
//     signal the product declines to. So no element that loads a remote
//     resource survives — img, script, iframe, object, link, style.
//   - A javascript: or data: href is a payload wearing a link's clothes, and
//     the recipient sees only the text it carries.
//   - A form posts a recipient's answer to whoever the sender named.
//
// The rule is an ALLOWLIST, because the failure of a denylist is silent: an
// element nobody thought of arrives intact, and nothing says so. What survives
// is what a business email needs — emphasis, structure, links, line breaks —
// and everything else is reduced to the text it contained rather than dropped,
// because a sender whose markup vanished still meant the words inside it.
//
// Unwrapping has a consequence worth stating rather than discovering: text a
// sender HID becomes visible. A `hidden` div or a display:none paragraph loses
// the attribute that concealed it and keeps its words, so a message can arrive
// saying more than its author saw in their own composer. That is the honest
// trade — the alternative is silently deleting prose whenever an element is
// unfamiliar — and it is why the drop list holds the elements whose content was
// never message text at all rather than merely being styled out of sight.

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// allowedElements are the tags a formatted business email is made of. Anything
// else is unwrapped to its text: a <div> becomes its content, a <script>
// becomes nothing, because it had no text a reader was meant to see.
var allowedElements = map[atom.Atom]bool{
	atom.P: true, atom.Br: true,
	atom.B: true, atom.Strong: true, atom.I: true, atom.Em: true, atom.U: true,
	atom.Ul: true, atom.Ol: true, atom.Li: true,
	atom.A: true, atom.Blockquote: true, atom.Hr: true,
}

// droppedWholesale are the elements whose CONTENT is not text a reader was
// meant to see. Everything else keeps its children when the element itself is
// not allowed — a <div> of prose must not lose the prose.
var droppedWholesale = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Head: true,
	// Metadata, not message. A <title> is never text a recipient was meant to
	// read, and unwrapping it would move a document's title into the body as a
	// sentence nobody wrote there.
	atom.Title:  true,
	atom.Iframe: true, atom.Object: true, atom.Embed: true,
	atom.Form: true, atom.Input: true, atom.Button: true, atom.Select: true,
	atom.Textarea: true, atom.Template: true, atom.Noscript: true,
}

// SanitizeOutboundHTML reduces caller-supplied markup to the subset a business
// email needs, and returns an error rather than a repair when the input is not
// parseable as HTML at all.
//
// It never returns markup it did not build itself: every element and attribute
// in the output was written by this function from a value that passed the
// allowlist, so a construct that survives did so deliberately.
func SanitizeOutboundHTML(markup string) (string, error) {
	if strings.TrimSpace(markup) == "" {
		return "", nil
	}
	// A fragment, not a document: a mail body is body content, and parsing it
	// as a document would wrap it in html/head/body we would then have to strip.
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(markup), body)
	if err != nil {
		return "", fmt.Errorf("activities: the message markup does not parse as HTML: %w", err)
	}
	var out strings.Builder
	for _, node := range nodes {
		writeSanitized(&out, node)
	}
	return out.String(), nil
}

// writeSanitized renders one node and its children under the allowlist.
func writeSanitized(out *strings.Builder, node *html.Node) {
	switch node.Type {
	case html.TextNode:
		// Escaped on the way out, so text that looked like markup in the input
		// is text in the output rather than markup we did not check.
		out.WriteString(html.EscapeString(node.Data))
		return
	case html.ElementNode:
		writeSanitizedElement(out, node)
		return
	default:
		// Comments, doctypes and the rest carry nothing a recipient reads, and
		// a comment can hide markup from a reviewer while a client still parses
		// it. Their children are walked, because a comment has none.
		return
	}
}

func writeSanitizedElement(out *strings.Builder, node *html.Node) {
	if droppedWholesale[node.DataAtom] {
		return
	}
	if !allowedElements[node.DataAtom] {
		// Unwrap: keep what it said, drop what it was.
		writeChildren(out, node)
		return
	}
	tag := node.Data
	out.WriteString("<" + tag)
	if href, ok := safeHref(node); ok {
		out.WriteString(` href="` + html.EscapeString(href) + `"`)
	}
	out.WriteString(">")
	if !voidElement[node.DataAtom] {
		writeChildren(out, node)
		out.WriteString("</" + tag + ">")
	}
}

func writeChildren(out *strings.Builder, node *html.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		writeSanitized(out, child)
	}
}

// voidElement names the tags that take no closing tag. Writing one would put a
// stray </br> in the message.
var voidElement = map[atom.Atom]bool{atom.Br: true, atom.Hr: true}

// safeHref answers the one attribute that survives, and only on a link.
//
// http and https only, and mailto because a business email links an address as
// often as a page. Everything else — javascript:, data:, file:, a scheme
// nobody has heard of — is dropped, and the link becomes plain text carrying
// its own label, which is what the reader was going to read anyway.
func safeHref(node *html.Node) (string, bool) {
	if node.DataAtom != atom.A {
		return "", false
	}
	for _, attr := range node.Attr {
		if !strings.EqualFold(attr.Key, "href") {
			continue
		}
		trimmed := strings.TrimSpace(attr.Val)
		lowered := strings.ToLower(trimmed)
		for _, scheme := range []string{"http://", "https://", "mailto:"} {
			if strings.HasPrefix(lowered, scheme) {
				return trimmed, true
			}
		}
		return "", false
	}
	return "", false
}
