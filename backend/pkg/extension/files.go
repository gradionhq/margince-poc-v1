// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

// The files a captured record carried, the ones it could not, and the files an
// outbound message carries.
//
// They live on the PUBLISHED surface rather than in the core's connector port
// because a unit and a core connector must hand the file keeper the same type.
// The alternative — a mirrored type on each side kept honest by a parity test —
// is what ports/jurisdiction already rejected for the pack contract: "aliased so
// a pack registered by an extension and one registered by a core module are the
// same type". A second spelling of a file is a second set of bounds that can
// disagree about how large a file may be.

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// InboundFile is one file a captured record carried.
//
// Body is held in memory because every bound that decides whether it may be held
// has already been applied by the time one exists — see MaxInboundMessageBytes,
// which is the bound that makes that safe rather than merely true.
type InboundFile struct {
	// Ordinal identifies the file WITHIN its message, counted over every
	// attachment part including dropped ones. Together with the record's source
	// id it is what capture is idempotent on, so it must not shift between pulls
	// of the same message.
	Ordinal int
	// Filename is presentational and already sanitized (SafeFilename). Nothing
	// opens a file by it; the object key is generated.
	Filename string
	// ContentType is what the BYTES say. DeclaredType carries the sender's claim
	// only where it disagreed, so the disagreement stays inspectable.
	ContentType  string
	DeclaredType string
	Body         []byte
}

// FileDrop is how many files one bound refused, and why.
//
// A COUNT rather than a row per file, because the count is attacker-chosen: a
// single message can contain hundreds of thousands of empty parts, and one
// breadcrumb each would let a sender decide how many rows our own audit trail
// writes. The reason is the fact worth keeping; the tally is the scale.
//
// It names no filename on purpose — it reaches an operational log, and a sender
// writes the filename.
type FileDrop struct {
	Reason string
	Count  int
}

// OutboundFile is one file to transmit, in provider-neutral form. The connector
// owns the wire encoding, exactly as it does for the body.
//
// The identifying fields travel WITH the bytes rather than being looked up at
// send time, because the outbound record snapshots them: archiving or superseding
// a document later must not rewrite the history of what was attached to a message
// that already went out.
type OutboundFile struct {
	AttachmentID string
	Filename     string
	ContentType  string
	ByteSize     int64
	Checksum     string
	Body         []byte
}

// The INBOUND bounds — all four of them, and the aggregate is not optional.
//
// Named for the DIRECTION because Carriage carries different numbers about the
// other one: what this installation will accept from a provider is not what a
// provider will carry for it.
//
// MaxInboundMessageBytes is what makes InboundFile.Body safe to hold in memory.
// Without it, MaxInboundFiles × MaxInboundFileBytes licenses 500 MiB per message
// — ten times what mail has ever admitted — on code that reads as bounded.
// MaxInboundFilesExamined is the separate ceiling on files LOOKED AT: a crafted
// message can hold hundreds of thousands of empty parts, and the cost of walking
// them is not the cost of keeping them.
const (
	MaxInboundFiles         = 20
	MaxInboundFilesExamined = 200
	MaxInboundFileBytes     = 25 << 20
	MaxInboundMessageBytes  = 50 << 20
)

// sniffLen is what http.DetectContentType actually reads. Reading exactly that
// much keeps the sniff off the whole file.
const sniffLen = 512

// maxFilenameLen keeps a pathological name out of the column and out of every
// list that renders it. Generous enough that no real filename hits it.
const maxFilenameLen = 200

// SniffContentType resolves what a file actually is. The sender's declaration is
// a hint from an untrusted party; the bytes are the fact.
//
// PUBLISHED because every producer of an InboundFile must answer this question
// the same way. A unit that self-reported ContentType would be certifying the
// one field whose entire purpose is to distrust the sender.
func SniffContentType(content []byte) string {
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

// sendableFallback is the type nobody can misread as something else. A part with
// no usable type is guessed at, and a client that guesses wrong renders a PDF as
// gibberish in the message body.
const sendableFallback = "application/octet-stream"

// SendableContentType is the media type a part may DECLARE on the wire, with its
// parameters, ready for mime.FormatMediaType.
//
// PUBLISHED for the reason SniffContentType is: every producer that renders a
// file onto a wire has to answer this question the same way, and there are two
// of them already — the mail renderer building an RFC822 part and the channel
// upload building a multipart one. Both write the answer into a HEADER they
// assemble themselves, so both need the same two properties, and neither is
// obvious enough to be re-derived per call site.
//
// It PARSES before it re-renders, which is the whole point. The stored value is
// whatever an upload declared — nothing on the write path validates it — so it
// has to be refused when it cannot be represented, and a value carrying CR/LF
// would otherwise end its header line early and let the rest be read as headers
// of its own. But formatting the raw string is not the same as parsing it:
// mime.FormatMediaType takes a bare type/subtype and answers EMPTY for anything
// carrying a parameter, so `text/plain; charset=utf-8` — which is what a
// mail-captured text part routinely stores — would be discarded exactly like a
// hostile value. Parsing first keeps the parameters and refuses only what is
// genuinely unrepresentable.
//
// The fallback is deliberate rather than an error return: a file whose declared
// type cannot be honoured still has to be SENT, and arriving as a download
// rather than a preview is a smaller loss than not arriving.
func SendableContentType(declared string) (string, map[string]string) {
	mediaType, params, err := mime.ParseMediaType(declared)
	if err != nil || mime.FormatMediaType(mediaType, params) == "" {
		return sendableFallback, nil
	}
	return mediaType, params
}

// DeclaredTypeDisagreement returns the declared type only when it differs from
// what the bytes say. Storing an agreeing claim would fill the column on every
// row and make the interesting case invisible.
func DeclaredTypeDisagreement(declared, sniffed string) string {
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
// (DOC-PARAM-8). It is presentational only: nothing opens a file by this name, and the object key is
// generated elsewhere.
//
// Three classes go, and each is a real attack rather than tidiness. Path
// separators stop a name from ever reading as a path. Line breaks stop a name
// from rewriting a log line it appears in. Bidirectional overrides stop a name
// from rendering as an extension it does not have — the name a person reads and
// the extension the file has must be the same string. (A name ending "gpj.exe"
// with a RIGHT-TO-LEFT OVERRIDE before it renders as "...jpg".)
//
// The line-break class is TWO tests, and the second is the one that is easy to
// drop as redundant: unicode.IsControl answers for the Cc block, which is CR, LF
// and NEL, and answers FALSE for U+2028 LINE SEPARATOR and U+2029 PARAGRAPH
// SEPARATOR, which are categories Zl and Zp. Those two break a line in the
// places a filename is read back — a log record, a JSON string, a CSV export —
// so a name carrying one rewrites the record that quotes it exactly as a bare
// newline would. Removing the Zl/Zp test because "IsControl already covers
// control characters" reopens this.
func SafeFilename(name string, ordinal int) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == 0:
			return -1
		case unicode.IsControl(r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
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
		// truncateFilename appends an ellipsis, so it is given room for one: the
		// stated ceiling is what the column and every list that renders it must
		// hold.
		cleaned = truncateFilename(cleaned, maxFilenameLen-len("…"))
	}
	return cleaned
}

// truncateFilename cuts to a byte ceiling and marks the cut, so a shortened name
// is visibly shortened rather than silently different. The ceiling is in BYTES
// because it is the column's ceiling; the cut backs off to a rune boundary so
// what is stored is never a broken UTF-8 sequence.
func truncateFilename(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Carriage is what a sending connector's provider can carry.
//
// A DESCRIPTOR rather than a bool because channels have real per-provider limits
// and a rep must learn them before pressing send, not from a parked delivery
// afterwards.
//
// A ZERO bound means "no limit beyond what the contract itself imposes", never
// "zero allowed" — the only field that says nothing may go is Carries.
//
// MaxBodyWithFiles exists because of a shape mail does not have: a channel that
// carries text-with-files as a CAPTION bounds that text far below what it accepts
// on a text-only message. A message over the bound cannot be split into two
// provider calls without reintroducing the partial send this seam exists to
// prevent, and it cannot be truncated at all, so the delivery parks instead.
// The bound is published on the channel directory so the composer warns BEFORE a
// human presses send — a park discovered at transmission is correct but late.
type Carriage struct {
	Carries         bool
	MaxBytesPerFile int64
	MaxFiles        int
	// MaxBodyWithFiles bounds the body of a message that ALSO carries files,
	// counted in CHARACTERS because a caption cap is a provider's rune count and
	// not a byte budget.
	MaxBodyWithFiles int
}
