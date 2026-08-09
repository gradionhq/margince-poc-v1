// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

// What an untrusted message may and may not do to us through its files.
//
// Every case here is a sender's choice: how many parts to send, how big to make
// them, what to call them, and what to claim they are. The parser's job is that
// none of those four choices reaches storage unexamined, and that whatever it
// refuses it says it refused.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// multipart builds one RFC822 message carrying the given attachment bodies.
func multipart(t *testing.T, files ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("From: her@example.com\r\nTo: me@ours.example\r\n")
	b.WriteString("Subject: Contract\r\nMessage-ID: <m1@example.com>\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=BOUND\r\n\r\n")
	b.WriteString("--BOUND\r\nContent-Type: text/plain\r\n\r\nSee attached.\r\n")
	for i, body := range files {
		b.WriteString("--BOUND\r\nContent-Type: application/pdf\r\n")
		fmt.Fprintf(&b, "Content-Disposition: attachment; filename=\"file-%d.pdf\"\r\n\r\n", i)
		b.WriteString(body + "\r\n")
	}
	b.WriteString("--BOUND--\r\n")
	return b.Bytes()
}

func parseParts(t *testing.T, raw []byte) ([]Part, []PartDrop) {
	t.Helper()
	msg, err := Parse(raw, "me@ours.example")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return msg.parts, msg.partDrops
}

func TestTheFilesAMessageCarriesAreRead(t *testing.T) {
	parts, drops := parseParts(t, multipart(t, "first body", "second body"))

	if len(parts) != 2 {
		t.Fatalf("read %d files, want 2", len(parts))
	}
	if len(drops) != 0 {
		t.Errorf("drops = %v, want none — both files are within every bound", drops)
	}
	if parts[0].Filename != "file-0.pdf" || parts[1].Filename != "file-1.pdf" {
		t.Errorf("filenames = %q, %q", parts[0].Filename, parts[1].Filename)
	}
	if parts[0].Ordinal != 1 || parts[1].Ordinal != 2 {
		t.Errorf("ordinals = %d, %d — they identify the part within the message",
			parts[0].Ordinal, parts[1].Ordinal)
	}
	if string(parts[0].Body) != "first body" {
		t.Errorf("body = %q, want the part's own bytes", parts[0].Body)
	}
}

// The body still reads. A message whose text was lost because it carried a file
// would be a worse record than one with no file at all.
func TestReadingTheFilesDoesNotCostTheBody(t *testing.T) {
	msg, err := Parse(multipart(t, "attached bytes"), "me@ours.example")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(msg.body, "See attached.") {
		t.Errorf("body = %q, want the text part", msg.body)
	}
}

// DOC-PARAM-3: beyond the cap the message captures with its parts truncated,
// and the drop is reported rather than discarded silently (DOC-AC-12).
func TestBeyondTheCapTheExtraFilesAreRefusedAndSaidSo(t *testing.T) {
	bodies := make([]string, maxParts+3)
	for i := range bodies {
		bodies[i] = "body"
	}
	parts, drops := parseParts(t, multipart(t, bodies...))

	if len(parts) != maxParts {
		t.Errorf("kept %d files, want the cap of %d", len(parts), maxParts)
	}
	if len(drops) != 1 || drops[0].Reason != DropTooManyParts {
		t.Fatalf("drops = %v, want one %q tally", drops, DropTooManyParts)
	}
	if drops[0].Count != 3 {
		t.Errorf("counted %d refused files, want the 3 that did not fit", drops[0].Count)
	}
}

// One breadcrumb per REASON, however many files were refused. Every part past
// the cap costs 25 bytes on the wire, so one message within any adapter's size
// limit holds hundreds of thousands of them — and a record per refusal would
// let an unauthenticated sender decide how many rows our own log writes, inside
// the transaction that captures their message.
func TestAFloodOfPartsIsOneTallyAndABoundedWalk(t *testing.T) {
	bodies := make([]string, maxPartsExamined*3)
	for i := range bodies {
		bodies[i] = ""
	}
	parts, drops := parseParts(t, multipart(t, bodies...))

	if len(parts) > maxParts {
		t.Errorf("kept %d files, want no more than the cap of %d", len(parts), maxParts)
	}
	if len(drops) > 2 {
		t.Errorf("wrote %d breadcrumbs for one message, want one per reason: %v", len(drops), drops)
	}
	var truncated bool
	for _, drop := range drops {
		if drop.Reason == DropWalkTruncated {
			truncated = true
		}
	}
	if !truncated {
		t.Errorf("drops = %v, want the walk to stop and say so rather than count every part", drops)
	}
}

// The ordinal is the part's identity, and capture is idempotent on it. If a
// dropped neighbour renumbered the survivors, a re-pull that dropped a
// different part would file every one of them again as a new file.
func TestADroppedFileDoesNotRenumberTheOnesThatSurvived(t *testing.T) {
	huge := strings.Repeat("x", maxPartBytes+1)
	parts, drops := parseParts(t, multipart(t, "first", huge, "third"))

	if len(parts) != 2 || len(drops) != 1 {
		t.Fatalf("kept %d and dropped %d, want 2 and 1", len(parts), len(drops))
	}
	if parts[0].Ordinal != 1 || parts[1].Ordinal != 3 {
		t.Errorf("surviving ordinals = %d, %d, want 1 and 3 — a drop must not renumber",
			parts[0].Ordinal, parts[1].Ordinal)
	}
}

// DOC-PARAM-4: one oversized file is refused; the message and its other files
// are not.
func TestAnOversizedFileIsRefusedAndTheMessageStillLands(t *testing.T) {
	parts, drops := parseParts(t, multipart(t, strings.Repeat("x", maxPartBytes+1), "small"))

	if len(parts) != 1 || string(parts[0].Body) != "small" {
		t.Fatalf("kept %d files, want only the one within the per-file cap", len(parts))
	}
	if len(drops) != 1 || drops[0].Reason != DropPartTooLarge || drops[0].Count != 1 {
		t.Errorf("drops = %v, want one %q counting 1", drops, DropPartTooLarge)
	}
}

// DOC-PARAM-8: a sender-supplied name is never a path, never rewrites a log
// line, and never renders as an extension it does not have.
func TestASenderCannotChooseADangerousFilename(t *testing.T) {
	for name, given := range map[string]string{
		"a path":                "../../etc/passwd",
		"a windows path":        `..\\..\\windows\\system32`,
		"a newline in the name": "invoice\r\nX-Injected: yes.pdf",
		"a right-to-left flip":  "invoice\u202egpj.exe",
	} {
		t.Run(name, func(t *testing.T) {
			got := SafeFilename(given, 1)
			for _, forbidden := range []string{"/", `\`, "\n", "\r", "\u202e"} {
				if strings.Contains(got, forbidden) {
					t.Errorf("SafeFilename(%q) = %q, which still contains %q", given, got, forbidden)
				}
			}
			if got == "" {
				t.Error("a sanitized name must still be something a reader can point at")
			}
		})
	}
}

// A name that sanitizes away entirely still needs to be something a reader can
// point at, and the ordinal is the one true thing we know about the file.
func TestAFileWithNoUsableNameIsNamedByItsPosition(t *testing.T) {
	for _, given := range []string{"", "   ", "...", "/////"} {
		if got := SafeFilename(given, 4); got != "attachment-4" {
			t.Errorf("SafeFilename(%q) = %q, want attachment-4", given, got)
		}
	}
}

// DOC-PARAM-9 / DOC-AC-13: the bytes govern, and the sender's disagreeing claim
// is kept rather than resolved away.
func TestTheBytesDecideTheTypeAndTheSendersClaimIsKept(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("From: her@example.com\r\nTo: me@ours.example\r\n")
	b.WriteString("Message-ID: <m2@example.com>\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=B\r\n\r\n")
	b.WriteString("--B\r\nContent-Type: text/plain\r\n\r\nhi\r\n")
	// Declared a PDF, actually a PNG.
	b.WriteString("--B\r\nContent-Type: application/pdf\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"invoice.pdf\"\r\n\r\n")
	b.WriteString("\x89PNG\r\n\x1a\n" + strings.Repeat("d", 40) + "\r\n")
	b.WriteString("--B--\r\n")

	parts, _ := parseParts(t, b.Bytes())
	if len(parts) != 1 {
		t.Fatalf("read %d files, want 1", len(parts))
	}
	if parts[0].ContentType != "image/png" {
		t.Errorf("content type = %q, want image/png — the bytes govern, not the claim",
			parts[0].ContentType)
	}
	if parts[0].DeclaredType != "application/pdf" {
		t.Errorf("declared type = %q, want the disagreeing claim kept for inspection",
			parts[0].DeclaredType)
	}
}

// An agreeing claim is NOT stored: a column filled on every row makes the one
// interesting case invisible.
func TestAnAgreeingClaimIsNotRecordedAsADisagreement(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("From: her@example.com\r\nTo: me@ours.example\r\n")
	b.WriteString("Message-ID: <m4@example.com>\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=B\r\n\r\n")
	b.WriteString("--B\r\nContent-Type: text/plain\r\n\r\nhi\r\n")
	// Declared text/plain over bytes that sniff as text/plain: the claim and
	// the file agree, which is the ordinary case and must leave no trace.
	b.WriteString("--B\r\nContent-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"notes.txt\"\r\n\r\n")
	b.WriteString("just some notes\r\n")
	b.WriteString("--B--\r\n")

	parts, _ := parseParts(t, b.Bytes())
	if len(parts) != 1 {
		t.Fatalf("read %d files, want 1", len(parts))
	}
	if parts[0].ContentType != "text/plain" {
		t.Fatalf("content type = %q, want text/plain", parts[0].ContentType)
	}
	if parts[0].DeclaredType != "" {
		t.Errorf("declared type = %q, want empty — it did not disagree with %q",
			parts[0].DeclaredType, parts[0].ContentType)
	}
}

// A message with no attachment parts reports no files and no drops, which is
// the case every existing captured message is.
func TestAMessageWithNoFilesReportsNeitherFilesNorDrops(t *testing.T) {
	raw := []byte("From: her@example.com\r\nTo: me@ours.example\r\n" +
		"Message-ID: <m3@example.com>\r\nContent-Type: text/plain\r\n\r\nJust text.\r\n")
	parts, drops := parseParts(t, raw)
	if len(parts) != 0 || len(drops) != 0 {
		t.Errorf("parts = %v, drops = %v, want neither", parts, drops)
	}
}
