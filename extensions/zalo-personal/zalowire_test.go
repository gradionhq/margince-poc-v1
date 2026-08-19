// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The wire format, driven against captured frames. What these tests are for is
// stated once: every constant in zalowire.go was read off a live account and
// none of it is documented, so a change that still almost works is the failure
// mode — a 12-byte nonce, a query-unescape, a zlib reader that meets gzip.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestAnOutboundFrameCarriesItsCommandLittleEndianAndItsBodyAsPlainJSON(t *testing.T) {
	frame := encodeZaloFrame(zaloCmdUserBacklog, zaloSubCmdRequestBacklog, []byte("{}"))

	// 510 = 0x01FE, so little-endian puts 0xFE first. A big-endian writer would
	// send cmd 65025 and the server would answer nothing at all.
	want := []byte{zaloFrameVersion, 0xFE, 0x01, zaloSubCmdRequestBacklog, '{', '}'}
	if string(frame) != string(want) {
		t.Errorf("the frame is % x, want % x", frame, want)
	}
}

func TestAFrameRoundTripsThroughItsOwnHeader(t *testing.T) {
	frame := encodeZaloFrame(zaloCmdPing, zaloSubCmdKeepConnection, []byte(`{"eventId":7}`))

	parsed, err := decodeZaloFrame(frame)
	if err != nil {
		t.Fatalf("decode the frame: %v", err)
	}
	if parsed.version != zaloFrameVersion || parsed.cmd != zaloCmdPing || parsed.subCmd != zaloSubCmdKeepConnection {
		t.Errorf("the header read back as version %d cmd %d sub %d, want %d/%d/%d",
			parsed.version, parsed.cmd, parsed.subCmd, zaloFrameVersion, zaloCmdPing, zaloSubCmdKeepConnection)
	}
	if string(parsed.body) != `{"eventId":7}` {
		t.Errorf("the body read back as %s, want the JSON that went in", parsed.body)
	}
}

func TestASocketMessageTooShortToHoldAHeaderIsRefusedRatherThanRead(t *testing.T) {
	for _, truncated := range [][]byte{nil, {1}, {1, 0xFE, 0x01}} {
		if _, err := decodeZaloFrame(truncated); err == nil {
			t.Errorf("a %d-byte message decoded without complaint, want a refusal", len(truncated))
		}
	}
}

// The one captured ciphertext this suite keeps verbatim, and the reason it is
// kept: it pins the AES-GCM container to what Zalo sends. Its plaintext also
// happens to be the queue list the server advertises, which is where 510_1 and
// 511_1 — the two queues the drain asks for — come from.
func TestTheCapturedCipherKeyFrameDecodesToTheQueuesZaloAdvertises(t *testing.T) {
	var frame struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(capturedQueueManifest), &frame); err != nil {
		t.Fatalf("parse the captured frame: %v", err)
	}
	if frame.Key != capturedCipherKey {
		t.Fatalf("the captured frame carries key %q, want the one this suite decrypts with", frame.Key)
	}

	decoded, err := decodeZaloEvent([]byte(capturedQueueManifest), frame.Key)
	if err != nil {
		t.Fatalf("decode the captured cmd=1 frame: %v", err)
	}

	var manifest struct {
		Data struct {
			QCmds []struct {
				Cmd    uint16 `json:"cmd"`
				SubCmd byte   `json:"subCmd"`
			} `json:"qCmds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(decoded, &manifest); err != nil {
		t.Fatalf("parse the decoded manifest (%s): %v", truncate(string(decoded), 200), err)
	}

	advertised := map[uint16]byte{}
	for _, q := range manifest.Data.QCmds {
		advertised[q.Cmd] = q.SubCmd
	}
	for _, cmd := range []uint16{zaloCmdUserBacklog, zaloCmdGroupBacklog} {
		if advertised[cmd] != zaloSubCmdRequestBacklog {
			t.Errorf("the server advertised no %d:%d queue, so the drain asks for one that does not exist",
				cmd, zaloSubCmdRequestBacklog)
		}
	}
}

// The captured cmd=1 payload's base64 contains a literal `+`. Decoding it
// through url.QueryUnescape would read that `+` as a space and the base64 would
// fail several characters later, pointing at the payload rather than at the
// unescaping — which is the bug this asserts is absent.
func TestABase64PayloadContainingAPlusIsNotUnescapedAsAQueryString(t *testing.T) {
	var frame struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(capturedQueueManifest), &frame); err != nil {
		t.Fatalf("parse the captured frame: %v", err)
	}
	if !strings.Contains(frame.Data, "+") {
		t.Fatal("the captured payload no longer contains a `+`, so this test proves nothing — pick a payload that does")
	}
	if _, err := decodeZaloEvent([]byte(capturedQueueManifest), capturedCipherKey); err != nil {
		t.Fatalf("decode a payload containing a `+`: %v", err)
	}
}

func TestAllFourPayloadEncodingsDecodeToTheSameCapturedMessage(t *testing.T) {
	for _, encrypt := range []int{0, 1, 2, 3} {
		body := sealCapturedEvent(t, capturedInbound, encrypt)

		decoded, err := decodeZaloEvent(body, capturedCipherKey)
		if err != nil {
			t.Errorf("decode an encrypt=%d payload: %v", encrypt, err)
			continue
		}
		if string(decoded) != capturedInbound {
			t.Errorf("encrypt=%d decoded to %s, want the captured envelope", encrypt, truncate(string(decoded), 200))
		}
	}
}

// Zalo uses both deflate wrappers and which one arrives is not predictable from
// the frame. Go's compress/zlib rejects gzip as "invalid header", which reads as
// a decryption failure one layer too low.
func TestAGzipWrappedPayloadInflatesAsWellAsAZlibOne(t *testing.T) {
	zlibbed := deflate(t, []byte(capturedInbound))
	if zlibbed[0] != 0x78 {
		t.Fatalf("the zlib payload starts %#x, want the 0x78 this sniff keys on", zlibbed[0])
	}

	gzipped := gzipBytes(t, []byte(capturedInbound))
	body, err := json.Marshal(map[string]any{"encrypt": 1, "data": base64.StdEncoding.EncodeToString(gzipped)})
	if err != nil {
		t.Fatalf("encode the event body: %v", err)
	}

	decoded, err := decodeZaloEvent(body, capturedCipherKey)
	if err != nil {
		t.Fatalf("decode a gzip-wrapped payload: %v", err)
	}
	if string(decoded) != capturedInbound {
		t.Errorf("the gzip payload decoded to %s, want the captured envelope", truncate(string(decoded), 200))
	}
}

func TestAnEncryptedEventThatArrivesBeforeTheCipherKeyIsRefused(t *testing.T) {
	body := sealCapturedEvent(t, capturedInbound, 2)

	_, err := decodeZaloEvent(body, "")
	if err == nil {
		t.Fatal("an encrypted event decoded with no cipher key, want a refusal")
	}
	if !strings.Contains(err.Error(), "cipher key") {
		t.Errorf("the refusal reads %q, and it has to name the missing key", err)
	}
}

func TestAnEncryptedEventTooShortForItsNonceAndAdditionalDataIsRefused(t *testing.T) {
	short := make([]byte, 2*zaloGCMNonceBytes)
	body, err := json.Marshal(map[string]any{"encrypt": 3, "data": base64.StdEncoding.EncodeToString(short)})
	if err != nil {
		t.Fatalf("encode the event body: %v", err)
	}

	if _, err := decodeZaloEvent(body, capturedCipherKey); err == nil {
		t.Fatal("a payload with a header and no ciphertext decoded, want a refusal")
	}
}

func TestACipherKeyThatCannotOpenThePayloadIsReportedRatherThanReturningGarbage(t *testing.T) {
	body := sealCapturedEvent(t, capturedInbound, 2)

	// A key of the right length that is simply the wrong key: GCM authenticates,
	// so this must fail rather than produce plausible bytes.
	_, err := decodeZaloEvent(body, base64.StdEncoding.EncodeToString(make([]byte, 16)))
	if err == nil {
		t.Fatal("the payload opened under the wrong key, want a refusal")
	}
	if !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("the refusal reads %q, and it has to say the decryption failed", err)
	}
}

func TestAnEventBodyThatIsNotJSONNamesWhatArrivedInstead(t *testing.T) {
	_, err := decodeZaloEvent([]byte("<html>a challenge page</html>"), capturedCipherKey)
	if err == nil {
		t.Fatal("a non-JSON body decoded, want a refusal")
	}
	if !strings.Contains(err.Error(), "html") {
		t.Errorf("the refusal reads %q, and it has to quote what arrived instead", err)
	}
}

func TestAPayloadThatIsNotURLEscapedAsExpectedIsReportedRatherThanDecoded(t *testing.T) {
	// A stray `%` with nothing decodable behind it: the server never sends this,
	// and reading it as literal bytes would hand base64 a corrupted string and
	// blame the payload.
	body := []byte(`{"encrypt":2,"data":"abc%zz"}`)

	_, err := decodeZaloEvent(body, capturedCipherKey)
	if err == nil {
		t.Fatal("a payload that is not URL-escaped was decoded anyway")
	}
	if !strings.Contains(err.Error(), "URL-escaped") {
		t.Errorf("the refusal reads %q, and it has to name the unescaping that failed", err)
	}
}

func TestAPayloadThatIsNotBase64IsReportedAtTheLayerThatFailed(t *testing.T) {
	body := []byte(`{"encrypt":1,"data":"not base64 at all!!"}`)

	_, err := decodeZaloEvent(body, capturedCipherKey)
	if err == nil {
		t.Fatal("a non-base64 payload was decoded anyway")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Errorf("the refusal reads %q, and it has to name base64 rather than the layer under it", err)
	}
}

// A payload whose compressed stream stops early inflates to something that looks
// like a shorter message. The checksum is only verified on Close, so a reader
// that ignores it accepts the truncation as a complete payload.
func TestATruncatedCompressedPayloadIsRefusedRatherThanReadAsShorter(t *testing.T) {
	full := deflate(t, []byte(capturedInbound))
	body, err := json.Marshal(map[string]any{
		"encrypt": 1,
		"data":    base64.StdEncoding.EncodeToString(full[:len(full)-6]),
	})
	if err != nil {
		t.Fatalf("encode the event body: %v", err)
	}

	if _, err := decodeZaloEvent(body, capturedCipherKey); err == nil {
		t.Fatal("a truncated compressed payload was accepted as a complete one")
	}
}

// AN ATTACK, not arithmetic. The frame is capped on the wire, but that caps
// COMPRESSED bytes and deflate is free: the payload below is a few kilobytes on
// the wire and describes gigabytes. A decode that read to EOF would let the
// provider choose this process's memory, and a test that merely checked a large
// VALID payload still works would pass with that bug present.
func TestACompressionBombIsRefusedRatherThanInflatedIntoThisProcess(t *testing.T) {
	for name, payload := range map[string][]byte{
		"zlib": deflate(t, bytes.Repeat([]byte{'a'}, 4*maxInflatedEventBytes)),
		"gzip": gzipBytes(t, bytes.Repeat([]byte{'a'}, 4*maxInflatedEventBytes)),
	} {
		t.Run(name, func(t *testing.T) {
			// The point of the fixture: it costs the attacker almost nothing to
			// send, so the wire cap never sees it coming.
			if len(payload) > 1<<20 {
				t.Fatalf("the %s bomb is %d bytes on the wire, which is too large to prove the point", name, len(payload))
			}

			body, err := json.Marshal(map[string]any{
				"encrypt": 1,
				"data":    base64.StdEncoding.EncodeToString(payload),
			})
			if err != nil {
				t.Fatalf("encode the event body: %v", err)
			}

			decoded, err := decodeZaloEvent(body, capturedCipherKey)
			if err == nil {
				t.Fatalf("a %d-byte frame inflated to %d bytes and was accepted, which lets the provider choose this process's memory",
					len(payload), len(decoded))
			}
			if !strings.Contains(err.Error(), "compression bomb") {
				t.Errorf("the refusal reads %q, and it has to name the ceiling it refused against", err)
			}
		})
	}
}

// The ceiling has to leave a real drain alone, or it is a self-inflicted outage
// rather than a bound. This is the other half of the bomb test and neither
// stands without the other.
func TestAPayloadTheSizeOfAFullDrainStillDecodes(t *testing.T) {
	// One message repeated to the drain's own ceiling — the largest envelope a
	// legitimate 510:1 reply could carry and this unit would still keep.
	message := onlyMessage(t, capturedInbound)
	batch := make([]json.RawMessage, maxInboundPerDrain)
	for i := range batch {
		batch[i] = message
	}
	envelope := envelopeAround(t, batch...)
	if len(envelope) >= maxInflatedEventBytes {
		t.Fatalf("a full drain's envelope is %d bytes, which is at or past the %d-byte ceiling — the ceiling is too low",
			len(envelope), maxInflatedEventBytes)
	}

	decoded, err := decodeZaloEvent(sealCapturedEvent(t, envelope, 2), capturedCipherKey)
	if err != nil {
		t.Fatalf("decode a full drain's worth of messages: %v", err)
	}
	if len(decoded) != len(envelope) {
		t.Errorf("the payload decoded to %d bytes, want the %d that went in", len(decoded), len(envelope))
	}
}

// runawayStream is what a compression bomb IS once it is inflating: an unbounded
// stream out of a bounded frame. It stops at a multiple of the ceiling and
// reports that nothing stopped it, so a decode with no cap fails this test in
// milliseconds instead of hanging.
type runawayStream struct{ produced int64 }

const runawayStreamLimit = 8 * maxInflatedEventBytes

var errNothingStoppedTheRead zaloError = "the read was never capped: this stream would have run forever"

func (s *runawayStream) Read(p []byte) (int, error) {
	if s.produced >= runawayStreamLimit {
		return 0, errNothingStoppedTheRead
	}
	for i := range p {
		p[i] = 'a'
	}
	s.produced += int64(len(p))
	return len(p), nil
}

func (s *runawayStream) Close() error { return nil }

// THE BOUND ITSELF, which the bomb test above cannot prove: a decode that read
// to EOF and only THEN noticed the size would still refuse, having already
// materialised whatever the provider chose. So this asserts on what was read out
// of the stream, not on the verdict alone.
func TestTheInflateCeilingStopsTheREADRatherThanJudgingTheResult(t *testing.T) {
	stream := &runawayStream{}

	_, err := readAllAndClose(stream, nil)
	if err == nil {
		t.Fatal("an unbounded stream inflated to completion, which cannot happen")
	}
	if !strings.Contains(err.Error(), "compression bomb") {
		t.Errorf("the refusal reads %q, and it has to be the ceiling rather than the stream giving up", err)
	}

	// The read stopped AT the ceiling. One byte over is the deliberate margin
	// that makes "exactly at the ceiling" tellable from "past it"; the buffer
	// ReadAll hands down may be larger, hence the one-buffer slack.
	if stream.produced > maxInflatedEventBytes+1+bytes.MinRead {
		t.Errorf("the decode read %d bytes out of the stream with a %d-byte ceiling: the ceiling is judging the result rather than bounding the read, so the provider still chose this process's memory",
			stream.produced, maxInflatedEventBytes)
	}
}
