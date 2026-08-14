// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"strings"
	"testing"
)

func TestATranscriptTooLargeForOneReadingIsRefusedRatherThanReadInPart(t *testing.T) {
	ordinary := []string{"Dana: hello.", "Priya: hello."}
	if err := WithinReadingBounds(ordinary); err != nil {
		t.Fatalf("an ordinary transcript is within bounds, got %v", err)
	}

	tooManyLines := make([]string, MaxReadableTranscriptLines+1)
	for i := range tooManyLines {
		tooManyLines[i] = "Dana: noted."
	}
	err := WithinReadingBounds(tooManyLines)
	if err == nil {
		t.Fatal("a transcript past the line bound must be refused; a reading of its first half is indistinguishable from a reading of all of it")
	}
	if !strings.Contains(err.Error(), "log the meeting as more than one transcript") {
		t.Errorf("the refusal must say what to do about it, got %q", err.Error())
	}

	if WithinReadingBounds([]string{strings.Repeat("x", MaxReadableTranscriptChars+1)}) == nil {
		t.Error("the character bound must hold even when the line bound does not")
	}
}

func TestTheTooLongRefusalIsAFieldFaultSoItReadsAs422(t *testing.T) {
	var err error = &TranscriptTooLongError{Lines: 900, Chars: 90000}
	fault, ok := err.(interface {
		FieldFault() (string, string, string)
	})
	if !ok {
		t.Fatal("the refusal must carry a FieldFault, or the client gets a 500 for a request it could fix")
	}
	field, code, message := fault.FieldFault()
	if field == "" || code == "" || !strings.Contains(message, "900 lines") {
		t.Errorf("the fault must name the field, a code, and the size given: %q/%q/%q", field, code, message)
	}
}

func TestTheLineSplitIsOneBasedAndKeepsEveryLine(t *testing.T) {
	lines := transcriptLines("first\nsecond\n\nfourth")
	if len(lines) != 4 {
		t.Fatalf("an empty turn is still a line, or every number after it shifts; got %d lines", len(lines))
	}
	if lines[0] != "first" || lines[3] != "fourth" {
		t.Errorf("line N must be the Nth segment; got %q at 1 and %q at 4", lines[0], lines[3])
	}
}
