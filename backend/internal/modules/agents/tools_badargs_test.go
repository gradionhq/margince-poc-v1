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
	"time"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// A refused call is read by a model, which can act on the argument name and the
// shape it wanted and on nothing else. The Go struct decodeArgs was filling is
// both unactionable and none of an agent's business — while the unknown-key
// refusal quotes the caller's OWN argument name, which is the most actionable
// thing this surface ever says, so it travels verbatim. It may, because it is
// OUR refusal and carries its own type; the case below asserts that type, since
// telling the two provenances apart by prose is what let a library's sentence
// through in the first place.
func TestDecodeArgsRefusalNamesTheArgumentAndNeverTheProgram(t *testing.T) {
	goInternals := []string{"Go struct", "Go value", "uuid.UUID", "ids.UUID", "2006", "github.com/"}
	for _, tc := range []struct{ name, in, want string }{
		{"a mistyped scalar", `{"limit":"ten"}`, "`limit` must be an integer, not a string"},
		{"arguments that are not an object", `[1]`, "must be a JSON object, not an array"},
		{"a timestamp", `{"since":"tomorrow"}`, `"tomorrow" is not an RFC 3339 timestamp`},
		{"our own uuid refusal", `{"id":"nope"}`, `"nope" is not a canonical UUID`},
		{"the unknown-key echo", `{"limitt":1}`, `unknown field "limitt"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var args struct {
				Limit int       `json:"limit"`
				ID    ids.UUID  `json:"id"`
				Since time.Time `json:"since"`
			}
			err := decodeArgs(json.RawMessage(tc.in), &args)
			var bad *BadArgsError
			if !errors.As(err, &bad) {
				t.Fatalf("arguments %s → %v, want BadArgsError", tc.in, err)
			}
			if !strings.Contains(bad.Error(), tc.want) {
				t.Errorf("refusal = %q, want it to say %q", bad.Error(), tc.want)
			}
			for _, leak := range goInternals {
				if strings.Contains(bad.Error(), leak) {
					t.Errorf("the refusal carries %q, which describes this program: %q", leak, bad.Error())
				}
			}
		})
	}
}

// The unknown-key refusal this surface echoes is OURS, and the assertion is its
// TYPE. That is the whole mechanism separating the one decode message an agent may
// read from the ones it may not: encoding/json's own unknown-field text is prose
// only a string match could recognise, and a string match is how a library's
// sentence reached a caller on the sibling route.
func TestTheUnknownArgumentRefusalIsOurOwnTypedOne(t *testing.T) {
	var args struct {
		Limit int `json:"limit"`
	}
	// A key that only case-FOLDS onto an argument counts as unknown here, exactly
	// as it does on the REST body and at the provider seam: encoding/json would
	// have matched it and written the value.
	for _, in := range []string{`{"limitt":1}`, `{"LIMIT":1}`} {
		err := decodeArgs(json.RawMessage(in), &args)
		var unknown *datasource.UnknownFieldError
		if !errors.As(err, &unknown) {
			t.Fatalf("arguments %s → %v, want a datasource.UnknownFieldError the surface may echo verbatim", in, err)
		}
	}
}

// errValueUnmarshalerInternals stands for the words a value type's own
// UnmarshalJSON writes about the program that called it — google/uuid's
// `invalid UUID length: 6` is the shipped example, and no branch of the
// restatement can name that shape.
var errValueUnmarshalerInternals = errors.New("cannot unmarshal into Go struct field crmsecrets.Vault.key")

type thirdPartyValue string

func (*thirdPartyValue) UnmarshalJSON([]byte) error { return errValueUnmarshalerInternals }

// A decode failure no branch can NAME is masked, not echoed. Nil from the
// restatement means "no shape recognised", never "safe to show": what is left at
// that point was written about this program, by an author the caller never
// addressed. The caller gets a sentence saying what to check; the withheld words
// stay in the chain, which is what the log reads.
func TestAnUnnameableDecodeFailureIsMaskedAndKeptForTheLog(t *testing.T) {
	var args struct {
		Segment thirdPartyValue `json:"segment"`
	}
	err := decodeArgs(json.RawMessage(`{"segment":"x"}`), &args)

	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("a value unmarshaler's refusal → %v, want BadArgsError", err)
	}
	if strings.Contains(bad.Error(), "Go struct field") || strings.Contains(bad.Error(), "crmsecrets") {
		t.Errorf("the refusal repeats a value unmarshaler's words about this program: %q", bad.Error())
	}
	if !strings.Contains(bad.Error(), "check each value's type") {
		t.Errorf("the masked refusal says nothing to act on: %q", bad.Error())
	}
	if !errors.Is(err, errValueUnmarshalerInternals) {
		t.Error("the withheld original is not reachable through the chain, so an operator's log gets nothing")
	}
}

// Arguments are ONE JSON value. A second value is a refusal, not a truncation:
// encoding/json's decoder happily reads the first and leaves the rest in the
// buffer, so without the boundary a caller's second object — the corrected one,
// in the shape a retry takes — vanishes and the call proceeds on the first as if
// nothing else had been sent. The REST body decode draws the same line, which is
// the point: the surfaces have to agree on what a payload IS.
func TestToolArgumentsAreExactlyOneJSONValue(t *testing.T) {
	var args struct {
		Limit int `json:"limit"`
	}
	err := decodeArgs(json.RawMessage(`{"limit":1} {"limit":99}`), &args)

	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("a second JSON value → %v (limit decoded as %d), want BadArgsError — the discarded value is "+
			"the caller's, and dropping it silently is how a corrected retry runs as its own first draft",
			err, args.Limit)
	}
	if !strings.Contains(bad.Error(), "trailing content") {
		t.Errorf("the refusal does not say what was wrong with the payload: %q", bad.Error())
	}
	if !strings.Contains(bad.Error(), "exactly one JSON object") {
		t.Errorf("the refusal does not say what to send instead: %q", bad.Error())
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
