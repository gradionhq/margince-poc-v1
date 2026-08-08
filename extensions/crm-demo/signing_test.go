// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmdemo

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func TestStoreSigningKeySealsTheTrimmedKey(t *testing.T) {
	rt := newRuntime()
	out, err := storeSigningKey(context.Background(), rt, json.RawMessage(`{"key":"  s3cr3t  "}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"stored":true}` {
		t.Errorf("result = %s", out)
	}
	// Trimmed: a key with surrounding whitespace signs perfectly well and is
	// impossible for whoever pasted it to reproduce on the verifying side.
	if got := string(rt.secrets.stored[signingKeyName]); got != "s3cr3t" {
		t.Errorf("the sealed key is %q, want the trimmed value", got)
	}
}

func TestStoreSigningKeyReplacesRatherThanAccumulates(t *testing.T) {
	rt := newRuntime()
	for _, key := range []string{"first", "second"} {
		if _, err := storeSigningKey(context.Background(), rt, json.RawMessage(`{"key":"`+key+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if got := string(rt.secrets.stored[signingKeyName]); got != "second" {
		t.Errorf("after a rotation the namespace holds %q — a Put replaces, and the superseded material is destroyed", got)
	}
}

func TestStoreSigningKeyRefusesWhatItCannotSealHonestly(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"an empty key", `{"key":""}`, "signing key is empty"},
		{"a key of whitespace", `{"key":"\t "}`, "signing key is empty"},
		{"an over-long key", `{"key":"` + strings.Repeat("k", maxSigningKey+1) + `"}`, "at most 4096 bytes"},
		{"an unknown field", `{"secret":"x"}`, "not the declared shape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRuntime()
			_, err := storeSigningKey(context.Background(), rt, json.RawMessage(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
			if len(rt.secrets.stored) != 0 {
				t.Error("the refusal still sealed something")
			}
		})
	}
}

func TestStoreSigningKeyPropagatesACustodianFailure(t *testing.T) {
	rt := newRuntime()
	rt.secrets.err = errors.New("extsecrets: no keyvault is configured")
	if _, err := storeSigningKey(context.Background(), rt, json.RawMessage(`{"key":"k"}`)); err == nil {
		t.Fatal("a deployment with no custodian reported the key stored")
	}
}

func TestSigningKeyStatusReportsPresenceAndNothingElse(t *testing.T) {
	rt := newRuntime()
	absent, err := signingKeyStatus(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil || string(absent) != `{"stored":false}` {
		t.Fatalf("with no key stored: %s, %v", absent, err)
	}

	rt.secrets.stored[signingKeyName] = []byte("s3cr3t")
	present, err := signingKeyStatus(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(present) != `{"stored":true}` {
		t.Fatalf("with a key stored: %s", present)
	}
	// The load-bearing assertion of the whole secrets surface: no operation
	// returns the material, or any part of it, masked or otherwise. Asserted by
	// VARYING the key rather than by grepping for one value: a length, a
	// prefix or a truncated hash would each pass a substring check while still
	// being a fact about the key, and each of them would move the result.
	rt.secrets.stored[signingKeyName] = []byte("a-much-longer-and-entirely-different-key")
	other, err := signingKeyStatus(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(other) != string(present) {
		t.Errorf("the status result varies with the key material: %s versus %s", present, other)
	}
}

func TestSigningKeyStatusPropagatesACustodianFailure(t *testing.T) {
	rt := newRuntime()
	rt.secrets.err = errors.New("vault unreachable")
	// A custodian that is DOWN must not read as "no key stored": an operator
	// would then be told to paste a key they already pasted.
	if _, err := signingKeyStatus(context.Background(), rt, json.RawMessage(`{}`)); err == nil {
		t.Fatal("an unreachable custodian answered stored:false")
	}
}

func TestSignPayloadProvesTheKeyIsUsedWithoutEmittingIt(t *testing.T) {
	rt := newRuntime()
	rt.secrets.stored[signingKeyName] = []byte("s3cr3t")

	out, err := signPayload(context.Background(), rt, json.RawMessage(`{"payload":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Algorithm string `json:"algorithm"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	// Recomputed here rather than pinned as a hex constant: a pinned digest
	// would pass for a handler that hashed the payload alone, or hashed the key
	// alone, as long as somebody once copied the output back into the test.
	mac := hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write([]byte("hello"))
	if want := hex.EncodeToString(mac.Sum(nil)); got.Signature != want {
		t.Errorf("signature = %s, want the HMAC-SHA256 of the payload under the stored key", got.Signature)
	}
	if got.Algorithm != signatureAlgorithm {
		t.Errorf("algorithm = %q — a verifier must not have to infer the construction from the digest length", got.Algorithm)
	}
	if strings.Contains(string(out), "s3cr3t") {
		t.Error("the result carries the key material")
	}
}

func TestSignPayloadDependsOnTheKey(t *testing.T) {
	// Two keys, one payload: a handler that ignored the key and hashed the
	// payload would answer the same twice, and every assertion above would
	// still hold.
	signatures := map[string]bool{}
	for _, key := range []string{"first", "second"} {
		rt := newRuntime()
		rt.secrets.stored[signingKeyName] = []byte(key)
		out, err := signPayload(context.Background(), rt, json.RawMessage(`{"payload":"hello"}`))
		if err != nil {
			t.Fatal(err)
		}
		signatures[string(out)] = true
	}
	if len(signatures) != 2 {
		t.Error("the signature does not change with the key — the stored credential is not being used")
	}
}

func TestSignPayloadNamesTheMissingKey(t *testing.T) {
	_, err := signPayload(context.Background(), newRuntime(), json.RawMessage(`{"payload":"hello"}`))
	if !errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("err = %v, want the published sentinel wrapped, so a caller can tell 'paste a key' from 'the vault is down'", err)
	}
}

func TestSignPayloadRefusesWhatItCannotSign(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"an empty payload", `{"payload":""}`, "no payload to sign"},
		{"an over-long payload", `{"payload":"` + strings.Repeat("p", maxPayload+1) + `"}`, "at most 4096 bytes"},
		{"an unknown field", `{"body":"x"}`, "not the declared shape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRuntime()
			rt.secrets.stored[signingKeyName] = []byte("s3cr3t")
			if _, err := signPayload(context.Background(), rt, json.RawMessage(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestSignPayloadSignsWhatWasSent(t *testing.T) {
	// NOT trimmed, unlike the key and the note body: a payload's bytes are the
	// thing being signed, and a signature over a value the caller did not send
	// verifies against nothing on the other side.
	rt := newRuntime()
	rt.secrets.stored[signingKeyName] = []byte("s3cr3t")
	padded, err := signPayload(context.Background(), rt, json.RawMessage(`{"payload":" hello "}`))
	if err != nil {
		t.Fatal(err)
	}
	bare, err := signPayload(context.Background(), rt, json.RawMessage(`{"payload":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(padded) == string(bare) {
		t.Error("the payload was trimmed before signing")
	}
}

func TestSignPayloadPropagatesACustodianFailure(t *testing.T) {
	rt := newRuntime()
	rt.secrets.err = errors.New("vault unreachable")
	_, err := signPayload(context.Background(), rt, json.RawMessage(`{"payload":"hello"}`))
	if err == nil || errors.Is(err, extension.ErrSecretNotFound) {
		t.Fatalf("err = %v — an unreachable custodian is not the same as an absent key", err)
	}
}
