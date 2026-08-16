// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Rendering an HTML-only mail as text. A tag-strip by regular expression gets
// three things wrong that a reader notices at once: `&auml;` stays spelled out,
// every block runs into the next because tags became spaces, and the contents
// of <style> and <script> arrive as body text. What this produces is stored, so
// it is also what search indexes and what a model reads.

// Elements whose CONTENT is not text of the message. Skipped whole rather than
// merely unstyled: a stylesheet's rules are the largest single source of
// nonsense in a stripped newsletter.
// <noscript> is deliberately NOT here: a mail client renders with scripting
// off, so its content is exactly what the reader sees.
var skippedContent = map[atom.Atom]bool{
	atom.Style:    true,
	atom.Script:   true,
	atom.Head:     true,
	atom.Title:    true,
	atom.Template: true,
	atom.Svg:      true,
}

// Elements that end the line they sit on.
var lineBreakers = map[atom.Atom]bool{
	atom.Br: true, atom.Tr: true, atom.Li: true, atom.Dt: true, atom.Dd: true,
	atom.Caption: true, atom.Option: true,
}

// Elements that stand apart from what surrounds them: a blank line either side.
var paragraphBreakers = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Blockquote: true, atom.Section: true,
	atom.Article: true, atom.Header: true, atom.Footer: true, atom.Table: true,
	atom.Ul: true, atom.Ol: true, atom.Dl: true, atom.Pre: true, atom.Hr: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true,
	atom.H6: true, atom.Form: true, atom.Fieldset: true, atom.Figure: true,
}

// textWriter assembles the rendered text. Breaks are requested rather than
// written, so a run of nested block elements collapses to one break instead of
// one per element, and a break before any text is dropped.
// How much rendered text is worth building. Only maxBodyLen of it is stored, so
// a sender cannot make this hold a hundred megabytes of repeated text in memory
// to have all but the first few thousand characters thrown away. The margin
// above maxBodyLen leaves tidy() something to trim without cutting the excerpt
// short of what gets stored.
const maxRenderedLen = 4 * maxBodyLen

// The largest single token worth buffering. A mail is written by a stranger and
// only maxBodyLen of it is stored, so no one token needs to be bigger than the
// whole excerpt; a token past this ends the walk with what was read so far.
const maxTokenLen = maxRenderedLen

type textWriter struct {
	out     strings.Builder
	pending int
	spaced  bool
}

// full reports that the excerpt is long enough. What follows would be truncated
// away, so rendering it buys nothing and costs whatever the sender chose.
func (w *textWriter) full() bool { return w.out.Len() >= maxRenderedLen }

func (w *textWriter) breakLine(n int) {
	if w.out.Len() == 0 {
		return
	}
	w.spaced = false
	if n > w.pending {
		w.pending = n
	}
}

func (w *textWriter) write(s string) {
	if s == "" {
		return
	}
	if w.pending > 0 {
		w.spaced = false
		w.out.WriteString(strings.Repeat("\n", w.pending))
		w.pending = 0
	}
	w.flushSpace(s)
	w.out.WriteString(s)
}

// space keeps a word boundary an inline tag would otherwise close up:
// "<b>Angebot</b><i>2026</i>" is two words, not one. Requested rather than
// written, so that punctuation following an element still closes the sentence
// up: the space is dropped when the next thing written starts with it.
func (w *textWriter) space() {
	if w.out.Len() == 0 || w.pending > 0 {
		return
	}
	w.spaced = true
}

// Punctuation that closes up against the word before it. A source newline
// between "</a>" and "." is insignificant whitespace, and writing it through
// leaves the sentence ending with a gap before its full stop.
const closingPunctuation = ".,;:!?)]}"

func (w *textWriter) flushSpace(next string) {
	if !w.spaced {
		return
	}
	w.spaced = false
	if next != "" && strings.ContainsAny(next[:1], closingPunctuation) {
		return
	}
	if !strings.HasSuffix(w.out.String(), " ") {
		w.out.WriteString(" ")
	}
}

func (w *textWriter) String() string { return w.out.String() }

// collapse folds HTML's insignificant whitespace into single spaces. A newline
// in the source carries no meaning — only the tags do — so keeping it would
// break a line where the sender never did.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// edgeSpace reports whether a text node began or ended on whitespace. It is the
// only evidence of a word boundary across an inline tag: "<b>Marg</b><b>ince</b>"
// is one word and "<b>Angebot</b> <i>2026</i>" is two, and the tags say nothing
// about which. Guessing a space corrupts the word for search as well as reading.
func edgeSpace(s string) (leading, trailing bool) {
	if s == "" {
		return false, false
	}
	trimmed := strings.TrimLeft(s, " \t\r\n\f")
	if trimmed == "" {
		return true, true
	}
	return trimmed != s, strings.TrimRight(s, " \t\r\n\f") != s
}

// The longest address worth writing out. A tracking link runs to thousands of
// characters of opaque query string, and the stored body is capped: writing one
// out spends the reader's excerpt — and the model's — on a URL nobody can read,
// pushing out the sentence that says what the mail wanted.
const maxWrittenHref = 120

// linkText is what an anchor contributes: its own text, and the address after
// it when the two differ. A link whose text already IS the address is written
// once, and a mailto or a fragment adds nothing a reader can act on.
func linkText(text, href string) string {
	text = collapse(text)
	href = strings.TrimSpace(href)
	if href == "" || text == "" {
		return text
	}
	lower := strings.ToLower(href)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return text
	}
	if len(href) > maxWrittenHref || strings.Contains(text, href) {
		return text
	}
	return text + " (" + href + ")"
}

func attrValue(token html.Token, name string) string {
	for _, attr := range token.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

// anchorState buffers the text inside <a>…</a> so the address can follow it.
// The depth count means a stray </a> cannot end an anchor a nested one opened.
type anchorState struct {
	text  strings.Builder
	href  string
	depth int
}

// writePre emits preformatted text as it stands. Inside <pre> the whitespace IS
// the content — an invoice table read as one run of words has lost its columns.
func writePre(w *textWriter, raw string) {
	for i, line := range strings.Split(raw, "\n") {
		if i > 0 {
			w.pending = 1
			w.spaced = false
		}
		w.write(strings.TrimRight(line, " \t\r"))
	}
}

// open starts an anchor. An <a> inside an open <a> is malformed, and HTML
// recovery closes the first — so the outer link is written out before the inner
// one starts, rather than the inner address being dropped.
func (a *anchorState) open(w *textWriter, href string) {
	if a.depth > 0 {
		a.flush(w)
	}
	a.href = href
	a.text.Reset()
	a.depth++
}

func (a *anchorState) close(w *textWriter) {
	a.depth--
	if a.depth > 0 {
		return
	}
	// Spaced on both sides: an anchor sits inside a sentence, and the address
	// written after its text ends on a bracket that would otherwise run into
	// whatever word comes next.
	w.space()
	w.write(linkText(a.text.String(), a.href))
	w.space()
	a.text.Reset()
}

// flush writes an anchor the document never closed, so its text is not lost
// along with the missing end tag.
func (a *anchorState) flush(w *textWriter) {
	if a.depth == 0 {
		return
	}
	a.depth = 0
	w.space()
	w.write(linkText(a.text.String(), a.href))
}

// htmlToText renders an HTML mail body as the text a person would read: entity
// references decoded, blocks separated by real line breaks, list items marked,
// and the contents of style and script left out.
func htmlToText(src string) string {
	r := &renderer{w: &textWriter{}, a: &anchorState{}}
	z := html.NewTokenizer(strings.NewReader(src))
	// The loop's budget is checked between tokens, which bounds a document made
	// of many tags but not ONE token holding the whole body: the tokenizer
	// buffers a token entire before returning it, so an unbroken run of text
	// allocates the sender's chosen size no matter what is done with it after.
	// Capping the buffer moves the bound to where the memory is actually taken.
	z.SetMaxBuf(maxTokenLen)
	for !r.w.full() {
		switch token := z.Next(); token {
		case html.ErrorToken:
			// Malformed markup is reported as tokens rather than as an error,
			// so this is the end of the document: EOF, or a token past the
			// buffer cap. Both end the walk with what was read up to here,
			// which is the readable part of the message.
			return r.finish()
		case html.TextToken:
			r.text(string(z.Text()))
		case html.StartTagToken, html.SelfClosingTagToken:
			r.startTag(z, token == html.SelfClosingTagToken)
		case html.EndTagToken:
			name, _ := z.TagName()
			r.endTag(atom.Lookup(name))
		}
	}
	return r.finish()
}

// renderer is the walk's state: what has been written, the anchor being built,
// and whether the text arriving is preformatted.
type renderer struct {
	w   *textWriter
	a   *anchorState
	pre int
}

func (r *renderer) text(raw string) {
	// Inside <pre> the whitespace is the content — unless an anchor is open,
	// where the text is being buffered for the link rather than written.
	if r.pre > 0 && r.a.depth == 0 {
		writePre(r.w, raw)
		return
	}
	writeText(r.w, r.a, raw)
}

func (r *renderer) startTag(z *html.Tokenizer, selfClosing bool) {
	token := z.Token()
	if skippedContent[token.DataAtom] {
		// A self-closing form has no content, and looking for an end tag that
		// cannot come would swallow the rest of the message.
		if !selfClosing {
			skipContent(z, token.DataAtom)
		}
		return
	}
	if selfClosing {
		openTag(r.w, token.DataAtom)
		return
	}
	switch token.DataAtom {
	case atom.A:
		r.a.open(r.w, attrValue(token, "href"))
	case atom.Pre:
		r.pre++
		openTag(r.w, token.DataAtom)
	default:
		openTag(r.w, token.DataAtom)
	}
}

func (r *renderer) endTag(tag atom.Atom) {
	switch {
	case tag == atom.A && r.a.depth > 0:
		r.a.close(r.w)
	case tag == atom.Pre:
		if r.pre > 0 {
			r.pre--
		}
		closeTag(r.w, tag)
	default:
		closeTag(r.w, tag)
	}
}

// finish writes out an anchor the document never closed, so its text is not
// lost with the missing end tag.
func (r *renderer) finish() string {
	r.a.flush(r.w)
	return tidy(r.w.String())
}

// writeText routes a text node to the anchor being built, or to the document.
// A space is asked for only where the source had one, so an inline tag between
// two halves of a word does not split it.
func writeText(w *textWriter, a *anchorState, raw string) {
	if len(raw) > maxRenderedLen {
		raw = raw[:maxRenderedLen]
	}
	leading, trailing := edgeSpace(raw)
	text := collapse(raw)
	if text == "" {
		// Whitespace between two elements is still a word boundary.
		if leading {
			w.space()
		}
		return
	}
	if a.depth > 0 {
		if leading && a.text.Len() > 0 {
			a.text.WriteString(" ")
		}
		a.text.WriteString(text)
		return
	}
	if leading {
		w.space()
	}
	w.write(text)
	if trailing {
		w.space()
	}
}

// openTag writes what an element contributes before its content.
func openTag(w *textWriter, tag atom.Atom) {
	switch {
	case tag == atom.Li:
		w.breakLine(1)
		w.write("- ")
	case tag == atom.Td || tag == atom.Th:
		w.space()
	case paragraphBreakers[tag]:
		w.breakLine(2)
	case lineBreakers[tag]:
		w.breakLine(1)
	}
}

// closeTag ends the line an element occupied. A cell is separated by a space
// instead: a table row read one cell per line loses the row.
func closeTag(w *textWriter, tag atom.Atom) {
	switch {
	case tag == atom.Td || tag == atom.Th:
		w.space()
	case paragraphBreakers[tag]:
		w.breakLine(2)
	case lineBreakers[tag]:
		w.breakLine(1)
	}
}

// Where a skipped element ends even though the document never closed it. A
// mail that opens <head> and goes straight to <body> is ordinary, and reading
// to EOF looking for </head> loses the whole message.
var impliedClose = map[atom.Atom]atom.Atom{atom.Head: atom.Body}

// skipContent consumes an element's content up to its matching end tag, so a
// stylesheet or a script contributes nothing. It also stops at the tag that
// implicitly closes the element, because HTML does not require every end tag
// and a mail is under no obligation to be well-formed.
func skipContent(z *html.Tokenizer, tag atom.Atom) {
	closer, hasCloser := impliedClose[tag]
	depth := 1
	for depth > 0 {
		switch z.Next() {
		case html.ErrorToken:
			return
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch found := atom.Lookup(name); {
			case hasCloser && found == closer:
				return
			case found == tag:
				depth++
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); atom.Lookup(name) == tag {
				depth--
			}
		}
	}
}

// tidy trims the trailing space a break may have left on a line and caps a run
// of blank lines at one, which is what a paragraph gap is.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
