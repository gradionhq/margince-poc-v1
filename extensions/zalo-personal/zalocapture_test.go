// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// FRAMES CAPTURED FROM A LIVE ZALO ACCOUNT, 2026-08-17, and the reason this
// suite uses them rather than frames it wrote itself: a hand-built frame proves
// the decoder agrees with whoever wrote the test, and only a real one proves it
// agrees with Zalo. Every field name, JSON type and sentinel below is verbatim
// — `msgId` and `ts` quoted as strings, `uidFrom: "0"` for the self-echo,
// `content` a bare string for msgType `webchat`, the envelope's `data.msgs[]`.
//
// TWO deliberate departures from the capture, both because this repository is
// public:
//
//  1. The counterparty's Zalo account id and display name, and the member's own
//     display name, are replaced with placeholders. Nothing structural changes —
//     the ids keep their 19-digit shape and the name keeps its \u escapes, which
//     is what the decoding is tested on.
//  2. The message frames are stored as the PLAINTEXT the capture decrypted to,
//     and re-sealed in-test. Their real ciphertext would have been a real
//     conversation.
//
// The one ciphertext kept verbatim is [capturedQueueManifest], the cmd=1 frame,
// because its plaintext is protocol metadata and nothing else — the queue list
// the server advertises, including the `510_1` and `511_1` this connector asks
// for. It is what pins the AES-GCM container (16-byte nonce, additional data at
// [16:32], ciphertext from [32:]) to what Zalo actually sends rather than to
// what this package believes.

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
)

// capturedCipherKey is the payload key the captured socket handed over on its
// cmd=1 frame. It is AES-128 — 16 bytes, not the 32 a reader might assume — and
// it protects nothing today: the session it belonged to ended on 2026-08-17, and
// the one frame it still opens carries the server's queue list.
const capturedCipherKey = "zQOJc+UV7TfEzVcFVa/cCQ=="

// capturedQueueManifest is the real cmd=1 frame body, ciphertext and all.
const capturedQueueManifest = `{"key":"zQOJc+UV7TfEzVcFVa/cCQ==","encrypt":2,"data":"O0pTEfY8tMNMrY1S+RLq1OpZbfQ7cDXp4sisQmGweb5oRcVYd/5czgtrJmEFmpnX51TX1qj6+4Idnhuj52f+OVEIuRsbAcSpmTSRyFOnZOMHhAHVhCjvqMSyztvz+KIGbm+DALEcl6Kkhrr1IIJS6KD9swrv9TSwp6dxgGG0Bovg1omB0cSuY123HjBGyL0XqWsrQ7bNDo6ZtqxHXjflBVak9rPhr0v3FBnzGVamlY1tEdDTEEqxNrzUi3qAAQJFgrjzdeqh2Q3O4nnEOzDCmHY9LQSwWefOJ+szG5wuzSD90PwvZaNlyI7AvZbPUm4=","error_code":0,"error_message":""}`

// capturedSelfEcho is the member's OWN message coming back — `uidFrom: "0"`,
// the counterparty in `idTo`, and the same msgId the send returned. This is the
// frame the unit layer's direction filter exists for.
const capturedSelfEcho = `{"error_code":0,"error_message":"","data":{"lastActionId":"13829514534617","more":0,"msgs":[{"actionId":"13829514534617","msgId":"8161097159889","cliMsgId":"1786940447877","msgType":"webchat","uidFrom":"0","idTo":"1900000000000000001","dName":"Người Bán","ts":"1786940448331","status":1,"content":"test A","notify":"1","ttl":0,"userId":"0","uin":"0","topOut":"0","topOutTimeOut":"0","topOutImprTimeOut":"0","propertyExt":{"color":0,"size":0,"type":0,"subType":0,"ext":"{\"shouldParseLinkOrContact\":0}"},"paramsExt":{"countUnread":1,"containType":0,"platformType":1},"cmd":501,"st":3,"at":5,"realMsgId":"0"}],"groupMsgs":[],"pageMsgs":[],"clearUnreads":[],"delivereds":[],"seens":[],"groupSeens":[],"queueStatus":{"510_0":{"ids":["13829514534617"],"lastId":"13829514534617"},"510_1":{"ids":["8161097159889"],"lastId":"8161097159889"}},"eesession":[]}}`

// capturedInbound is a message somebody sent the member: the counterparty in
// `uidFrom`, `idTo: "0"`.
const capturedInbound = `{"error_code":0,"error_message":"","data":{"lastActionId":"13829516715153","more":0,"msgs":[{"actionId":"13829516715153","msgId":"8161098001435","cliMsgId":"1786940459593","msgType":"webchat","uidFrom":"1900000000000000001","idTo":"0","dName":"Nguyễn Văn Mẫu","ts":"1786940459649","status":1,"content":"Test B","notify":"1","ttl":0,"userId":"0","uin":"0","topOut":"0","topOutTimeOut":"0","topOutImprTimeOut":"0","propertyExt":{"color":-1,"size":-1,"type":1,"subType":0,"ext":"{\"emoji\":{\"content\":0},\"shouldParseLinkOrContact\":1}"},"paramsExt":{"countUnread":1,"containType":0,"platformType":0},"cmd":501,"st":3,"at":0,"realMsgId":"0"}],"groupMsgs":[],"pageMsgs":[],"clearUnreads":[],"delivereds":[],"seens":[],"groupSeens":[],"queueStatus":{"510_0":{"ids":["13829516715153"],"lastId":"13829516715153"},"510_1":{"ids":["8161098001435"],"lastId":"8161098001435"}},"eesession":[]}}`

// sealCapturedEvent renders a plaintext event body the way the server would at
// the given `encrypt` level, so one captured payload exercises all four
// encodings. Levels 2 and 3 seal under [capturedCipherKey] with the same layout
// the captured cmd=1 frame proves.
func sealCapturedEvent(t *testing.T, plaintext string, encrypt int) []byte {
	t.Helper()

	payload := []byte(plaintext)
	switch encrypt {
	case 0:
		return eventBody(t, encrypt, plaintext)
	case 1:
		return eventBody(t, encrypt, base64.StdEncoding.EncodeToString(deflate(t, payload)))
	case 2:
		payload = deflate(t, payload)
	case 3:
	default:
		t.Fatalf("there is no encrypt=%d on this wire", encrypt)
	}

	sealed := sealGCM(t, payload)
	// The server escapes the base64 before sending it, which is what makes
	// PathUnescape-not-QueryUnescape load-bearing.
	return eventBody(t, encrypt, url.PathEscape(base64.StdEncoding.EncodeToString(sealed)))
}

func eventBody(t *testing.T, encrypt int, data string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"encrypt": encrypt, "data": data})
	if err != nil {
		t.Fatalf("encode the event body: %v", err)
	}
	return body
}

// sealGCM lays out nonce ++ additionalData ++ ciphertext exactly as the captured
// frame does.
func sealGCM(t *testing.T, plaintext []byte) []byte {
	t.Helper()

	key, err := base64.StdEncoding.DecodeString(capturedCipherKey)
	if err != nil {
		t.Fatalf("decode the captured cipher key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("build the cipher: %v", err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, zaloGCMNonceBytes)
	if err != nil {
		t.Fatalf("build GCM: %v", err)
	}

	header := make([]byte, 2*zaloGCMNonceBytes)
	if _, err := rand.Read(header); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	nonce, additional := header[:zaloGCMNonceBytes], header[zaloGCMNonceBytes:]
	return append(header, gcm.Seal(nil, nonce, plaintext, additional)...)
}

func deflate(t *testing.T, plaintext []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	if _, err := w.Write(plaintext); err != nil {
		t.Fatalf("compress the payload: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the compressor: %v", err)
	}
	return out.Bytes()
}

// capturedFrame wraps a body in the 4-byte header, which is how it arrived.
func capturedFrame(cmd uint16, subCmd byte, body []byte) []byte {
	frame := make([]byte, zaloFrameHeaderBytes+len(body))
	frame[0] = zaloFrameVersion
	frame[1] = byte(cmd)
	frame[2] = byte(cmd >> 8)
	frame[3] = subCmd
	copy(frame[zaloFrameHeaderBytes:], body)
	return frame
}

// onlyMessage reads the one message out of a captured envelope, so a test that
// is about mapping does not restate the envelope.
func onlyMessage(t *testing.T, envelope string) json.RawMessage {
	t.Helper()
	var parsed struct {
		Data struct {
			Msgs []json.RawMessage `json:"msgs"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(envelope), &parsed); err != nil {
		t.Fatalf("parse the captured envelope: %v", err)
	}
	if len(parsed.Data.Msgs) != 1 {
		t.Fatalf("the captured envelope holds %d messages, want exactly 1", len(parsed.Data.Msgs))
	}
	return parsed.Data.Msgs[0]
}

// withField replaces one field of a captured message, so a test about a single
// malformed value keeps every other field real.
func withField(t *testing.T, message json.RawMessage, field string, value any) json.RawMessage { //craft:ignore naked-any the field being replaced is `content`, which genuinely is any JSON value on Zalo's wire — a concrete type here would make the fixture assert a shape the provider does not promise
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(message, &fields); err != nil {
		t.Fatalf("parse the captured message: %v", err)
	}
	fields[field] = value
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("re-encode the captured message: %v", err)
	}
	return out
}

// envelopeAround wraps messages in the event envelope the socket delivers.
func envelopeAround(t *testing.T, msgs ...json.RawMessage) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"data": map[string]any{"msgs": msgs}})
	if err != nil {
		t.Fatalf("encode the event envelope: %v", err)
	}
	return string(body)
}

// gzipBytes wraps a payload in the OTHER deflate container Zalo uses.
func gzipBytes(t *testing.T, plaintext []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(plaintext); err != nil {
		t.Fatalf("compress the payload: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the compressor: %v", err)
	}
	return out.Bytes()
}
