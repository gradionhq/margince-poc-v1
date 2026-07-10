// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package keyvault

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The local provider's AES-GCM layer is unit-tested here without a database;
// the Put/Get/Delete round-trip against real Postgres is the integration
// lane's job. These cover the security-critical crypto: round-trip, that a
// wrong root key fails cleanly without leaking the plaintext, and that the
// AAD binds the ciphertext to its exact ref so a swapped or cross-workspace
// ref cannot open it.

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	return k
}

func TestSealOpen_roundTrip(t *testing.T) {
	aead, err := newAEAD(testKey(t))
	if err != nil {
		t.Fatalf("newAEAD: %v", err)
	}
	ref, err := mintRef(ids.New[ids.WorkspaceKind]())
	if err != nil {
		t.Fatalf("mintRef: %v", err)
	}
	secret := []byte("the-imap-credential-bundle")

	sealed, err := seal(aead, []byte(ref), secret)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("sealed bytes contain the plaintext — encryption did not happen")
	}

	got, err := open(aead, []byte(ref), sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("open returned %q, want %q", got, secret)
	}
}

func TestOpen_wrongKeyFailsWithoutLeakingPlaintext(t *testing.T) {
	good, err := newAEAD(testKey(t))
	if err != nil {
		t.Fatalf("newAEAD good: %v", err)
	}
	wrong, err := newAEAD(testKey(t))
	if err != nil {
		t.Fatalf("newAEAD wrong: %v", err)
	}
	ref, err := mintRef(ids.New[ids.WorkspaceKind]())
	if err != nil {
		t.Fatalf("mintRef: %v", err)
	}
	secret := []byte("plaintext-must-not-leak-abcdef")

	sealed, err := seal(good, []byte(ref), secret)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	_, err = open(wrong, []byte(ref), sealed)
	if err == nil {
		t.Fatal("open with the wrong key must fail")
	}
	if strings.Contains(err.Error(), string(secret)) || strings.Contains(err.Error(), "abcdef") {
		t.Fatalf("decrypt error leaks the plaintext: %v", err)
	}
}

// The AAD binds the ciphertext to its exact ref (workspace + version +
// token). Opening with a different ref's AAD must fail — this is the crypto
// half of workspace isolation and defeats ciphertext substitution.
func TestOpen_differentRefAADFails(t *testing.T) {
	aead, err := newAEAD(testKey(t))
	if err != nil {
		t.Fatalf("newAEAD: %v", err)
	}
	refA, err := mintRef(ids.New[ids.WorkspaceKind]())
	if err != nil {
		t.Fatalf("mintRef A: %v", err)
	}
	refB, err := mintRef(ids.New[ids.WorkspaceKind]())
	if err != nil {
		t.Fatalf("mintRef B: %v", err)
	}
	sealed, err := seal(aead, []byte(refA), []byte("secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := open(aead, []byte(refB), sealed); err == nil {
		t.Fatal("open with a different ref's AAD must fail")
	}
}

func TestNewAEAD_rejectsShortKey(t *testing.T) {
	if _, err := newAEAD(make([]byte, 16)); err == nil {
		t.Fatal("newAEAD must reject a 16-byte key — AES-256 needs 32 bytes")
	}
	if _, err := newAEAD(nil); err == nil {
		t.Fatal("newAEAD must reject a nil key")
	}
}

// A short key error must name the length requirement, never the key bytes.
func TestNewAEAD_errorDoesNotLeakKey(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes — too short
	_, err := newAEAD(key)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), string(key)) {
		t.Fatalf("newAEAD error leaks the key material: %v", err)
	}
}

func TestFromEnv_unsetIsNotConfigured(t *testing.T) {
	t.Setenv(envRootKey, "")
	v, configured, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv unset: %v", err)
	}
	if configured || v != nil {
		t.Fatalf("FromEnv with no root key must report not-configured, got configured=%v vault=%v", configured, v)
	}
}

func TestFromEnv_shortKeyIsAnError(t *testing.T) {
	// base64 of 16 bytes — decodes fine but is too short for AES-256.
	t.Setenv(envRootKey, "MDEyMzQ1Njc4OWFiY2RlZg==")
	if _, _, err := FromEnv(nil); err == nil {
		t.Fatal("FromEnv with a 16-byte key must error, not silently accept it")
	}
}

func TestFromEnv_nonBase64IsAnError(t *testing.T) {
	t.Setenv(envRootKey, "not valid base64!!!")
	if _, _, err := FromEnv(nil); err == nil {
		t.Fatal("FromEnv with a non-base64 key must error")
	}
}
