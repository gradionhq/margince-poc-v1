// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package password hashes and verifies user passwords with Argon2id
// (ADR-0043: Argon2id, never plaintext), serialized in PHC string format
// so parameters can be raised later without invalidating existing hashes.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The COST parameters — timeCost, memoryKiB, threads — live in params.go and
// params_integration.go, selected by build tag. The two below do not, and that
// is deliberate: neither is a cost knob, and both have a correctness floor.
//
// keyLength must stay within the 16..64 bytes Verify accepts below; a shorter
// key would make every hash this package writes fail its own Verify, and the
// failure would surface as a login error far from the constant that caused it.
// saltLength changes the PHC encoding and buys no speed. Keeping them out of
// the tagged files is what makes those mistakes unavailable.
const (
	saltLength = 16
	keyLength  = 32
)

// Hash derives a PHC-formatted Argon2id hash.
func Hash(plaintext string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: generating salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, timeCost, memoryKiB, threads, keyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, timeCost, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// ErrMismatch means the password does not match the stored hash.
var ErrMismatch = errors.New("password: mismatch")

// Verify checks plaintext against a PHC-formatted hash in constant time.
func Verify(plaintext, phc string) error {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("password: malformed hash")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return errors.New("password: malformed hash version")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return errors.New("password: malformed hash params")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return errors.New("password: malformed salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return errors.New("password: malformed key")
	}
	// Bound the key length explicitly (any real Argon2id key is 16–64
	// bytes) instead of masking the int→uint32 conversion — the old
	// &0xFFFF mask would have silently truncated an oversized length.
	if len(want) < 16 || len(want) > 64 {
		return errors.New("password: implausible key length")
	}

	got := argon2.IDKey([]byte(plaintext), salt, t, m, p, uint32(len(want))) //nolint:gosec // bounded 16–64 just above
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}
