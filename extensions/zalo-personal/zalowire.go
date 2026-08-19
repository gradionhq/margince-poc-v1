// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The message socket's wire format: how a frame is laid out, and the four ways
// its payload can be encoded.
//
// None of it is documented by Zalo, and none of it is negotiable — every
// constant here was read off a live account. Two of them are the kind of detail
// that costs a day if it is wrong in a way that still almost works, so they are
// stated at the top rather than buried in the function that needs them:
//
//   - The frame header is version(1) + cmd(2, LITTLE-endian uint16) + subCmd(1),
//     and the body is JSON. OUTBOUND frames are never encrypted; only inbound
//     ones may be, which is why encoding a frame takes no key.
//   - The AES-GCM nonce is 16 bytes, not the usual 12, and the 16 bytes after
//     it are additional authenticated data. Go's default cipher.NewGCM cannot
//     open this at all.

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
)

// zaloFrameHeaderBytes is the fixed header every frame carries. The body has no
// length of its own anywhere — the websocket message boundary IS the frame
// boundary — which is why a truncated message cannot be told apart from a
// corrupt one, and why [wireGuard] exists.
const zaloFrameHeaderBytes = 4

// zaloFrameVersion is the only version this protocol has ever spoken.
const zaloFrameVersion = 1

// The frame commands this unit knows. The socket carries dozens more; these are
// the ones it acts on.
const (
	// zaloCmdCipherKey is the socket's first frame. It hands over the payload
	// cipher key — a different key from the session key that signs REST calls —
	// and nothing encrypted can be read before it arrives.
	zaloCmdCipherKey = 1

	// zaloCmdPing is the keep-alive, at the interval getServerInfo names.
	zaloCmdPing = 2

	// zaloCmdUserMessage carries 1:1 messages, inbound and the echo of our own
	// outbound ones alike.
	zaloCmdUserMessage = 501

	// zaloCmdUserBacklog is the 1:1 offline queue and zaloCmdGroupBacklog the
	// group one. They are REQUESTS, not pushes — see [inboxDrain.requestBacklog].
	zaloCmdUserBacklog  = 510
	zaloCmdGroupBacklog = 511
)

// The subCmd values, which share a wire value and mean different things — so
// they are named separately rather than reused.
const (
	zaloSubCmdKeepConnection = 1
	zaloSubCmdRequestBacklog = 1
)

// zaloFrame is one parsed socket message: the header, and the JSON body
// untouched.
type zaloFrame struct {
	version byte
	cmd     uint16
	subCmd  byte
	body    []byte
}

// encodeZaloFrame lays out one outbound control frame around an
// already-rendered JSON body. The body arrives rendered rather than as a value
// to marshal because every frame this unit sends has a fixed shape, and a
// marshal step here would be an error path that cannot happen.
func encodeZaloFrame(cmd uint16, subCmd byte, body []byte) []byte {
	frame := make([]byte, zaloFrameHeaderBytes+len(body))
	frame[0] = zaloFrameVersion
	binary.LittleEndian.PutUint16(frame[1:3], cmd)
	frame[3] = subCmd
	copy(frame[zaloFrameHeaderBytes:], body)
	return frame
}

// decodeZaloFrame reads the header off an inbound websocket message.
//
// A message too short to hold a header is REFUSED rather than skipped. It means
// either a truncated read or a protocol we no longer understand, and both are
// worth an outage: the socket is the only path inbound messages take, so a
// reader that quietly drops what it cannot parse loses records with no way to
// notice — and the server treats a frame as delivered the moment it pushes it.
func decodeZaloFrame(data []byte) (zaloFrame, error) {
	if len(data) < zaloFrameHeaderBytes {
		return zaloFrame{}, fmt.Errorf(
			"a %d-byte socket message cannot hold the %d-byte frame header, so which command it carries is unreadable",
			len(data), zaloFrameHeaderBytes)
	}
	return zaloFrame{
		version: data[0],
		cmd:     binary.LittleEndian.Uint16(data[1:3]),
		subCmd:  data[3],
		body:    data[zaloFrameHeaderBytes:],
	}, nil
}

// unescapeZaloPayload reverses the server's escaping the way the web client's
// decodeURIComponent does.
//
// PathUnescape, NOT QueryUnescape: query semantics read a literal `+` as a
// space, and `+` is in base64's alphabet — so the wrong helper silently
// corrupts roughly any payload long enough to contain one, and surfaces as
// "illegal base64 data at input byte N" pointing at the payload rather than at
// the unescaping.
func unescapeZaloPayload(payload string) (string, error) {
	unescaped, err := url.PathUnescape(payload)
	if err != nil {
		return "", fmt.Errorf("the event payload is not URL-escaped as expected: %w", err)
	}
	return unescaped, nil
}

// decodeZaloEvent reverses whichever of the four payload encodings a frame
// body declares: 0 is plaintext, 1 is base64 then inflate, 2 is unescape then
// base64 then AES-GCM then inflate, 3 is the same without the inflate.
func decodeZaloEvent(body []byte, cipherKey string) ([]byte, error) {
	var parsed struct {
		Data    string `json:"data"`
		Encrypt int    `json:"encrypt"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("the event frame is not JSON (%s): %w", truncate(string(body), 120), err)
	}

	if parsed.Encrypt == 0 {
		return []byte(parsed.Data), nil
	}

	payload := parsed.Data
	if parsed.Encrypt != 1 {
		// encrypt=1 is NOT escaped; only the encrypted kinds are.
		unescaped, err := unescapeZaloPayload(payload)
		if err != nil {
			return nil, err
		}
		payload = unescaped
	}

	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("the event payload is not base64: %w", err)
	}

	if parsed.Encrypt == 1 {
		return inflateZaloPayload(raw)
	}

	plain, err := decryptZaloEvent(raw, cipherKey)
	if err != nil {
		return nil, err
	}
	if parsed.Encrypt == 3 {
		return plain, nil
	}
	return inflateZaloPayload(plain)
}

// zaloGCMNonceBytes is the length of the nonce Zalo prefixes to an encrypted
// payload, and the reason this cannot use cipher.NewGCM: the standard
// constructor is fixed at 12 bytes and rejects a 16-byte nonce outright.
const zaloGCMNonceBytes = 16

// decryptZaloEvent opens the AES-GCM layer. The layout is
// nonce[0:16] ++ additionalData[16:32] ++ ciphertext[32:], and the key is the
// base64-decoded cipher key the cmd=1 frame handed over.
func decryptZaloEvent(raw []byte, cipherKey string) ([]byte, error) {
	if cipherKey == "" {
		return nil, fmt.Errorf("this event is encrypted but the socket's cipher key has not arrived yet, so nothing on it can be read")
	}

	const headerBytes = 2 * zaloGCMNonceBytes
	if len(raw) <= headerBytes {
		return nil, fmt.Errorf(
			"an encrypted event is %d bytes, which is not enough for the %d-byte nonce and additional data plus a ciphertext",
			len(raw), headerBytes)
	}

	key, err := base64.StdEncoding.DecodeString(cipherKey)
	if err != nil {
		return nil, fmt.Errorf("the socket's cipher key is not base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("the socket's cipher key is %d bytes: %w", len(key), err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, zaloGCMNonceBytes)
	if err != nil {
		return nil, fmt.Errorf("build AES-GCM with a %d-byte nonce: %w", zaloGCMNonceBytes, err)
	}

	plain, err := gcm.Open(nil, raw[:zaloGCMNonceBytes], raw[headerBytes:], raw[zaloGCMNonceBytes:headerBytes])
	if err != nil {
		return nil, fmt.Errorf("decrypt the event payload: %w", err)
	}
	return plain, nil
}

// inflateZaloPayload decompresses an event payload. Zalo uses BOTH wrappers
// over deflate — zlib (0x78 …) and gzip (0x1f 0x8b …) — and which one arrives
// is not predictable from the frame. JavaScript never had to care because pako
// sniffs the header; Go's compress/zlib rejects a gzip stream as "invalid
// header", which reads as a decryption failure and sends the reader hunting one
// layer too low.
func inflateZaloPayload(data []byte) ([]byte, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return readAllAndClose(gzip.NewReader(bytes.NewReader(data)))
	}
	return readAllAndClose(zlib.NewReader(bytes.NewReader(data)))
}

// maxInflatedEventBytes bounds what ONE event payload may inflate to, and it is
// a bound on the DECOMPRESSED size because that is the quantity an attacker
// controls cheaply. [maxSocketPayloadBytes] caps the frame on the wire, which
// caps compressed bytes — and deflate is free: a few kilobytes of
// provider-controlled frame expands to gigabytes, so the wire cap gives no
// protection at all against the case that matters.
//
// Derived from the most this unit will ever keep out of one payload, not from
// the frame size:
//
//   - [maxInboundPerDrain] (5000) is the most messages one drain retains, and a
//     single 510:1 reply may legitimately carry the whole queue in one envelope.
//   - A message's JSON is ~900 bytes in the captured traffic, and can reach
//     ~30 KiB at Zalo's own 10000-rune text limit; ~3 KiB apiece is a generous
//     average for a full queue.
//
// 5000 × 3 KiB ≈ 15 MiB, so 16 MiB. That is roughly 7000× the largest drain
// actually measured against a live account (2.3 KB for five messages) and ~250×
// DESIGN §9.6's estimate of a deep backfill reaching 64 KiB, so the headroom is
// real rather than tuned to a test. And refusing a payload past it costs
// nothing: it describes more messages than one drain would have kept anyway, and
// reading does not consume Zalo's queue, so the next tick reads the same
// messages once somebody has looked at why one arrived this large.
const maxInflatedEventBytes = 16 << 20

// readAllAndClose inflates a payload under a hard cap and closes the
// decompressor, reporting whichever step failed. The close matters: it is where
// both readers verify the trailing checksum, so ignoring it accepts a truncated
// stream as a complete one.
func readAllAndClose(r io.ReadCloser, openErr error) ([]byte, error) {
	if openErr != nil {
		return nil, fmt.Errorf("open the compressed event payload: %w", openErr)
	}

	// One byte past the cap, so "exactly at the cap" stays tellable from "past
	// it" — and so nothing beyond that is ever allocated.
	out, readErr := io.ReadAll(io.LimitReader(r, maxInflatedEventBytes+1))
	closeErr := r.Close()

	switch {
	case len(out) > maxInflatedEventBytes:
		// Checked before the read and close errors: stopping short is what the
		// cap DOES, so whatever those two report about a stream cut off mid-way
		// describes this refusal rather than a second problem.
		return nil, fmt.Errorf(
			"an event payload inflated past the %d-byte ceiling this unit will decode, so it is refused undecoded: "+
				"a frame small enough to arrive that expands this far is a compression bomb rather than a busy inbox",
			maxInflatedEventBytes)
	case readErr != nil && closeErr != nil:
		return nil, fmt.Errorf("inflate the event payload: %w (and closing it: %w)", readErr, closeErr)
	case readErr != nil:
		return nil, fmt.Errorf("inflate the event payload: %w", readErr)
	case closeErr != nil:
		return nil, fmt.Errorf("the compressed event payload did not end cleanly, so what inflated may be partial: %w", closeErr)
	}
	return out, nil
}
