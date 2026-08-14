// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

// The wire format: a provider-neutral message rendered as the RFC822 bytes
// Gmail's messages.send accepts.
//
// Apart from the connector's transport concerns because it is a different
// question — that half decides WHETHER and WHEN to send, this half decides what
// the bytes are. It is also the half a reader checks against a spec: the part
// order, the transfer encodings and the boundary rules below each cite the RFC
// that requires them.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"mime"
	"net/mail"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// buildRFC822 renders the provider-neutral message as the wire format Gmail
// accepts: header order follows RFC 5322's origination-first sequence, body is
// text/plain UTF-8.
func buildRFC822(from string, msg connector.EmailMessage) string {
	var b strings.Builder
	writeHeader(&b, "From", fromHeader(from, msg.FromName))
	writeHeader(&b, "To", addressList(msg.To))
	if cc := addressList(msg.Cc); cc != "" {
		writeHeader(&b, "Cc", cc)
	}
	// Encoded-word per RFC 2047 so a non-ASCII subject survives the wire.
	writeHeader(&b, "Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	writeHeader(&b, "Message-ID", bracket(msg.MessageID))
	if msg.InReplyTo != "" {
		writeHeader(&b, "In-Reply-To", bracket(msg.InReplyTo))
	}
	if len(msg.References) > 0 {
		refs := make([]string, 0, len(msg.References))
		for _, r := range msg.References {
			refs = append(refs, bracket(r))
		}
		writeHeader(&b, "References", strings.Join(refs, " "))
	}
	if msg.ListUnsubscribe != "" {
		writeHeader(&b, "List-Unsubscribe", msg.ListUnsubscribe)
		writeHeader(&b, "List-Unsubscribe-Post", msg.ListUnsubscribePost)
	}
	writeHeader(&b, "MIME-Version", "1.0")
	writeBody(&b, msg)
	return b.String()
}

// writeBody renders the content headers and the body itself.
//
// A plain-text-only message keeps the single-part shape it has always had:
// wrapping one part in a multipart envelope buys nothing and costs every reader
// a boundary to parse.
//
// With markup it becomes multipart/alternative, PLAIN PART FIRST. The order is
// the contract, not a preference — RFC 2046 §5.1.4 puts the least-faithful
// alternative first and a client renders the LAST part it understands, so a
// reversed order shows the plain text to everybody.
func writeBody(b *strings.Builder, msg connector.EmailMessage) {
	if len(msg.Files) > 0 {
		writeMixed(b, msg)
		return
	}
	writeText(b, msg)
}

// writeMixed renders a message carrying files: multipart/mixed whose FIRST part
// is the message itself — text, or the alternative pair — and whose remaining
// parts are the attachments.
//
// The nesting is the standard one and the order is not arbitrary: a client
// shows the first part as the body and offers the rest, so a message whose
// files came first would open on an attachment.
func writeMixed(b *strings.Builder, msg connector.EmailMessage) {
	boundary := safeBoundary(msg) + "_mixed"
	writeHeader(b, "Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "\r\n")
	writeText(b, msg)
	b.WriteString("\r\n")

	for _, file := range msg.Files {
		writeAttachment(b, boundary, file)
	}
	b.WriteString("--" + boundary + "--\r\n")
}

// writeAttachment renders one file as a base64 part.
//
// base64 rather than 8bit, unlike the text parts: a PDF's bytes include the
// line endings and the boundary-looking sequences that a text part is allowed
// to assume it has none of, and any of them would end the part early.
//
// The filename rides in BOTH Content-Type and Content-Disposition because
// clients disagree about which they read, and it is RFC 2047 encoded in each:
// a German file name is otherwise mangled or rejected.
func writeAttachment(b *strings.Builder, boundary string, file connector.OutboundFile) {
	contentType := file.ContentType
	if contentType == "" {
		// The type nobody can misread as something else. A part with no type
		// is guessed at, and a client that guesses wrong renders a PDF as
		// gibberish in the message body.
		contentType = "application/octet-stream"
	}
	name := mime.QEncoding.Encode("utf-8", file.Filename)
	b.WriteString("--" + boundary + "\r\n")
	writeHeader(b, "Content-Type", contentType+`; name="`+name+`"`)
	writeHeader(b, "Content-Disposition", `attachment; filename="`+name+`"`)
	writeHeader(b, "Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	b.WriteString(wrapBase64(base64.StdEncoding.EncodeToString(file.Body)))
	b.WriteString("\r\n")
}

// wrapBase64 folds the encoding at 76 characters, which RFC 2045 requires and
// some relays enforce by refusing longer lines.
func wrapBase64(encoded string) string {
	const width = 76
	var out strings.Builder
	for i := 0; i < len(encoded); i += width {
		end := i + width
		if end > len(encoded) {
			end = len(encoded)
		}
		out.WriteString(encoded[i:end])
		out.WriteString("\r\n")
	}
	return out.String()
}

// writeText renders the message itself — one text part, or the alternative
// pair when it carries markup.
func writeText(b *strings.Builder, msg connector.EmailMessage) {
	if msg.HTMLBody == "" {
		writeHeader(b, "Content-Type", `text/plain; charset="utf-8"`)
		writeHeader(b, "Content-Transfer-Encoding", transferEncoding)
		b.WriteString("\r\n")
		b.WriteString(canonicalCRLF(msg.Body))
		return
	}
	boundary := safeBoundary(msg)
	writeHeader(b, "Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	writePart(b, boundary, `text/plain; charset="utf-8"`, msg.Body)
	writePart(b, boundary, `text/html; charset="utf-8"`, msg.HTMLBody)
	b.WriteString("--" + boundary + "--\r\n")
}

// writePart writes one MIME part: its boundary, its type, and its content.
func writePart(b *strings.Builder, boundary, contentType, content string) {
	b.WriteString("--" + boundary + "\r\n")
	writeHeader(b, "Content-Type", contentType)
	writeHeader(b, "Content-Transfer-Encoding", transferEncoding)
	b.WriteString("\r\n")
	b.WriteString(canonicalCRLF(content))
	b.WriteString("\r\n")
}

// transferEncoding declares that a part carries raw UTF-8 octets.
//
// Without it MIME defaults a part to 7bit, which says every octet is ASCII —
// and a German umlaut or a Vietnamese diacritic in a body so declared is a lie
// the next relay is entitled to act on, by rejecting the message or by
// stripping the high bit. Gmail's base64url wrapper does not fix this: it
// encodes the RFC822 message for transport to the API, and what this header
// describes is the part INSIDE that message.
const transferEncoding = "8bit"

// canonicalCRLF makes every line ending CRLF, which is the only line ending
// RFC 5322 admits.
//
// Bodies arrive here with bare LFs — the signature and the unsubscribe footer
// are both built with "\n" — and a lone LF inside a MIME part is not a line
// break to a strict reader. It is also how a part boundary stops being
// recognised, since a boundary delimiter must be preceded by CRLF.
func canonicalCRLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
}

// safeBoundary is the derived boundary, extended until no part contains it.
//
// A delimiter that occurs at the start of a line inside a part ENDS that part
// there, and everything after it is read as the next part's headers — the
// message arrives truncated at whatever the sender happened to write. The
// derived value is 96 bits of the message id's digest, so a collision is not
// something that happens by accident; this loop is what makes it not something
// that happens on purpose either.
func safeBoundary(msg connector.EmailMessage) string {
	boundary := mimeBoundary(msg.MessageID)
	for strings.Contains(msg.Body, boundary) || strings.Contains(msg.HTMLBody, boundary) {
		boundary += "x"
	}
	return boundary
}

// mimeBoundary derives the part separator from the message identity.
//
// Derived rather than random so the same message renders byte-identically on a
// retry: a connector that re-sends compares what it is about to transmit with
// what it already did, and a fresh boundary each time would make every retry
// look like a different message.
//
// The prefix guarantees the boundary cannot occur in the body it separates:
// a boundary is only safe if no line of any part begins with it, and no mail
// body contains this token unless somebody set out to forge one — which the
// hyphens make impossible to do accidentally.
func mimeBoundary(messageID string) string {
	sum := sha256.Sum256([]byte(messageID))
	return "--=_margince_" + hex.EncodeToString(sum[:12])
}

// fromHeader renders the sender, with their name when the CRM knows it.
//
// A bare address shows its LOCAL PART in every mail client — a message from
// lars@gradion.com arrives from "lars" — which is what the recipient reads
// first, before the signature at the bottom of the body says who actually
// wrote it.
//
// mail.Address does the rendering rather than string concatenation, and that is
// the whole reason this function exists: a name carrying a non-ASCII character
// needs RFC 2047 encoding, and one carrying a comma or a quote needs quoting or
// the header parses as TWO addresses. Both are easy to get wrong by hand and
// impossible to get wrong this way.
//
// An empty name renders the bare address, which is what every message did
// before the name was available. `"" <addr>` would be worse than nothing.
func fromHeader(address, name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return address
	}
	return (&mail.Address{Name: trimmed, Address: address}).String()
}
