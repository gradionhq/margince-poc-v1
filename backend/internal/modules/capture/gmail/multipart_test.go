// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

// The wire shape of a message that carries markup.
//
// These assert against a real MIME parser rather than against substrings: a
// malformed multipart message is not a broken string, it is a mail that arrives
// blank, and only a parser can tell the two apart.

import (
	"encoding/base64"
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

// A part carrying UTF-8 must say so. Without the header MIME defaults to 7bit,
// which claims every octet is ASCII — and an umlaut in a body so declared is a
// lie the next relay may act on, by rejecting the message or stripping the high
// bit. Gmail's base64url wrapper does not fix it: that encodes the message for
// the API, and this header describes the part inside it.
func TestEveryPartDeclaresItsTransferEncoding(t *testing.T) {
	msg := plainMessage()
	msg.HTMLBody = "<p>Zahlen für Sie</p>"

	raw := buildRFC822("rep@gradion.test", msg)
	if strings.Count(raw, "Content-Transfer-Encoding: 8bit") != 2 {
		t.Fatalf("expected both parts to declare 8bit:\n%s", raw)
	}

	plain := buildRFC822("rep@gradion.test", plainMessage())
	if !strings.Contains(plain, "Content-Transfer-Encoding: 8bit") {
		t.Fatalf("a single-part message declares no transfer encoding:\n%s", plain)
	}
}

// Every line ending is CRLF, the only one RFC 5322 admits. Bodies arrive with
// bare LFs — the signature and the unsubscribe footer are both built with "\n"
// — and a lone LF is how a boundary delimiter stops being recognised.
func TestBareLineFeedsAreCanonicalised(t *testing.T) {
	msg := plainMessage()
	msg.Body = "Line one\nLine two"
	msg.HTMLBody = "<p>one</p>\n<p>two</p>"

	raw := buildRFC822("rep@gradion.test", msg)
	if strings.Contains(strings.ReplaceAll(raw, "\r\n", ""), "\n") {
		t.Fatalf("a bare line feed survived into the wire format:\n%q", raw)
	}
}

// A body that contains the boundary would end its own part there. The derived
// value is 96 bits of a digest, so this is not an accident — the extension is
// what makes it not a deliberate one either.
func TestABodyHoldingTheBoundaryForcesADifferentOne(t *testing.T) {
	msg := plainMessage()
	derived := mimeBoundary(msg.MessageID)
	msg.Body = "prefix " + derived + " suffix"
	msg.HTMLBody = "<p>markup</p>"

	chosen := safeBoundary(msg)
	if chosen == derived {
		t.Fatal("the boundary a body contains was used anyway")
	}
	if strings.Contains(msg.Body, chosen) || strings.Contains(msg.HTMLBody, chosen) {
		t.Fatalf("the chosen boundary still occurs in a part: %q", chosen)
	}
}

// A bare From header shows the address's LOCAL PART in every mail client, so a
// message from lars@gradion.com arrives from "lars" — which is what a recipient
// reads first, before the signature at the bottom says who wrote it.
func TestTheSenderIsNamedWhenTheNameIsKnown(t *testing.T) {
	msg := plainMessage()
	msg.FromName = "Lars Jankowfsky"

	parsed := parseMail(t, buildRFC822("lars@gradion.test", msg))
	addr, err := mail.ParseAddress(parsed.Header.Get("From"))
	if err != nil {
		t.Fatalf("the From header does not parse as an address: %v", err)
	}
	if addr.Name != "Lars Jankowfsky" {
		t.Fatalf("the sender's name is %q", addr.Name)
	}
	if addr.Address != "lars@gradion.test" {
		t.Fatalf("the sender's address changed: %q", addr.Address)
	}
}

// A name with no ASCII spelling needs RFC 2047 encoding, exactly as the Subject
// gets. Concatenated raw it is either mangled or rejected.
func TestANonASCIISenderNameSurvivesTheWire(t *testing.T) {
	msg := plainMessage()
	msg.FromName = "Weiß Konrad"

	raw := buildRFC822("weiss@gradion.test", msg)
	if strings.Contains(raw, "From: Weiß") {
		t.Fatalf("a non-ASCII name went out unencoded:\n%s", raw)
	}
	addr, err := mail.ParseAddress(parseMail(t, raw).Header.Get("From"))
	if err != nil {
		t.Fatalf("the From header does not parse: %v", err)
	}
	if addr.Name != "Weiß Konrad" {
		t.Fatalf("the name did not survive the round trip: %q", addr.Name)
	}
}

// A comma in a display name splits an unquoted header into TWO addresses, which
// is a message from somebody the sender never named.
func TestASenderNameWithACommaStaysOneAddress(t *testing.T) {
	msg := plainMessage()
	msg.FromName = "Jankowfsky, Lars"

	header := parseMail(t, buildRFC822("lars@gradion.test", msg)).Header.Get("From")
	list, err := mail.ParseAddressList(header)
	if err != nil {
		t.Fatalf("the From header does not parse: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("a comma in the name produced %d addresses: %q", len(list), header)
	}
	if list[0].Name != "Jankowfsky, Lars" {
		t.Fatalf("the name changed: %q", list[0].Name)
	}
}

// No name on file sends a bare address, which is what every message did before
// the name was available. `"" <addr>` would be worse than nothing.
func TestNoSenderNameSendsABareAddress(t *testing.T) {
	for name, given := range map[string]string{"unset": "", "only spaces": "   "} {
		t.Run(name, func(t *testing.T) {
			msg := plainMessage()
			msg.FromName = given
			if got := parseMail(t, buildRFC822("lars@gradion.test", msg)).Header.Get("From"); got != "lars@gradion.test" {
				t.Fatalf("expected a bare address, got %q", got)
			}
		})
	}
}

// A message carrying files is multipart/mixed whose FIRST part is the message
// and whose rest are the attachments. A client shows the first part as the body
// and offers the others, so a message whose files came first would open on an
// attachment.
func TestAMessageWithFilesIsMultipartMixed(t *testing.T) {
	msg := plainMessage()
	msg.Files = []connector.OutboundFile{
		{Filename: "Vertrag.pdf", ContentType: "application/pdf", Body: []byte("%PDF-1.7 fake")},
	}

	parsed := parseMail(t, buildRFC822("rep@gradion.test", msg))
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parsing the content type failed: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("expected multipart/mixed, got %q", mediaType)
	}

	reader := multipart.NewReader(parsed.Body, params["boundary"])
	first, err := reader.NextPart()
	if err != nil {
		t.Fatalf("reading the message part failed: %v", err)
	}
	if got := first.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("the first part is %q, so a client would open on something other than the message", got)
	}

	attachment, err := reader.NextPart()
	if err != nil {
		t.Fatalf("reading the attachment part failed: %v", err)
	}
	if got := attachment.Header.Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Fatalf("the file is not marked as an attachment: %q", got)
	}
	// NextPart hands back the part's raw bytes; decoding is the client's job
	// and this is that step, so the assertion is on the file a recipient would
	// actually reconstruct.
	encoded, err := io.ReadAll(attachment)
	if err != nil {
		t.Fatalf("reading the attachment body failed: %v", err)
	}
	body, err := base64.StdEncoding.DecodeString(
		strings.ReplaceAll(string(encoded), "\r\n", ""))
	if err != nil {
		t.Fatalf("the attachment is not valid base64: %v", err)
	}
	if string(body) != "%PDF-1.7 fake" {
		t.Fatalf("the bytes did not survive the wire: %q", body)
	}
}

// A file's bytes are base64, not 8bit like the text parts: a PDF contains the
// line endings and boundary-looking sequences a text part may assume it has
// none of, and any of them would end the part early.
func TestAFilesBytesAreBase64Encoded(t *testing.T) {
	msg := plainMessage()
	msg.Files = []connector.OutboundFile{
		{Filename: "raw.bin", Body: []byte("line\r\n--boundary-ish\r\nmore")},
	}

	raw := buildRFC822("rep@gradion.test", msg)
	if !strings.Contains(raw, "Content-Transfer-Encoding: base64") {
		t.Fatalf("a file went out unencoded:\n%s", raw)
	}
	if strings.Contains(raw, "--boundary-ish") {
		t.Fatal("raw file bytes reached the wire, where a boundary-looking line would truncate the message")
	}
}

// Markup AND files: the alternative pair nests inside the mixed envelope, so a
// client still chooses between text and HTML while the files remain attached.
func TestFilesAndMarkupNestCorrectly(t *testing.T) {
	msg := plainMessage()
	msg.HTMLBody = "<p>Anbei die Zahlen.</p>"
	msg.Files = []connector.OutboundFile{{Filename: "z.pdf", Body: []byte("x")}}

	parsed := parseMail(t, buildRFC822("rep@gradion.test", msg))
	_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parsing the content type failed: %v", err)
	}
	first, err := multipart.NewReader(parsed.Body, params["boundary"]).NextPart()
	if err != nil {
		t.Fatalf("reading the message part failed: %v", err)
	}
	if got := first.Header.Get("Content-Type"); !strings.HasPrefix(got, "multipart/alternative") {
		t.Fatalf("the message part is %q, so the plain/markup choice was lost", got)
	}
}

// A non-ASCII file name needs RFC 2047 encoding, exactly as the Subject does.
// Raw, it is mangled or the message is rejected.
func TestANonASCIIFilenameIsEncoded(t *testing.T) {
	msg := plainMessage()
	msg.Files = []connector.OutboundFile{{Filename: "Größenänderung.pdf", Body: []byte("x")}}

	// Asserted through the parser rather than on the bytes: what matters is
	// that a CLIENT recovers the name, not which legal spelling produced it.
	// RFC 2231 is what a MIME parameter takes; the RFC 2047 encoded word this
	// replaced is for header text and arrives literally in some clients.
	recovered := filenameFromWire(t, buildRFC822("rep@gradion.test", msg))
	if recovered != "Größenänderung.pdf" {
		t.Fatalf("a client would see the filename as %q", recovered)
	}
}

// filenameFromWire reads the attachment's filename back the way a mail client
// does: parse the part, parse its Content-Disposition, take the parameter.
func filenameFromWire(t *testing.T, raw string) string {
	t.Helper()
	parsed := parseMail(t, raw)
	_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parsing the content type failed: %v", err)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("no attachment part carried a filename: %v", err)
		}
		_, disp, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			continue
		}
		if name := disp["filename"]; name != "" {
			return name
		}
	}
}

// A file with no declared type is octet-stream rather than nothing: a part with
// no type is guessed at, and a client guessing wrong renders a PDF as gibberish
// in the message body.
func TestAFileWithNoTypeGetsOctetStream(t *testing.T) {
	msg := plainMessage()
	msg.Files = []connector.OutboundFile{{Filename: "unknown", Body: []byte("x")}}

	if !strings.Contains(buildRFC822("rep@gradion.test", msg), "application/octet-stream") {
		t.Fatal("a typeless file went out with no content type")
	}
}

// A declared type carrying a PARAMETER survives, and a type carrying a line
// break does not.
//
// Both used to fail the same way, and silently: the raw stored value went
// straight into mime.FormatMediaType, which takes a bare type/subtype and
// answers the empty string for anything with a parameter — so a
// `text/plain; charset=utf-8` part, which is what a mail-captured text
// attachment routinely stores, went out with a BLANK Content-Type value. The
// question now goes through extension.SendableContentType, which parses before
// it re-renders: the parameter is kept, and only what cannot be represented at
// all falls back.
func TestADeclaredTypesParametersSurviveButItsLineBreaksDoNot(t *testing.T) {
	for _, tc := range []struct{ name, declared, want string }{
		{"a parameter is kept", "text/plain; charset=utf-8", "text/plain; charset=utf-8"},
		{"a bare type is unchanged", "application/pdf", "application/pdf"},
		{"a type that would write its own header falls back", "text/plain\r\nX-Injected: yes", "application/octet-stream"},
		{"no declared type at all", "", "application/octet-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := plainMessage()
			msg.Files = []connector.OutboundFile{
				{Filename: "notes.txt", ContentType: tc.declared, Body: []byte("x")},
			}

			raw := buildRFC822("rep@gradion.test", msg)
			if !strings.Contains(raw, "Content-Type: "+tc.want) {
				t.Errorf("the attachment part does not declare %q:\n%s", tc.want, raw)
			}
			// The injected header must not appear as a header of its own, under any
			// spelling — the part builds its own header block by hand.
			if strings.Contains(raw, "X-Injected") {
				t.Errorf("a declared content type wrote its own header:\n%s", raw)
			}
		})
	}
}

// A filename that could break the parameter it sits in must not. A quote ends
// the value early, and a client then shows a truncated name or none at all.
func TestAFilenameWithAQuoteStaysOneParameter(t *testing.T) {
	msg := plainMessage()
	msg.Files = []connector.OutboundFile{
		{Filename: `the "final" contract.pdf`, Body: []byte("x")},
	}

	if got := filenameFromWire(t, buildRFC822("rep@gradion.test", msg)); got != `the "final" contract.pdf` {
		t.Fatalf("a client would see the filename as %q", got)
	}
}

// A bcc'd address must never appear in a header the recipients read. This is
// the whole of what "blind" means, and the To line is where it would leak.
func TestABlindCopyIsAbsentFromTheVisibleHeaders(t *testing.T) {
	msg := plainMessage()
	msg.To = []string{"visible@surfe.test"}
	msg.Bcc = []string{"blind@surfe.test"}

	parsed := parseMail(t, buildRFC822("rep@gradion.test", msg))
	for _, header := range []string{"To", "Cc"} {
		if strings.Contains(parsed.Header.Get(header), "blind@surfe.test") {
			t.Fatalf("a blind copy reached the %s header: %q", header, parsed.Header.Get(header))
		}
	}
	// The Bcc header itself IS written: messages.send takes the raw message
	// and no envelope list, so it is the only way to address a blind copy —
	// and the submission agent strips it before delivery (RFC 5322 §3.6.3).
	if !strings.Contains(parsed.Header.Get("Bcc"), "blind@surfe.test") {
		t.Fatal("the blind copy was not addressed at all")
	}
}

// A bare "To:" is a malformed header some relays refuse, so an empty visible
// addressee line is omitted rather than written.
//
// The send path refuses a message with no visible addressee before it reaches
// this renderer, so this is a defence in depth rather than a shape the product
// produces: a renderer that emits a header it was handed nothing for is one
// defect away from putting a malformed message on the wire.
func TestABccOnlyMessageOmitsTheToHeaderEntirely(t *testing.T) {
	msg := plainMessage()
	msg.To = nil
	msg.Bcc = []string{"one@surfe.test", "two@surfe.test"}

	raw := buildRFC822("rep@gradion.test", msg)
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, "To:") {
			t.Fatalf("an empty To header was written: %q", line)
		}
	}
	if !strings.Contains(raw, "Bcc: one@surfe.test, two@surfe.test") {
		t.Fatalf("the blind copies were not addressed:\n%s", raw)
	}
}
