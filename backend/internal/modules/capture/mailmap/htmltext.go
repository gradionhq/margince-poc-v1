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
var skippedContent = map[atom.Atom]bool{
	atom.Style:    true,
	atom.Script:   true,
	atom.Head:     true,
	atom.Title:    true,
	atom.Noscript: true,
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
type textWriter struct {
	out     strings.Builder
	pending int
	spaced  bool
}

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
	if strings.Contains(text, href) {
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

func (a *anchorState) open(href string) {
	if a.depth == 0 {
		a.href = href
		a.text.Reset()
	}
	a.depth++
}

func (a *anchorState) close(w *textWriter) {
	a.depth--
	if a.depth > 0 {
		return
	}
	w.space()
	w.write(linkText(a.text.String(), a.href))
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
	w := &textWriter{}
	z := html.NewTokenizer(strings.NewReader(src))
	a := &anchorState{}
	for {
		switch z.Next() {
		case html.ErrorToken:
			// The tokenizer reports malformed markup as tokens, so an error
			// here is the end of the document rather than a parse failure.
			a.flush(w)
			return tidy(w.String())
		case html.TextToken:
			writeText(w, a, collapse(string(z.Text())))
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			if skippedContent[token.DataAtom] {
				skipContent(z, token.DataAtom)
				continue
			}
			if token.DataAtom == atom.A && token.Type == html.StartTagToken {
				a.open(attrValue(token, "href"))
				continue
			}
			openTag(w, token.DataAtom)
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := atom.Lookup(name)
			if tag == atom.A && a.depth > 0 {
				a.close(w)
				continue
			}
			closeTag(w, tag)
		}
	}
}

// writeText routes a text node to the anchor being built, or to the document.
func writeText(w *textWriter, a *anchorState, text string) {
	if text == "" {
		return
	}
	if a.depth > 0 {
		if a.text.Len() > 0 {
			a.text.WriteString(" ")
		}
		a.text.WriteString(text)
		return
	}
	w.space()
	w.write(text)
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

// skipContent consumes an element's content up to its matching end tag, so a
// stylesheet or a script contributes nothing.
func skipContent(z *html.Tokenizer, tag atom.Atom) {
	depth := 1
	for depth > 0 {
		switch z.Next() {
		case html.ErrorToken:
			return
		case html.StartTagToken:
			if name, _ := z.TagName(); atom.Lookup(name) == tag {
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
