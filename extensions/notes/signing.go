// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package notes

// The secrets surface, demonstrated by USE rather than by disclosure.
//
// Nothing here returns the key, or any part of it, masked or otherwise. That is
// the demonstration: an HMAC signature is what a real connector needs a stored
// credential for (a webhook signature, a request signature), so signing a
// payload exercises the production pattern instead of a display affordance
// nothing would ship. The status operation answers presence and nothing else,
// because presence is the only fact about a sealed credential a screen can act
// on.
//
// The namespace wall is not enforced in this file and could not be: the Secrets
// port a handler holds closes over the invoking unit, so "read notes's key"
// is not something another unit's handler can express. See
// backend/internal/compose/extruntime_integration_test.go for the wall driven
// against this unit's own key name.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// signingKeyName is the unit's own bare key name, and must equal the Secrets
// declaration in New() — the declaration is what tells an operator this unit
// expects a workspace-scoped `signing` key before it ever runs, and a
// declaration naming a key nothing reads describes a secret that does not
// exist. New() spells it as a literal because the manifest is derived from that
// function's AST; here it is a constant because this is ordinary code.
const signingKeyName = "signing"

// signatureAlgorithm is reported alongside every signature, so a verifier is
// never left inferring the construction from the digest length.
const signatureAlgorithm = "hmac-sha256"

// maxSigningKey and maxPayload mirror the contract's declared bounds. The
// contract advertises them; these enforce them, since nothing on this seam
// validates a body against the published schema before the handler runs.
//
// Counted in RUNES, because that is what the contract's maxLength: 4096 counts:
// JSON Schema bounds characters, so a byte count here refuses a key or a
// payload the schema handed the client says will fit — and the refusal names a
// length nothing the author can see agrees with. Every non-ASCII value is
// affected, and the shorter the alphabet's byte-per-character ratio the earlier
// it breaks.
const (
	maxSigningKey = 4096
	maxPayload    = 4096
)

// storeSigningKey seals (or replaces) this workspace's signing key.
//
// Replacement IS rotation: the port destroys the superseded material once the
// new value is durable, so a key rotated on a schedule does not accumulate
// blobs the core can no longer name.
func storeSigningKey(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	args, err := decode[struct {
		Key string `json:"key"`
	}](in)
	if err != nil {
		return nil, err
	}
	// Trimmed before the emptiness check, not after storing: a key of spaces
	// would sign perfectly well and be impossible for whoever pasted it to
	// reproduce.
	key := strings.TrimSpace(args.Key)
	switch {
	case key == "":
		return nil, errors.New("notes: the signing key is empty")
	case utf8.RuneCountInString(key) > maxSigningKey:
		return nil, fmt.Errorf("notes: the signing key is at most %d characters, this one is %d", maxSigningKey, utf8.RuneCountInString(key))
	}
	if err := rt.Secrets().Put(ctx, signingKeyName, []byte(key)); err != nil {
		return nil, err
	}
	return json.Marshal(storedResult{Stored: true})
}

// storedResult is the shape both key operations answer with: one boolean, and
// nothing derived from the material.
type storedResult struct {
	Stored bool `json:"stored"`
}

// signingKeyStatus reports whether a key is stored.
//
// It reads the key to answer, and deliberately does not keep, hash, truncate or
// measure it — a length is a fact about the material, and this operation's
// whole claim is that it discloses none.
func signingKeyStatus(ctx context.Context, rt extension.Runtime, _ json.RawMessage) (json.RawMessage, error) {
	switch _, err := rt.Secrets().Get(ctx, signingKeyName); {
	case errors.Is(err, extension.ErrSecretNotFound):
		return json.Marshal(storedResult{Stored: false})
	case err != nil:
		return nil, err
	}
	return json.Marshal(storedResult{Stored: true})
}

// signPayload returns the HMAC-SHA256 of a payload under the stored key.
func signPayload(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	args, err := decode[struct {
		Payload string `json:"payload"`
	}](in)
	if err != nil {
		return nil, err
	}
	// NOT trimmed: a payload's bytes are the thing being signed, and a
	// signature over a value the caller did not send verifies against nothing.
	switch {
	case args.Payload == "":
		return nil, errors.New("notes: there is no payload to sign")
	case utf8.RuneCountInString(args.Payload) > maxPayload:
		return nil, fmt.Errorf("notes: a payload is at most %d characters, this one is %d", maxPayload, utf8.RuneCountInString(args.Payload))
	}
	key, err := rt.Secrets().Get(ctx, signingKeyName)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return nil, fmt.Errorf("notes: this workspace has stored no signing key yet: %w", err)
		}
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	// hash.Hash never reports a write error; the interface documents it, and
	// checking it here would be a branch no input can reach.
	mac.Write([]byte(args.Payload))
	return json.Marshal(struct {
		Algorithm string `json:"algorithm"`
		Signature string `json:"signature"`
	}{Algorithm: signatureAlgorithm, Signature: hex.EncodeToString(mac.Sum(nil))})
}
