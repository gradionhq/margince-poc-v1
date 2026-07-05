// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package diffhash spells the ONE canonicalization a diff_hash carries
// (ADR-0036): decode into interface maps, re-marshal — which sorts keys
// at every depth — and hash those bytes. Staging, redemption, and
// modify-then-approve all hash through here, so "identical call" is a
// property of content, never of whitespace or a client's key order.
package diffhash

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Canonical re-serializes one JSON object canonically and returns the
// bytes together with their diff_hash.
func Canonical(raw json.RawMessage) (json.RawMessage, string, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", fmt.Errorf("diffhash: payload is not a JSON object: %w", err)
	}
	return Object(m)
}

// Object canonicalizes an already-decoded object.
func Object(m map[string]any) (json.RawMessage, string, error) {
	canonical, err := json.Marshal(m)
	if err != nil {
		return nil, "", fmt.Errorf("diffhash: canonicalize: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}
