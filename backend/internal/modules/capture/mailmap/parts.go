// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

// The files a message carries, read out of its MIME parts.
//
// Every bound lives HERE rather than in each provider adapter, because a bound
// enforced per adapter is a bound the next adapter forgets. Gmail, IMAP and
// Graph all hand their RFC822 bytes to this one parser, so this is the single
// place where an untrusted message becomes a fixed, countable set of files.
//
// Three things about an inbound part are never taken on trust: how many there
// are, how big they are, and what they claim to be. A sender controls all
// three, and each has its own answer below — a cap, a cap, and a sniff.

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"

	"github.com/emersion/go-message/mail"
)

// The inbound bounds (DOC-PARAM-3/4/5). They exist so one message cannot
// exhaust storage: a message beyond them is captured with the parts that fit,
// and what did not fit is reported rather than dropped in silence (DOC-AC-12).
const (
	maxParts        = 20
	maxPartBytes    = 25 << 20
	maxMessageBytes = 50 << 20
)

// sniffLen is what http.DetectContentType actually reads. Reading exactly that
// much keeps the sniff off the whole file.
const sniffLen = 512

// Part is one file a message carried, already bounded and named safely.
type Part struct {
	// Ordinal is the part's position in the message, counted over EVERY
	// attachment part including the ones dropped for size. It is the part's
	// identity within the message, so it must not shift when a neighbour is
	// dropped — a re-pull that dropped a different part would otherwise
	// renumber the survivors and duplicate them all.
	Ordinal int
	// Filename is presentational and sanitized. The object key is generated,
	// never derived from this.
	Filename string
	// ContentType is the SNIFFED type, which governs. DeclaredType is kept only
	// when the sender's claim disagreed, so the disagreement stays inspectable
	// instead of being resolved silently (DOC-PARAM-9).
	ContentType  string
	DeclaredType string
	Body         []byte
}

// PartDrop names one file that did not survive the bounds. It carries no
// filename: it is written to the capture breadcrumb, which records the natural
// key and the reason and nothing a sender wrote.
type PartDrop struct {
	Ordinal int
	Reason  string
}

// The reasons a part can fail to make it. Each is observable on the capture
// breadcrumb so an operator can tell a message with no files from a message
// whose files were refused.
const (
	DropTooManyParts   = "too_many_parts"
	DropPartTooLarge   = "part_too_large"
	DropMessageTooBig  = "message_bytes_exceeded"
	DropUnreadablePart = "part_unreadable"
)

// collector accumulates the files a single message carried while the body walk
// runs, so the message is read once rather than twice.
type collector struct {
	parts   []Part
	drops   []PartDrop
	seen    int
	budget  int
	ordinal int
}

func newCollector() *collector { return &collector{budget: maxMessageBytes} }

// take reads one attachment part, or records why it could not.
//
// The ordinal advances for every attachment part the message contains, whether
// or not it survives — see Part.Ordinal for why that matters more than it
// looks.
func (c *collector) take(header *mail.AttachmentHeader, body io.Reader) {
	c.ordinal++
	ordinal := c.ordinal
	if c.seen >= maxParts {
		c.drops = append(c.drops, PartDrop{Ordinal: ordinal, Reason: DropTooManyParts})
		return
	}
	// Read one byte past the per-file cap so a file sitting exactly on it is
	// kept and the one over it is refused, without holding the whole oversized
	// body to find out which.
	content, err := io.ReadAll(io.LimitReader(body, maxPartBytes+1))
	if err != nil {
		c.drops = append(c.drops, PartDrop{Ordinal: ordinal, Reason: DropUnreadablePart})
		return
	}
	if len(content) > maxPartBytes {
		c.drops = append(c.drops, PartDrop{Ordinal: ordinal, Reason: DropPartTooLarge})
		return
	}
	if len(content) > c.budget {
		// The message's total allowance is spent. Everything after this is
		// refused for the same reason, which is why the budget is not restored.
		c.budget = 0
		c.drops = append(c.drops, PartDrop{Ordinal: ordinal, Reason: DropMessageTooBig})
		return
	}
	c.budget -= len(content)
	c.seen++

	declared, _, err := header.ContentType()
	if err != nil {
		// A malformed Content-Type is no claim at all, which the sniff below
		// answers on its own.
		declared = ""
	}
	filename, err := header.Filename()
	if err != nil {
		filename = ""
	}
	sniffed := sniff(content)
	c.parts = append(c.parts, Part{
		Ordinal:      ordinal,
		Filename:     SafeFilename(filename, ordinal),
		ContentType:  sniffed,
		DeclaredType: disagreement(declared, sniffed),
		Body:         content,
	})
}

// sniff resolves what a file actually is. The sender's declaration is a hint
// from an untrusted party; the bytes are the fact (DOC-PARAM-9).
func sniff(content []byte) string {
	head := content
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	full := http.DetectContentType(head)
	// DetectContentType appends a charset for text types. The column stores the
	// media type, and the charset is not something a receipt reports on.
	if base, _, err := mime.ParseMediaType(full); err == nil {
		return base
	}
	return full
}

// disagreement returns the declared type only when it differs from what the
// bytes say. Storing an agreeing claim would fill the column on every row and
// make the interesting case invisible.
func disagreement(declared, sniffed string) string {
	base := strings.TrimSpace(strings.ToLower(declared))
	if base == "" {
		return ""
	}
	if parsed, _, err := mime.ParseMediaType(base); err == nil {
		base = parsed
	}
	if base == sniffed {
		return ""
	}
	return base
}

// SafeFilename makes a sender-supplied name safe to store and show
// (DOC-PARAM-8). It is presentational only: nothing opens a file by this name,
// and the object key is generated elsewhere.
//
// Three classes go, and each is a real attack rather than tidiness. Path
// separators stop a name from ever reading as a path. Control characters stop a
// name from rewriting a log line it appears in. Bidirectional overrides stop a
// name from rendering as an extension it does not have — the name a person
// reads and the extension the file has must be the same string. (A name ending
// "gpj.exe" with a RIGHT-TO-LEFT OVERRIDE before it renders as "...jpg".)
func SafeFilename(name string, ordinal int) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == 0:
			return -1
		case unicode.IsControl(r):
			return -1
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200F, r == 0x200E:
			return -1
		}
		return r
	}, name)
	// A name that is only dots would still read as a path component.
	cleaned = strings.TrimSpace(cleaned)
	if strings.Trim(cleaned, ".") == "" {
		cleaned = ""
	}
	if cleaned == "" {
		// Named by position rather than left blank: a reader needs something to
		// point at, and the ordinal is the one true thing we know about it.
		return "attachment-" + itoa(ordinal)
	}
	if len(cleaned) > maxFilenameLen {
		cleaned = truncate(cleaned, maxFilenameLen)
	}
	return cleaned
}

// maxFilenameLen keeps a pathological name out of the column and out of every
// list that renders it. It is generous enough that no real filename hits it.
const maxFilenameLen = 200

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
