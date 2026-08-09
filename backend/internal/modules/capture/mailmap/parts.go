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
	"strconv"
	"strings"
	"unicode"

	"github.com/emersion/go-message/mail"
)

// The inbound bounds (DOC-PARAM-3/4/5). They exist so one message cannot
// exhaust storage: a message beyond them is captured with the parts that fit,
// and what did not fit is reported rather than dropped in silence (DOC-AC-12).
const (
	maxParts = 20
	// maxPartsExamined is the hard ceiling on parts LOOKED AT, well above any
	// real message and far below what a crafted one can hold.
	maxPartsExamined = 200
	maxPartBytes     = 25 << 20
	maxMessageBytes  = 50 << 20
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

// PartDrop is how many files one bound refused, and why. Counted rather than
// listed: the number of refusals is the sender's choice, so a record per
// refusal would let them size our own log.
type PartDrop struct {
	Reason string
	Count  int
}

// The reasons a part can fail to make it. Each is observable on the capture
// breadcrumb so an operator can tell a message with no files from a message
// whose files were refused.
const (
	DropTooManyParts   = "too_many_parts"
	DropPartTooLarge   = "part_too_large"
	DropMessageTooBig  = "message_bytes_exceeded"
	DropUnreadablePart = "part_unreadable"
	// DropWalkTruncated names files we know we did not reach: the MIME walk
	// failed structurally and everything after that point is unread. The count
	// is unknowable, which is exactly why it is reported.
	DropWalkTruncated = "message_walk_truncated"
)

// collector accumulates the files a single message carried while the body walk
// runs, so the message is read once rather than twice.
type collector struct {
	parts   []Part
	dropped map[string]int
	budget  int
	ordinal int
}

func newCollector() *collector {
	return &collector{budget: maxMessageBytes, dropped: map[string]int{}}
}

// drop tallies one refusal. Bounded by construction: there are five reasons, so
// there are at most five tallies however many parts a message contains.
func (c *collector) drop(reason string) { c.dropped[reason]++ }

// drops renders the tallies in a stable order, so the same message always
// reports the same way.
func (c *collector) drops() []PartDrop {
	out := make([]PartDrop, 0, len(c.dropped))
	for _, reason := range []string{
		DropTooManyParts, DropPartTooLarge, DropMessageTooBig,
		DropUnreadablePart, DropWalkTruncated,
	} {
		if count := c.dropped[reason]; count > 0 {
			out = append(out, PartDrop{Reason: reason, Count: count})
		}
	}
	return out
}

// exhausted reports that the walk has seen more parts than any real message
// carries and must stop.
//
// The per-part bounds decide what is KEPT; this decides what is even looked at.
// Without it a sender chooses how long we walk: parts cost 25 bytes each on the
// wire, so a single message within the adapters' own size limits contains
// millions of them, and merely counting those is work an unauthenticated sender
// got us to do.
func (c *collector) exhausted() bool { return c.ordinal >= maxPartsExamined }

// inlineFilename reads the name an inline part gave itself, from either place a
// client may put it: the Content-Disposition filename parameter, or the older
// Content-Type name parameter. Empty means the part named nothing and is body
// text rather than a file.
func inlineFilename(header *mail.InlineHeader) string {
	if _, params, err := header.ContentDisposition(); err == nil {
		if name := strings.TrimSpace(params["filename"]); name != "" {
			return name
		}
	}
	if _, params, err := header.ContentType(); err == nil {
		if name := strings.TrimSpace(params["name"]); name != "" {
			return name
		}
	}
	return ""
}

// takeInline reads a part the sender rendered inline but named — a file by any
// measure. It shares every bound and every rule with a declared attachment;
// only the header type differs.
func (c *collector) takeInline(header *mail.InlineHeader, name string, body io.Reader) {
	declared, _, err := header.ContentType()
	if err != nil {
		declared = ""
	}
	c.admit(name, declared, body)
}

// take reads one attachment part, or records why it could not.
//
// The ordinal advances for every attachment part the message contains, whether
// or not it survives — see Part.Ordinal for why that matters more than it
// looks.
func (c *collector) take(header *mail.AttachmentHeader, body io.Reader) {
	declared, err := "", error(nil)
	if declared, _, err = header.ContentType(); err != nil {
		// A malformed Content-Type is no claim at all, which the sniff answers
		// on its own.
		declared = ""
	}
	filename, err := header.Filename()
	if err != nil {
		// An undecodable name is no name. SafeFilename supplies one from the
		// ordinal, which is the only thing about this file we can vouch for.
		filename = ""
	}
	c.admit(filename, declared, body)
}

// truncated records that the walk ended before the message did, so files we
// never reached are reported rather than silently absent.
func (c *collector) truncated() { c.drop(DropWalkTruncated) }

// admit applies every bound to one candidate file and keeps it or says why not.
func (c *collector) admit(filename, declared string, body io.Reader) {
	c.ordinal++
	ordinal := c.ordinal
	// Counted over every attachment part the message CONTAINS, not over the
	// ones that fit. Counting survivors would let a sender past the cap with
	// oversized parts: each is refused without advancing the count, so the next
	// one is still admitted, and a message of a thousand 25 MB parts is read in
	// full — a cap that costs more to enforce than to ignore.
	if ordinal > maxParts {
		c.drop(DropTooManyParts)
		return
	}
	// Read one byte past the per-file cap so a file sitting exactly on it is
	// kept and the one over it is refused, without holding the whole oversized
	// body to find out which.
	content, err := io.ReadAll(io.LimitReader(body, maxPartBytes+1))
	if err != nil {
		c.drop(DropUnreadablePart)
		return
	}
	if len(content) > maxPartBytes {
		c.drop(DropPartTooLarge)
		return
	}
	if len(content) > c.budget {
		// The message's total allowance is spent. Everything after this is
		// refused for the same reason, which is why the budget is not restored.
		c.budget = 0
		c.drop(DropMessageTooBig)
		return
	}
	c.budget -= len(content)

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
		return "attachment-" + strconv.Itoa(ordinal)
	}
	if len(cleaned) > maxFilenameLen {
		// truncate appends an ellipsis, so it is given room for one: the stated
		// ceiling is what the column and every list that renders it must hold.
		cleaned = truncate(cleaned, maxFilenameLen-len("…"))
	}
	return cleaned
}

// maxFilenameLen keeps a pathological name out of the column and out of every
// list that renders it. It is generous enough that no real filename hits it.
const maxFilenameLen = 200
