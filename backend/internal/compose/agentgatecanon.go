// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The canonicalization the REST agent gate hashes for staging and redemption.
//
// Split from agentgate.go on the 500-line cap, along a boundary already
// implicit in the file: agentgate.go decides admission, and this is the one
// thing both stageOrRedeem (agentgatestaging.go) and splitOrRedeemUpdate
// (agentsplit.go) hash a call through — kept together because a caller that
// diverges on operation, path, headers or body diverges on the identity a
// staged approval binds to.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// canonicalHeaders is the one REQUEST header that changes what a staged call
// executes without the gate itself already deciding it: Idempotency-Key says
// whether a retry is a fresh effect, a replay or a conflict, and it reaches
// the handler exactly as the caller sent it.
//
// If-Match is deliberately NOT here, though it is also execution-relevant.
// On the redemption path the gate OVERWRITES the caller's If-Match with the
// server-side version pin taken at staging time (agentgatestaging.go,
// redeemIfPresented) before the handler ever reads it, and on the
// field-ownership split path (agentsplit.go) the auto-execute half can
// advance the record's version between staging and redemption while the
// staged residue's hash must still match on retry — hashing If-Match would
// make that retry unredeemable by the version the agent just saw, without
// protecting anything the server-side pin does not already protect.
//
// Every other header (Authorization, User-Agent, tracing headers, …) is
// excluded for the same reason it always was: hashing one would make an
// approval unredeemable from a different client, which is a worse bug than
// the one this guards against.
func canonicalHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	if v := h.Get(idempotencyKeyHeader); v != "" {
		out[idempotencyKeyHeader] = v
	}
	return out
}

// canonicalRESTCall canonicalizes the request into the bytes both staging
// and redemption hash: decoding into maps and re-marshaling sorts keys at
// every depth and folds equivalent number spellings ("1" vs "1.0" vs "1e0")
// to the same value, so "identical call" is a property of content, not of
// the client's serialization habits. The decode into `any` also draws the
// one-value boundary this tree already draws elsewhere (httperr.Decode,
// modules/agents/badargs.go): a body carrying trailing content after the
// JSON value — `{"a":1} garbage`, `{"a":1}{"b":2}` — is refused rather than
// silently truncated to its first value.
//
// The `headers` member is present only when canonicalHeaders found
// something to carry: a call presenting no hashed header canonicalizes
// byte-for-byte as it did before this member existed, so a REST-agent
// approval or redemption token minted before headers joined the hash stays
// redeemable. Adding the empty member unconditionally would have changed
// the hash of every call, headered or not.
//
// UTF-8 is checked on both sides of the decode, matching the tool door's
// two-halved check (modules/agents/reserved.go): utf8.Valid on the raw
// bytes catches malformed encoding BEFORE the decode destroys the evidence
// (encoding/json replaces an invalid byte with U+FFFD, so two different
// wire bodies would arrive as one string); the replacement-rune scan on the
// canonical form catches an escaped unpaired surrogate (`"\udcff"`), which
// is valid UTF-8 on the wire and still decodes to U+FFFD, so the byte check
// cannot see it.
func canonicalRESTCall(op, path string, headers http.Header, body []byte) (json.RawMessage, string, error) {
	if !utf8.Valid(body) {
		return nil, "", httperr.Validation("body", "invalid_utf8", "request body must be valid UTF-8")
	}
	var payload any
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, "", httperr.Validation("body", "invalid_json", "request body must be valid JSON")
		}
	}
	fields := map[string]any{"operation": op, "path": path, "body": payload}
	if hdrs := canonicalHeaders(headers); len(hdrs) > 0 {
		fields["headers"] = hdrs
	}
	canonical, err := json.Marshal(fields)
	if err != nil {
		return nil, "", err
	}
	if bytes.ContainsRune(canonical, utf8.RuneError) {
		return nil, "", httperr.Validation("body", "invalid_utf8",
			"request body contains the Unicode replacement character, which makes two different calls indistinguishable")
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}
