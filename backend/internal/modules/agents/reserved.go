// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The arguments the SURFACE owns, which no handler ever sees.
//
// Two of them today. `approval_id` asserts that a human released this exact
// call (approvals.go), and `idempotency_key` asks that the call be safe to
// repeat (idempotency.go). Neither is part of what a tool DOES, so neither
// reaches its decode — a handler that had to remember to ignore them would be
// thirty handlers that can each forget one.
//
// ONE READING of the caller's bytes decides both, and then the diff_hash. That
// is not tidiness: `encoding/json` matches members case-insensitively and takes
// the last of a duplicate pair while a map lookup does neither, so two
// components reading one wire value by different means eventually disagree —
// the defect class C1 found three instances of. Whatever is popped here is
// popped from the object the digest is then computed over, so there is nothing
// for a second reading to differ about.
//
// BOTH ARE POPPED BEFORE THE HASH, and that ordering is load-bearing for the 🟡
// loop: a refused call is staged under the hash of its arguments, and its
// post-approval retry must hash identically. A retry that added — or dropped —
// a retry key would otherwise present a call the human never approved.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The reserved member names, spelled once. Both the pop below and the schema
// splice that advertises them read these, so a rename cannot leave the surface
// accepting one spelling and advertising another.
const (
	approvalIDArg     = "approval_id"
	idempotencyKeyArg = "idempotency_key"
)

var (
	errInvalidApprovalID    = errors.New("`approval_id` must be a UUID string")
	errInvalidRetryKey      = errors.New("`idempotency_key` must be a string")
	errEmptyRetryKey        = errors.New("`idempotency_key` is empty; omit it, or send the key you will retry with")
	errRetryKeyTooLong      = fmt.Errorf("`idempotency_key` is longer than %d characters", maxRetryKeyLen)
	errControlInRetryKey    = errors.New("`idempotency_key` contains a control character; use printable text")
	errArgumentsNotUTF8     = errors.New("the arguments are not valid UTF-8")
	errArgumentsNotAnObject = errors.New("the arguments must be a JSON object")
)

// maxRetryKeyLen bounds the caller-chosen retry key. It is the same NUMBER the
// REST middleware applies to the Idempotency-Key header — one promise, one
// bound — counted in the unit each wire format actually has: that door measures
// a header, which is bytes, and this one measures a JSON string against a
// published `maxLength`, which JSON Schema defines as characters. Counting
// bytes here would refuse keys this server's own schema calls legal. The column
// behind both is `text`, so neither unit is a storage constraint.
const maxRetryKeyLen = 255

// reserved is one call's surface-owned arguments, plus what is left of it.
type reserved struct {
	// Args is the call WITHOUT the reserved members, canonicalized — the bytes
	// a handler decodes and the bytes the digest was taken over.
	Args json.RawMessage
	// ApprovalID is the release a retry asserts; zero when none was presented.
	ApprovalID ids.ApprovalID
	// RetryKey is the caller's idempotency key; empty when none was presented.
	RetryKey string
	// DiffHash identifies the CALL: the same arguments in any spelling produce
	// the same hash, which is what lets an approval bind to one call and a
	// retry key notice a different one.
	DiffHash string
}

// splitReserved pops the surface's own arguments and canonicalizes what remains
// through the shared diffhash spelling, so "the identical call" is a property of
// content rather than of whitespace or key order.
func splitReserved(in json.RawMessage) (reserved, error) {
	// Before the decode, because the decode DESTROYS the evidence: encoding/json
	// replaces every invalid byte with U+FFFD, so two different wire keys arrive
	// as one string and would claim, and replay, a single claim between them.
	if !utf8.Valid(in) {
		return reserved{}, &BadArgsError{Cause: errArgumentsNotUTF8}
	}
	var m map[string]any
	if err := json.Unmarshal(in, &m); err != nil {
		return reserved{}, &BadArgsError{Cause: err}
	}
	if m == nil {
		// `null` decodes into a nil map without error, and would reach a handler
		// as an argument-less call — while every tool on this surface advertises
		// an object input. An absent object and an empty one are different
		// claims; only one of them is something a caller can have meant.
		return reserved{}, &BadArgsError{Cause: errArgumentsNotAnObject}
	}
	var out reserved
	if raw, ok := m[approvalIDArg]; ok {
		s, isStr := raw.(string)
		if !isStr {
			return reserved{}, &BadArgsError{Cause: errInvalidApprovalID}
		}
		id, err := ids.ParseAs[ids.ApprovalKind](s)
		if err != nil {
			return reserved{}, &BadArgsError{Cause: err}
		}
		out.ApprovalID = id
		delete(m, approvalIDArg)
	}
	if raw, ok := m[idempotencyKeyArg]; ok {
		key, isStr := raw.(string)
		if !isStr {
			return reserved{}, &BadArgsError{Cause: errInvalidRetryKey}
		}
		if err := checkRetryKey(key); err != nil {
			return reserved{}, &BadArgsError{Cause: err}
		}
		out.RetryKey = key
		delete(m, idempotencyKeyArg)
	}
	canonical, hash, err := diffhash.Object(m)
	if err != nil {
		return reserved{}, err
	}
	out.Args, out.DiffHash = canonical, hash
	return out, nil
}

// checkRetryKey holds a presented key to what the schema advertises.
//
// An empty or blank key is REFUSED rather than read as "no key". A model that
// sends `""` asked for retry safety and would silently not get it, and what
// that hides is a second irreversible act — the one thing the key exists to
// prevent.
//
// The length is counted in RUNES, because that is what the advertised
// `maxLength` counts: JSON Schema measures characters and Go's len measures
// UTF-8 bytes, so a byte count would refuse keys the schema this surface
// publishes says are legal — the advertised-vs-enforced split one axis over.
//
// A CONTROL CHARACTER is refused for a reason worth stating: `text` cannot hold
// a NUL, so a key carrying one fails at the INSERT rather than here — and a
// failed claim refuses the call. That is safe, but it is a refusal the caller
// cannot act on ("could not be made safe to retry") for a defect that is
// entirely in their own argument. Naming it here is the difference between an
// answer and a mystery.
func checkRetryKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errEmptyRetryKey
	}
	if utf8.RuneCountInString(key) > maxRetryKeyLen {
		return errRetryKeyTooLong
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return errControlInRetryKey
		}
	}
	return nil
}
