// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package ids provides the UUIDv7 identifier every entity row uses
// (data-model §1.1). It is part of the dependency-free kernel: seam
// packages reference ids.UUID so no Tier-0 signature drags in a
// third-party UUID library.
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// UUID is a RFC 9562 UUID in big-endian byte order.
type UUID [16]byte

// Nil is the zero UUID.
var Nil UUID

var v7 = struct {
	sync.Mutex
	lastMs uint64
	seq    uint16 // 12-bit counter in rand_a
}{}

// NewV7 returns a UUIDv7: a 48-bit Unix millisecond timestamp, a 12-bit
// monotonic counter (RFC 9562 method 1), and 62 random bits. IDs minted
// by one process strictly sort by creation order, which keeps B-tree
// inserts append-mostly — the reason data-model §1.1 mandates v7.
func NewV7() UUID {
	var u UUID
	if _, err := rand.Read(u[8:]); err != nil {
		// crypto/rand never fails on supported platforms; a failure here
		// means the process cannot mint identity and must not continue.
		panic(fmt.Sprintf("ids: crypto/rand unavailable: %v", err))
	}

	v7.Lock()
	ms := uint64(time.Now().UnixMilli())
	switch {
	case ms > v7.lastMs:
		v7.lastMs = ms
		// Re-seed the counter each millisecond with headroom for 2^11
		// increments before overflow, keeping IDs unguessable across ms.
		v7.seq = uint16(binary.BigEndian.Uint16(u[8:10])) & 0x07FF
	case v7.seq < 0x0FFF:
		v7.seq++
	default:
		// Counter exhausted within one millisecond: borrow the next one.
		v7.lastMs++
		v7.seq = 0
	}
	ms, seq := v7.lastMs, v7.seq
	v7.Unlock()

	u[0] = byte(ms >> 40 & 0xFF)
	u[1] = byte(ms >> 32 & 0xFF)
	u[2] = byte(ms >> 24 & 0xFF)
	u[3] = byte(ms >> 16 & 0xFF)
	u[4] = byte(ms >> 8 & 0xFF)
	u[5] = byte(ms & 0xFF)

	u[6] = 0x70 | byte(seq>>8)&0x0F // version 7 + counter high bits
	u[7] = byte(seq & 0xFF)

	u[8] = u[8]&0x3F | 0x80 // variant 10

	return u
}

// Parse reads the canonical 8-4-4-4-12 hex form.
func Parse(s string) (UUID, error) {
	var u UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return Nil, fmt.Errorf("ids: %q is not a canonical UUID", s)
	}
	hexed := s[:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	if _, err := hex.Decode(u[:], []byte(hexed)); err != nil {
		return Nil, fmt.Errorf("ids: %q is not a canonical UUID", s)
	}
	return u, nil
}

// MustParse is Parse for compile-time-known literals (tests, seeds).
func MustParse(s string) UUID {
	u, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func (u UUID) String() string {
	var b [36]byte
	hex.Encode(b[:8], u[:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:], u[10:])
	return string(b[:])
}

func (u UUID) IsZero() bool { return u == Nil }

// MarshalText/UnmarshalText make UUID work with encoding/json and
// database drivers that negotiate via encoding.Text{M,Unm}arshaler.
func (u UUID) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

func (u *UUID) UnmarshalText(b []byte) error {
	parsed, err := Parse(string(b))
	if err != nil {
		return err
	}
	*u = parsed
	return nil
}
