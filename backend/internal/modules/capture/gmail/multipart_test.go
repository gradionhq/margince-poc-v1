// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

// The wire shape of a message that carries markup.
//
// These assert against a real MIME parser rather than against substrings: a
// malformed multipart message is not a broken string, it is a mail that arrives
// blank, and only a parser can tell the two apart.

import (
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func plainMessage() connector.EmailMessage {
	return connector.EmailMessage{
		To:        []string{"marine@surfe.test"},
		Subject:   "Die Lieferbedingungen",
		Body:      "Hallo Marine,\r\n\r\nanbei die Zahlen.",
		MessageID: "abc123@margince.test",
	}
}

// A message with no markup keeps the single-part shape it has always had.
// Wrapping one part in a multipart envelope buys nothing and costs every reader
// a boundary to parse.
func TestAPlainMessageStaysSinglePart(t *testing.T) {
	parsed := parseMail(t, buildRFC822("rep@gradion.test", plainMessage()))

	mediaType, _, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parsing the content type failed: %v", err)
	}
	if mediaType != "text/plain" {
		t.Fatalf("expected text/plain, got %q", mediaType)
	}
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatalf("reading the body failed: %v", err)
	}
	if string(body) != "Hallo Marine,\r\n\r\nanbei die Zahlen." {
		t.Fatalf("the body changed: %q", body)
	}
}

// Both parts arrive, and the PLAIN one is first. RFC 2046 §5.1.4 orders
// alternatives least-faithful first and a client renders the last part it
// understands, so a reversed order shows plain text to everybody.
func TestAnHTMLMessageIsMultipartAlternativeWithPlainFirst(t *testing.T) {
	msg := plainMessage()
	msg.HTMLBody = "<p>Hallo Marine,</p><p>anbei die <b>Zahlen</b>.</p>"

	parts := parseParts(t, buildRFC822("rep@gradion.test", msg))
	if len(parts) != 2 {
		t.Fatalf("expected two alternatives, got %d", len(parts))
	}
	if parts[0].mediaType != "text/plain" {
		t.Fatalf("the first alternative must be text/plain, got %q", parts[0].mediaType)
	}
	if parts[1].mediaType != "text/html" {
		t.Fatalf("the second alternative must be text/html, got %q", parts[1].mediaType)
	}
	if !strings.Contains(parts[0].body, "anbei die Zahlen.") {
		t.Fatalf("the plain alternative lost its text: %q", parts[0].body)
	}
	if !strings.Contains(parts[1].body, "<b>Zahlen</b>") {
		t.Fatalf("the html alternative lost its markup: %q", parts[1].body)
	}
}

// Both parts declare utf-8. A part without the charset is read as US-ASCII by
// clients that follow the default, which mangles every umlaut in it.
func TestBothAlternativesDeclareUTF8(t *testing.T) {
	msg := plainMessage()
	msg.HTMLBody = "<p>Zahlen für Sie</p>"

	for _, part := range parseParts(t, buildRFC822("rep@gradion.test", msg)) {
		if got := strings.ToLower(part.charset); got != "utf-8" {
			t.Errorf("%s declares charset %q, expected utf-8", part.mediaType, got)
		}
	}
}

// The boundary is derived from the message identity, so the same message
// renders byte-identically on a retry — a connector comparing what it is about
// to send with what it already sent must not see a different message each time.
func TestTheBoundaryIsStableAcrossRenders(t *testing.T) {
	msg := plainMessage()
	msg.HTMLBody = "<p>x</p>"

	if first, second := buildRFC822("rep@gradion.test", msg), buildRFC822("rep@gradion.test", msg); first != second {
		t.Fatal("two renders of one message differ; a retry would look like a new message")
	}

	other := msg
	other.MessageID = "different@margince.test"
	if mimeBoundary(msg.MessageID) == mimeBoundary(other.MessageID) {
		t.Fatal("two different messages share a boundary")
	}
}

// A boundary that occurs inside a part ends it early, and everything after it
// is read as the next part's headers — the message arrives truncated at the
// point where the body happened to say the wrong thing.
func TestABodyCannotForgeTheBoundary(t *testing.T) {
	msg := plainMessage()
	boundary := mimeBoundary(msg.MessageID)
	msg.Body = "Text mentioning " + boundary + " in passing."
	msg.HTMLBody = "<p>markup</p>"

	parts := parseParts(t, buildRFC822("rep@gradion.test", msg))
	if len(parts) != 2 {
		t.Fatalf("a body that names the boundary broke the message into %d parts", len(parts))
	}
	if !strings.Contains(parts[0].body, "in passing.") {
		t.Fatalf("the plain part was cut short: %q", parts[0].body)
	}
}

type mimePart struct {
	mediaType string
	charset   string
	body      string
}

func parseMail(t *testing.T, raw string) *mail.Message {
	t.Helper()
	parsed, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("the rendered message does not parse as mail: %v", err)
	}
	return parsed
}

func parseParts(t *testing.T, raw string) []mimePart {
	t.Helper()
	parsed := parseMail(t, raw)
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parsing the content type failed: %v", err)
	}
	if mediaType != "multipart/alternative" {
		t.Fatalf("expected multipart/alternative, got %q", mediaType)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	var out []mimePart
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("reading a part failed: %v", err)
		}
		partType, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parsing a part's content type failed: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("reading a part's body failed: %v", err)
		}
		out = append(out, mimePart{
			mediaType: partType,
			charset:   partParams["charset"],
			body:      string(body),
		})
	}
}
