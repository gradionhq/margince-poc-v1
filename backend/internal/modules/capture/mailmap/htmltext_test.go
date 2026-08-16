// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

import "testing"

// What an HTML-only mail has to read like once stored. Every case here was a
// way the previous tag-strip produced text a person could not read: entities
// left spelled out, paragraphs run together, and stylesheet rules delivered as
// if the sender had written them.
func TestHTMLToText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "decodes entity references",
			html: "<p>Gr&uuml;&szlig;e aus M&#252;nchen &amp; Umgebung</p>",
			want: "Grüße aus München & Umgebung",
		},
		{
			name: "separates paragraphs with a blank line",
			html: "<p>Erste Frage.</p><p>Zweite Frage.</p>",
			want: "Erste Frage.\n\nZweite Frage.",
		},
		{
			name: "breaks a line at br",
			html: "Anna Berger<br>Kunde GmbH<br/>München",
			want: "Anna Berger\nKunde GmbH\nMünchen",
		},
		{
			name: "drops stylesheet rules",
			html: "<html><head><style>.x{color:red}</style></head><body><p>Angebot</p></body></html>",
			want: "Angebot",
		},
		{
			name: "drops script bodies",
			html: "<p>Angebot</p><script>var track = 1; document.write('x')</script>",
			want: "Angebot",
		},
		{
			name: "marks list items",
			html: "<p>Offen:</p><ul><li>Preis</li><li>Termin</li></ul>",
			want: "Offen:\n\n- Preis\n- Termin",
		},
		{
			name: "keeps a table row on one line",
			html: "<table><tr><td>Preis</td><td>1.200 EUR</td></tr><tr><td>Termin</td><td>KW 34</td></tr></table>",
			want: "Preis 1.200 EUR\nTermin KW 34",
		},
		{
			name: "writes the address after link text that differs",
			html: `<p>Die <a href="https://kunde.de/angebot">Unterlagen</a> liegen bereit.</p>`,
			want: "Die Unterlagen (https://kunde.de/angebot) liegen bereit.",
		},
		{
			name: "writes a self-describing link once",
			html: `<a href="https://kunde.de/angebot">https://kunde.de/angebot</a>`,
			want: "https://kunde.de/angebot",
		},
		{
			name: "leaves a mailto link as its text",
			html: `<a href="mailto:anna@kunde.de">Anna schreiben</a>`,
			want: "Anna schreiben",
		},
		{
			name: "leaves a relative link as its text",
			html: `<a href="/impressum">Impressum</a>`,
			want: "Impressum",
		},
		{
			name: "keeps the text of an anchor with no address",
			html: `<a>Kein Ziel</a>`,
			want: "Kein Ziel",
		},
		{
			name: "keeps a word boundary an inline tag would close up",
			html: "<b>Angebot</b> <i>2026</i>",
			want: "Angebot 2026",
		},
		{
			name: "collapses insignificant whitespace",
			html: "<p>Viele\n   Grüße   aus\tMünchen</p>",
			want: "Viele Grüße aus München",
		},
		{
			name: "caps a run of empty blocks at one blank line",
			html: "<p>Oben</p><div></div><div></div><p>Unten</p>",
			want: "Oben\n\nUnten",
		},
		{
			name: "reads a document with unclosed tags",
			html: "<div><p>Erste Frage.<p>Zweite Frage.",
			want: "Erste Frage.\n\nZweite Frage.",
		},
		{
			name: "keeps the text of an anchor the document never closed",
			html: `<p>Siehe <a href="https://kunde.de/x">hier`,
			want: "Siehe hier (https://kunde.de/x)",
		},
		{
			name: "renders a heading as its own block",
			html: "<h1>Angebot 2026</h1><p>Anbei die Unterlagen.</p>",
			want: "Angebot 2026\n\nAnbei die Unterlagen.",
		},
		{
			name: "keeps nothing from an empty document",
			html: "",
			want: "",
		},
		{
			name: "keeps nothing from a document that is only markup",
			html: "<html><head><title>Newsletter</title></head><body></body></html>",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := htmlToText(tc.html); got != tc.want {
				t.Errorf("htmlToText()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// The realistic case: an Outlook-shaped German mail, where every failure mode
// above appears at once.
func TestHTMLToTextRendersAnOutlookMail(t *testing.T) {
	t.Parallel()
	const src = `<html><head><meta charset="utf-8">
<style type="text/css">p.MsoNormal{margin:0cm;font-size:11.0pt}</style></head>
<body lang="DE"><div class="WordSection1">
<p class="MsoNormal">Sehr geehrter Herr Jankowfsky,</p>
<p class="MsoNormal">anbei das &uuml;berarbeitete Angebot. Details finden Sie
<a href="https://kunde.de/angebot-2026">in unserem Portal</a>.</p>
<p class="MsoNormal">Mit freundlichen Gr&uuml;&szlig;en<br>Anna Berger</p>
</div></body></html>`
	got := htmlToText(src)
	want := "Sehr geehrter Herr Jankowfsky,\n\n" +
		"anbei das überarbeitete Angebot. Details finden Sie in unserem Portal (https://kunde.de/angebot-2026).\n\n" +
		"Mit freundlichen Grüßen\nAnna Berger"
	if got != want {
		t.Errorf("htmlToText()\n got: %q\nwant: %q", got, want)
	}
}

// bodyText is where the choice between the two MIME parts is made, and the
// plain part must keep winning: it is what the sender's own client composed.
func TestBodyTextPrefersThePlainPart(t *testing.T) {
	t.Parallel()
	got := bodyText("Anbei das Angebot.", "<p>Anbei das <b>Angebot</b>.</p>")
	if got != "Anbei das Angebot." {
		t.Errorf("bodyText() = %q, want the plain part verbatim", got)
	}
}

func TestBodyTextRendersHTMLWhenThereIsNoPlainPart(t *testing.T) {
	t.Parallel()
	got := bodyText("   ", "<p>Anbei das Angebot.</p><p>Bis Dienstag.</p>")
	if got != "Anbei das Angebot.\n\nBis Dienstag." {
		t.Errorf("bodyText() = %q, want the rendered HTML", got)
	}
}

func TestBodyTextIsEmptyWhenTheMessageCarriedNoText(t *testing.T) {
	t.Parallel()
	if got := bodyText("", ""); got != "" {
		t.Errorf("bodyText() = %q, want empty", got)
	}
}
