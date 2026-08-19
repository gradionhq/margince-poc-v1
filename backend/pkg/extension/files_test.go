// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The bytes are the fact and the sender's claim is a hint. A producer that could
// self-report ContentType would be certifying the one field whose whole purpose
// is distrust.
func TestSniffContentTypeReadsTheBytesNotTheClaim(t *testing.T) {
	for _, c := range []struct {
		name string
		body []byte
		want string
	}{
		{"png by magic bytes", []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 16)), "image/png"},
		{"plain text", []byte("just words"), "text/plain"},
		// An empty body carries no signature at all, and the stdlib sniffer
		// treats "no signature" as text. Recorded because it is surprising:
		// application/octet-stream is what an unrecognised file gets, not what
		// an EMPTY one gets.
		{"empty body is text, not octet-stream", nil, "text/plain"},
		{"unrecognised bytes are octet-stream", []byte{0x00, 0x01, 0x02, 0xFF}, "application/octet-stream"},
	} {
		if got := extension.SniffContentType(c.body); got != c.want {
			t.Errorf("%s: sniffed %q, want %q", c.name, got, c.want)
		}
	}
}

// A charset must not survive into the column: the media type is what is stored.
func TestSniffContentTypeDropsTheCharset(t *testing.T) {
	if got := extension.SniffContentType([]byte("words")); strings.Contains(got, "charset") {
		t.Errorf("sniffed %q, which still carries a charset", got)
	}
}

// The sniff reads the head, not the file. A body far larger than the sniff
// window must cost the same as one that fits in it, and must still be typed by
// its opening bytes rather than by whatever follows them.
func TestSniffContentTypeReadsOnlyTheHead(t *testing.T) {
	body := append([]byte("\x89PNG\r\n\x1a\n"), strings.Repeat("<html>", 1<<16)...)
	if got := extension.SniffContentType(body); got != "image/png" {
		t.Errorf("sniffed %q, want image/png: the sniff read past its window", got)
	}
}

// An agreeing claim is not worth a column: storing it on every row would make
// the interesting case invisible.
func TestDeclaredTypeDisagreementKeepsOnlyTheDisagreement(t *testing.T) {
	for _, c := range []struct{ name, declared, sniffed, want string }{
		{"agreement records nothing", "image/png", "image/png", ""},
		{"disagreement is kept", "image/png", "application/zip", "image/png"},
		{"no claim records nothing", "", "image/png", ""},
		{"parameters are stripped before comparing", "text/plain; charset=utf-8", "text/plain", ""},
		{"case and padding do not make a disagreement", "  IMAGE/PNG  ", "image/png", ""},
		{"an unparseable claim is kept as it stands", "not a media type", "image/png", "not a media type"},
	} {
		if got := extension.DeclaredTypeDisagreement(c.declared, c.sniffed); got != c.want {
			t.Errorf("%s: DeclaredTypeDisagreement(%q, %q) = %q, want %q",
				c.name, c.declared, c.sniffed, got, c.want)
		}
	}
}

// Three classes of character go, and each is an attack rather than tidiness.
func TestSafeFilenameRemovesPathControlAndBidiCharacters(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"path separators", "../../etc/passwd", "....etcpasswd"},
		{"windows separators", `..\..\windows\system32`, "....windowssystem32"},
		{"a name that is only dots is named by position", "...", "attachment-3"},
		{"empty is named by position", "", "attachment-3"},
		{"whitespace only is named by position", "   ", "attachment-3"},
		{"right-to-left override that fakes an extension", "invoice‮gpj.exe", "invoicegpj.exe"},
		{"a newline cannot rewrite a log line", "note\ninjected", "noteinjected"},
		{"a header injection attempt", "invoice\r\nX-Injected: yes.pdf", "invoiceX-Injected: yes.pdf"},
		{"separators only is named by position", "/////", "attachment-3"},
		{"an ordinary name is left alone", "quarterly-report.pdf", "quarterly-report.pdf"},
	} {
		if got := extension.SafeFilename(c.in, 3); got != c.want {
			t.Errorf("%s: SafeFilename(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// A pathological name is cut, and the cut is visible: a name silently shortened
// to something that still reads as a whole name is worse than one marked.
func TestSafeFilenameTruncatesVisiblyAndOnARuneBoundary(t *testing.T) {
	got := extension.SafeFilename(strings.Repeat("a", 500), 1)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a cut name %q does not say it was cut", got)
	}
	if len(got) > 200 {
		t.Errorf("a cut name is %d bytes, past the 200-byte ceiling the column holds", len(got))
	}

	// Multi-byte runes make the byte ceiling land mid-character; the cut must
	// back off rather than store a broken UTF-8 sequence.
	multibyte := extension.SafeFilename(strings.Repeat("é", 500), 1)
	if !strings.HasSuffix(multibyte, "…") {
		t.Errorf("a cut multi-byte name %q does not say it was cut", multibyte)
	}
	if !utf8.ValidString(multibyte) {
		t.Errorf("a cut multi-byte name %q is not valid UTF-8", multibyte)
	}
}
