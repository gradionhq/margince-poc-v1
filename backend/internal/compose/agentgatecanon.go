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

// canonicalHeaders is the two REQUEST headers that change what a staged
// call executes, sorted (a Go map marshals its keys in sorted order, so
// the type carries the ordering rather than a caller having to remember
// it). If-Match decides whether the write happens at all; Idempotency-Key
// decides whether it is a fresh effect, a replay, or a conflict — both
// reach the handler, so both are part of what "the identical call" means.
// Every other header (Authorization, User-Agent, tracing headers, …) does
// not change execution and is deliberately excluded: hashing one would
// make an approval unredeemable from a different client, which is a worse
// bug than the one this guards against.
func canonicalHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, name := range []string{"If-Match", idempotencyKeyHeader} {
		if v := h.Get(name); v != "" {
			out[name] = v
		}
	}
	return out
}

// canonicalRESTCall canonicalizes the request into the bytes both staging
// and redemption hash: decoding into maps and re-marshaling sorts keys at
// every depth, so "identical call" is a property of content, not of the
// client's serialization habits.
//
// Numbers survive as json.Number (UseNumber): decoding through a bare
// `any` renders every JSON number as float64, which loses precision past
// 2^53 while the handler that later decodes the same bytes reads an exact
// int64 — two distinct integers would canonicalize alike and a hash
// collision would let one redeem the other's approval.
//
// UTF-8 is checked on both sides of the decode, matching the tool door's
// two-halved check (modules/agents/reserved.go): utf8.Valid on the raw
// bytes catches malformed encoding BEFORE the decode destroys the
// evidence (encoding/json replaces an invalid byte with U+FFFD, so two
// different wire bodies would arrive as one string); the replacement-rune
// scan on the canonical form catches an escaped unpaired surrogate
// (`"\udcff"`), which is valid UTF-8 on the wire and still decodes to
// U+FFFD, so the byte check cannot see it.
func canonicalRESTCall(op, path string, headers http.Header, body []byte) (json.RawMessage, string, error) {
	if !utf8.Valid(body) {
		return nil, "", httperr.Validation("body", "invalid_utf8", "request body must be valid UTF-8")
	}
	var payload any
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			return nil, "", httperr.Validation("body", "invalid_json", "request body must be valid JSON")
		}
	}
	canonical, err := json.Marshal(map[string]any{
		"operation": op,
		"path":      path,
		"headers":   canonicalHeaders(headers),
		"body":      payload,
	})
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
