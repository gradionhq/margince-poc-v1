// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// A refused tool call answers the MODEL, and that answer becomes an
// observation in a run's cumulative transcript — carried into every later
// prompt of the run and persisted across a suspension. The decoder quotes
// the caller's own JSON key back verbatim, so the message is a field the
// model writes: it is bounded, the way the tool name it also chooses is.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

func TestBadArgsErrorBoundsWhatTheCallerCanEchoBack(t *testing.T) {
	var args struct {
		Segment string `json:"segment"`
	}
	// An unknown field, named by the caller: the decoder puts that name in
	// its message, so the name's length is the caller's to choose.
	paragraph := strings.Repeat("payload", 400)
	err := decodeArgs(json.RawMessage(`{"`+paragraph+`":1}`), &args)

	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("an unknown field → %v, want BadArgsError", err)
	}
	if got := len(bad.Error()); got > len("arguments: ")+maxBadArgsDetail+len("…") {
		t.Errorf("a refusal carries %d bytes of caller-chosen text; the bound is %d", got, maxBadArgsDetail)
	}
	if !strings.HasPrefix(bad.Error(), "arguments: ") {
		t.Errorf("the refusal lost its prefix: %q", bad.Error())
	}
	// Bounded, not mangled: a truncated message still has to be a string a
	// prompt can carry.
	if !utf8.ValidString(bad.Error()) {
		t.Errorf("the bound cut a rune in half: %q", bad.Error())
	}
}

// A short, ordinary decoder message is passed through whole — the bound
// exists to stop an essay, not to hide what went wrong.
func TestBadArgsErrorKeepsAnOrdinaryMessageWhole(t *testing.T) {
	var args struct {
		Limit int `json:"limit"`
	}
	err := decodeArgs(json.RawMessage(`{"limit":"ten"}`), &args)

	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("a mistyped field → %v, want BadArgsError", err)
	}
	if strings.Contains(bad.Error(), "…") {
		t.Errorf("an ordinary decoder message was truncated: %q", bad.Error())
	}
	if !strings.Contains(bad.Error(), "limit") {
		t.Errorf("the refusal does not say which field was wrong: %q", bad.Error())
	}
}

func TestALongUnknownKeyDoesNotEatTheAcceptedFieldList(t *testing.T) {
	// The bound is on the CALLER's echo, and a refusal built from both halves
	// used to spend it on the caller's key and truncate our own accepted-field
	// list mid-word — deleting the only part of the message that says what to do
	// next, precisely when the caller has proved it does not know.
	long := strings.Repeat("wrongkey", 60)
	err := rejectUnknownFields(createShapes, "person", json.RawMessage(`{"`+long+`":"x"}`))

	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("an unknown field → %v, want BadArgsError", err)
	}
	message := bad.Error()
	// The caller's half is still bounded — that property is the reason the bound
	// exists and it has to survive the split. The marker proves the echo was cut,
	// so this key really is long enough to have eaten the old budget.
	if !strings.Contains(message, "…") {
		t.Errorf("a %d-byte unknown key was echoed unbounded: %q", len(long), message)
	}
	if !strings.Contains(message, "wrongkey") {
		t.Errorf("the refusal does not quote the key it refused: %q", message)
	}
	// Our half arrives whole. Checking the LAST accepted name, not merely that
	// the word "accepts" appears: truncation kept the opening and dropped the
	// end, so a prefix check would have passed on the defect.
	accepted := contractFieldNames(createShapes[datasource.EntityPerson])
	last := accepted[len(accepted)-1]
	if !strings.Contains(message, last) {
		t.Errorf("the accepted-field list is cut short — %q is missing from %q", last, message)
	}
}

func TestBoundDetailCutsOnARuneBoundary(t *testing.T) {
	// Three-byte runes over a bound that lands mid-sequence: the cut walks
	// back to the rune start rather than emitting half of one.
	s := strings.Repeat("日", 10)
	for n := 1; n <= len(s); n++ {
		got := boundDetail(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("boundDetail(%d) produced invalid UTF-8: %q", n, got)
		}
	}
}
