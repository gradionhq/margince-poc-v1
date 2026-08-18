// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The Zalo Web crypto layer, ported from zca-js (src/utils.ts).
//
// Two entirely separate AES schemes live here and mixing them up is the most
// likely way this file breaks:
//
//   - The LOGIN scheme (paramsEncryptor, encodeAESHex): the key is a 32-char
//     ASCII string used as raw AES-256 key bytes, output hex or base64. It
//     protects the getLoginInfo call, before any server-issued key exists.
//   - The SESSION scheme (encodeAES/decodeAES): the key is the server-issued
//     `zpw_enk`, which is BASE64 and must be decoded to bytes first. Every
//     ordinary API call and response uses it.
//
// Both use CBC with an all-zero 16-byte IV and PKCS#7. One IV for every message
// is Zalo Web's own design, not a transcription slip and not a defect to fix
// here: randomising it would make every request unreadable to the server.

// MD5 appears three times below and is waived three times, so the reasoning
// lives here once: it is NOT a cryptographic choice this code made and is not
// open to review as one. It is the transcription of somebody else's wire
// format. Zalo Web signs every request with MD5("zsecure" + callType + the
// params' values) and derives the device IMEI as MD5(userAgent); the authority
// is zca-js's src/utils.ts and PROTOCOL.md §"Layer 2"/§"Layer 3", not a
// judgement about hash strength. "Upgrading" it to SHA-256 changes the bytes
// the server verifies, so every request is refused — with an opaque numeric
// error code that names nothing, which is a day gone before anyone suspects
// the hash. The confidentiality of this layer rests on the session key and on
// TLS, neither of which is MD5.
import (
	"crypto/aes"
	"crypto/cipher"
	//nolint:gosec // G501: MD5 is Zalo Web's wire format (request signature and IMEI), transcribed from zca-js src/utils.ts — see the note above this import; changing the hash makes the server refuse every request.
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
)

// zcidKey is the fixed key Zalo Web ships in its own bundle to encrypt the
// zcid. It is a constant in their JavaScript, not a secret of ours.
const zcidKey = "3FC4F0D2AB50057BCE0D90D9187A22B1"

// zeroIV is the initialization vector every Zalo Web message uses. See the file
// comment: the all-zero IV is the protocol, not a defect.
//
// It is a function rather than a package-level var because a var initializer
// that CALLS anything runs at import — before this unit's declaration has been
// validated and before anything has decided the unit may run at all.
func zeroIV() []byte { return make([]byte, aes.BlockSize) }

func pkcs7Pad(data []byte) ([]byte, error) {
	n := aes.BlockSize - len(data)%aes.BlockSize
	if n < 1 || n > math.MaxUint8 {
		return nil, fmt.Errorf("pad length %d is not a byte, so this is not PKCS#7", n)
	}
	out := make([]byte, len(data)+n)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out, nil
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext is not a whole number of blocks (%d bytes)", len(data))
	}
	n := int(data[len(data)-1])
	if n == 0 || n > aes.BlockSize || n > len(data) {
		return nil, fmt.Errorf("bad PKCS#7 padding byte %d", n)
	}
	return data[:len(data)-n], nil
}

func aesCBCEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes key (%d bytes): %w", len(key), err)
	}
	padded, err := pkcs7Pad(plaintext)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, zeroIV()).CryptBlocks(out, padded)
	return out, nil
}

func aesCBCDecrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes key (%d bytes): %w", len(key), err)
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext is not a whole number of blocks (%d bytes)", len(ciphertext))
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, zeroIV()).CryptBlocks(out, ciphertext)
	return pkcs7Unpad(out)
}

// encodeAESHex is the login-scheme encrypt: an ASCII key used as raw key bytes,
// rendered as UPPERCASE hex because that is the only case the zcid is ever
// minted in, and the derivation downstream reads the characters themselves.
func encodeAESHex(key, message string) (string, error) {
	ct, err := aesCBCEncrypt([]byte(key), []byte(message))
	if err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(ct)), nil
}

// encodeAESBase64 is the same login scheme with base64 output — what carries
// the getLoginInfo request body.
func encodeAESBase64(key, message string) (string, error) {
	ct, err := aesCBCEncrypt([]byte(key), []byte(message))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decryptLoginResp decrypts the getLoginInfo response under the ephemeral
// login key. Same ASCII-key scheme as the request.
func decryptLoginResp(key, data string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("login response is not base64: %w", err)
	}
	return aesCBCDecrypt([]byte(key), raw)
}

// encodeAES is the session-scheme encrypt under the server-issued zpw_enk,
// which is base64 and is the key only after decoding.
func encodeAES(secretKey, data string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(secretKey)
	if err != nil {
		return "", fmt.Errorf("session key is not base64: %w", err)
	}
	ct, err := aesCBCEncrypt(key, []byte(data))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decodeAES is the session-scheme decrypt of an API response body.
func decodeAES(secretKey, data string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(secretKey)
	if err != nil {
		return nil, fmt.Errorf("session key is not base64: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("response payload is not base64: %w", err)
	}
	return aesCBCDecrypt(key, raw)
}

// getSignKey reproduces Zalo's request signature: the literal "zsecure", the
// call type, then every parameter VALUE in sorted-key order, MD5'd. The keys
// themselves are not in the digest — only their values, which is why a
// parameter whose value stringifies differently than JavaScript would (a float
// where JS had an int, say) fails as an opaque signature rejection.
func getSignKey(callType string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("zsecure")
	b.WriteString(callType)
	for _, k := range keys {
		b.WriteString(params[k])
	}
	//nolint:gosec // G401: the signature Zalo verifies IS an MD5 digest (PROTOCOL.md §"Layer 3"); a stronger hash here is a signature the server rejects, not a stronger signature.
	return fmt.Sprintf("%x", md5.Sum([]byte(b.String())))
}

// paramsEncryptor mints the one-request-lived key that protects the login
// call. The key is DERIVED from two values the server also receives (zcid and
// zcid_ext), so it is obfuscation rather than secrecy — worth stating plainly,
// because it looks like key agreement and is not.
type paramsEncryptor struct {
	zcid       string
	zcidExt    string
	encVer     string
	encryptKey string
}

func newParamsEncryptor(apiType int, imei string, firstLaunchTime int64) (*paramsEncryptor, error) {
	p := &paramsEncryptor{encVer: "v2"}

	zcid, err := encodeAESHex(zcidKey, fmt.Sprintf("%d,%s,%d", apiType, imei, firstLaunchTime))
	if err != nil {
		return nil, fmt.Errorf("mint zcid: %w", err)
	}
	p.zcid = zcid

	ext, err := randomHexString()
	if err != nil {
		return nil, err
	}
	p.zcidExt = ext

	if err := p.deriveEncryptKey(); err != nil {
		return nil, err
	}
	return p, nil
}

// deriveEncryptKey builds the 32-char key by interleaving the two zcid values:
// 8 even-index chars of MD5(zcid_ext), then 12 even-index chars of zcid, then
// 12 odd-index chars of zcid reversed. Arbitrary, and load-bearing — the
// server derives the same string from the same two values.
func (p *paramsEncryptor) deriveEncryptKey() error {
	//nolint:gosec // G401: the server derives this same key from the same zcid_ext with the same MD5 (zca-js src/utils.ts); the digest is a shared derivation step, and it is obfuscation rather than secrecy either way — see the type comment above.
	digest := strings.ToUpper(fmt.Sprintf("%x", md5.Sum([]byte(p.zcidExt))))

	digestEven, _ := splitEvenOdd(digest)
	zcidEven, zcidOdd := splitEvenOdd(p.zcid)

	if len(digestEven) < 8 || len(zcidEven) < 12 || len(zcidOdd) < 12 {
		return fmt.Errorf("cannot derive encrypt key: zcid too short (%d chars)", len(p.zcid))
	}

	reversed := make([]rune, 0, len(zcidOdd))
	for i := len(zcidOdd) - 1; i >= 0; i-- {
		reversed = append(reversed, zcidOdd[i])
	}

	p.encryptKey = string(digestEven[:8]) + string(zcidEven[:12]) + string(reversed[:12])
	return nil
}

func splitEvenOdd(s string) (even, odd []rune) {
	for i, r := range []rune(s) {
		if i%2 == 0 {
			even = append(even, r)
		} else {
			odd = append(odd, r)
		}
	}
	return even, odd
}

func randomHexString() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
