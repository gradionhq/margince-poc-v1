// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSplitReservedPopsWhatTheSurfaceOwns(t *testing.T) {
	approval := ids.NewV7()
	in := json.RawMessage(`{"approval_id":"` + approval.String() + `","idempotency_key":"k-1","record_type":"deal"}`)

	res, err := splitReserved(in)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if res.ApprovalID.String() != approval.String() {
		t.Fatalf("approval_id = %s, want %s", res.ApprovalID, approval)
	}
	if res.RetryKey != "k-1" {
		t.Fatalf("retry key = %q, want k-1", res.RetryKey)
	}
	var left map[string]any
	if err := json.Unmarshal(res.Args, &left); err != nil {
		t.Fatalf("canonical args: %v", err)
	}
	if _, present := left[approvalIDArg]; present {
		t.Error("approval_id reached the handler's arguments")
	}
	if _, present := left[idempotencyKeyArg]; present {
		t.Error("idempotency_key reached the handler's arguments")
	}
	if left["record_type"] != "deal" {
		t.Errorf("the tool's own argument did not survive the split: %v", left)
	}
}

// The 🟡 loop depends on this: a refused call is staged under the hash of its
// arguments, and its post-approval retry must present the identical call. A
// retry that added a retry key — or dropped one — would otherwise hash to
// something the human never approved, and redemption would refuse the very
// call it was granted for.
func TestTheRetryKeyIsNotPartOfTheCallAHumanApproves(t *testing.T) {
	bare, err := splitReserved(json.RawMessage(`{"record_type":"deal","reason":"stale"}`))
	if err != nil {
		t.Fatalf("split bare: %v", err)
	}
	keyed, err := splitReserved(json.RawMessage(`{"idempotency_key":"k-9","reason":"stale","record_type":"deal"}`))
	if err != nil {
		t.Fatalf("split keyed: %v", err)
	}
	if bare.DiffHash != keyed.DiffHash {
		t.Fatalf("the same call hashes differently with a retry key: %s vs %s", bare.DiffHash, keyed.DiffHash)
	}
	changed, err := splitReserved(json.RawMessage(`{"idempotency_key":"k-9","reason":"duplicate","record_type":"deal"}`))
	if err != nil {
		t.Fatalf("split changed: %v", err)
	}
	if changed.DiffHash == keyed.DiffHash {
		t.Fatal("two different calls hash the same, so a retry key could never notice a changed payload")
	}
}

func TestSplitReservedRefusesAKeyItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want error
	}{
		{name: "a number is not a key", in: `{"idempotency_key":7}`, want: errInvalidRetryKey},
		{name: "null is not a key", in: `{"idempotency_key":null}`, want: errInvalidRetryKey},
		// Read as "no key", this would withdraw the protection the caller asked
		// for and tell them nothing — and what it fails to prevent is a second
		// irreversible act.
		{name: "an empty key is refused, never read as absent", in: `{"idempotency_key":""}`, want: errEmptyRetryKey},
		{name: "a blank key is refused too", in: `{"idempotency_key":"   "}`, want: errEmptyRetryKey},
		{
			name: "an over-long key is named as the caller's argument",
			in:   `{"idempotency_key":"` + strings.Repeat("k", maxRetryKeyLen+1) + `"}`,
			want: errRetryKeyTooLong,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := splitReserved(json.RawMessage(tc.in))
			var bad *BadArgsError
			if !errors.As(err, &bad) {
				t.Fatalf("err = %v, want a BadArgsError", err)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSplitReservedKeepsRefusingABadApprovalID(t *testing.T) {
	for _, in := range []string{`{"approval_id":7}`, `{"approval_id":"not-a-uuid"}`} {
		if _, err := splitReserved(json.RawMessage(in)); err == nil {
			t.Fatalf("%s was accepted", in)
		}
	}
	if _, err := splitReserved(json.RawMessage(`[]`)); err == nil {
		t.Fatal("a non-object argument list was accepted")
	}
}

// The advertised `maxLength` counts CHARACTERS, so the surface must too: Go's
// len counts UTF-8 bytes, and a byte count would refuse keys the schema this
// server publishes calls legal.
func TestTheKeyBoundCountsCharactersNotBytes(t *testing.T) {
	// maxRetryKeyLen four-byte runes: legal by the advertised schema, and four
	// times the bound in bytes.
	key := strings.Repeat("\U0001F600", maxRetryKeyLen)
	res, err := splitReserved(json.RawMessage(`{"idempotency_key":"` + key + `"}`))
	if err != nil {
		t.Fatalf("a %d-character key was refused: %v", maxRetryKeyLen, err)
	}
	if res.RetryKey != key {
		t.Fatal("the key did not survive the split")
	}
	over, err := json.Marshal(map[string]string{idempotencyKeyArg: key + "\U0001F600"})
	if err != nil {
		t.Fatalf("encoding the over-long probe: %v", err)
	}
	if _, err := splitReserved(over); !errors.Is(err, errRetryKeyTooLong) {
		t.Fatalf("a %d-character key → %v, want the length refusal", maxRetryKeyLen+1, err)
	}
}

// A NUL cannot live in the `text` column the claim is written to, so a key
// carrying one fails at the INSERT — which refuses the call safely, but with a
// message about this surface rather than about the caller's own argument.
func TestAControlCharacterInTheKeyIsNamedAsTheCallersArgument(t *testing.T) {
	for _, key := range []string{"a\x00b", "a\nb", "a\tb", "\x1b[0m"} {
		encoded, err := json.Marshal(map[string]string{idempotencyKeyArg: key})
		if err != nil {
			t.Fatalf("encoding the probe: %v", err)
		}
		if _, err := splitReserved(encoded); !errors.Is(err, errControlInRetryKey) {
			t.Errorf("%q → %v, want the control-character refusal", key, err)
		}
	}
}

// A key at exactly the bound is legal: the refusal is for what exceeds it, and
// an off-by-one here would refuse keys the REST door accepts for the same
// column.
func TestAKeyAtTheBoundIsAccepted(t *testing.T) {
	res, err := splitReserved(json.RawMessage(`{"idempotency_key":"` + strings.Repeat("k", maxRetryKeyLen) + `"}`))
	if err != nil {
		t.Fatalf("a %d-character key was refused: %v", maxRetryKeyLen, err)
	}
	if len(res.RetryKey) != maxRetryKeyLen {
		t.Fatalf("key length = %d, want %d", len(res.RetryKey), maxRetryKeyLen)
	}
}

// `null` decodes into a nil map without error, so it would reach a handler as
// an argument-less call — while every tool on this surface advertises an object
// input. An absent object and an empty one are different claims.
func TestNullArgumentsAreRefusedRatherThanReadAsEmpty(t *testing.T) {
	if _, err := splitReserved(json.RawMessage(`null`)); !errors.Is(err, errArgumentsNotAnObject) {
		t.Fatalf("err = %v, want the not-an-object refusal", err)
	}
	// An EMPTY object is a legal call and stays one.
	if _, err := splitReserved(json.RawMessage(`{}`)); err != nil {
		t.Fatalf("an empty argument object was refused: %v", err)
	}
}

// encoding/json replaces every invalid byte with U+FFFD, so two distinct wire
// keys would arrive as one string and claim — and replay — a single claim
// between them. Checked on the raw bytes, because the decode destroys the
// evidence.
func TestInvalidUTF8ArgumentsAreRefusedBeforeTheyAreNormalized(t *testing.T) {
	first := []byte(`{"idempotency_key":"` + "\xff" + `"}`)
	second := []byte(`{"idempotency_key":"` + "\xfe" + `"}`)
	for _, in := range [][]byte{first, second} {
		if _, err := splitReserved(in); !errors.Is(err, errArgumentsNotUTF8) {
			t.Fatalf("err = %v, want the UTF-8 refusal", err)
		}
	}
	// And the defect that makes the refusal worth having: decoded rather than
	// refused, the two DIFFERENT wire keys are one string — so one caller's
	// recorded result answers the other's key.
	var firstKey, secondKey map[string]string
	if err := json.Unmarshal(first, &firstKey); err != nil {
		t.Fatalf("decoding the first probe: %v", err)
	}
	if err := json.Unmarshal(second, &secondKey); err != nil {
		t.Fatalf("decoding the second probe: %v", err)
	}
	if firstKey[idempotencyKeyArg] != secondKey[idempotencyKeyArg] {
		t.Fatalf("the two probes decode differently (%q vs %q), so this test is not exercising the collision it describes",
			firstKey[idempotencyKeyArg], secondKey[idempotencyKeyArg])
	}
}
