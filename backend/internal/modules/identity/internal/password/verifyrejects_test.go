// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package password

import (
	"strings"
	"testing"
)

// Every way a stored hash can be malformed, and the refusal each one earns.
//
// These branches decide what happens when the string in app_user.password_hash
// is not something Hash wrote — a truncated column, a hand-edited row, a value
// from a tool that formats PHC differently. The failure mode they exist to
// prevent is not a crash: it is Verify reaching argon2.IDKey with garbage it
// half-parsed and comparing against a key length it inferred from that garbage.
//
// The last case is the one with teeth. An oversized key length used to be
// masked with &0xFFFF before the int->uint32 conversion, which SILENTLY
// TRUNCATED it — so a hash claiming a 65536+ byte key would have been compared
// at a length nobody chose. The explicit 16..64 bound replaced that mask, and
// this is what holds it: a bound with no test is a comment.
func TestVerifyRefusesEveryShapeOfMalformedHash(t *testing.T) {
	// A real hash, to corrupt one field at a time. Deriving it rather than
	// hardcoding one keeps these cases honest under either build's parameters.
	valid, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	parts := strings.Split(valid, "$")
	if len(parts) != 6 {
		t.Fatalf("Hash wrote %d PHC fields, want 6: %q", len(parts), valid)
	}
	rebuild := func(mutate func(p []string)) string {
		cp := append([]string(nil), parts...)
		mutate(cp)
		return strings.Join(cp, "$")
	}

	for _, tc := range []struct {
		name string
		phc  string
		want string
	}{
		{
			name: "not PHC at all",
			phc:  "not-a-hash",
			want: "malformed hash",
		},
		{
			name: "a different algorithm, which must not be verified as argon2id",
			phc:  rebuild(func(p []string) { p[1] = "argon2i" }),
			want: "malformed hash",
		},
		{
			name: "an unreadable version field",
			phc:  rebuild(func(p []string) { p[2] = "v=nineteen" }),
			want: "malformed hash version",
		},
		{
			name: "cost parameters that do not parse",
			phc:  rebuild(func(p []string) { p[3] = "m=lots,t=some,p=few" }),
			want: "malformed hash params",
		},
		{
			name: "a salt that is not base64",
			phc:  rebuild(func(p []string) { p[4] = "!!!not-base64!!!" }),
			want: "malformed salt",
		},
		{
			name: "a key that is not base64",
			phc:  rebuild(func(p []string) { p[5] = "!!!not-base64!!!" }),
			want: "malformed key",
		},
		{
			// AAAA decodes to 3 bytes.
			name: "a key too short to be an Argon2id key",
			phc:  rebuild(func(p []string) { p[5] = "AAAA" }),
			want: "implausible key length",
		},
		{
			// 96 bytes: past the 64-byte ceiling, and the shape the old
			// &0xFFFF mask would have accepted after truncating it.
			name: "a key too long to be an Argon2id key",
			phc:  rebuild(func(p []string) { p[5] = strings.Repeat("A", 128) }),
			want: "implausible key length",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify("correct horse battery staple", tc.phc)
			if err == nil {
				t.Fatal("Verify accepted a malformed hash — a stored value Hash never wrote " +
					"must never authenticate anybody")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say what is wrong: got %q, want it to mention %q",
					err, tc.want)
			}
		})
	}
}

// Hash must not write two identical hashes for one password: the salt is what
// stops a stolen table from revealing which users share a password, and a
// constant salt would keep every assertion above passing.
func TestHashSaltsEveryDerivation(t *testing.T) {
	const plaintext = "correct horse battery staple"
	first, err := Hash(plaintext)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	second, err := Hash(plaintext)
	if err != nil {
		t.Fatalf("hashing again: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password are identical, so the salt is not random — " +
			"a stolen table would show which accounts share a password")
	}
	for _, phc := range []string{first, second} {
		if err := Verify(plaintext, phc); err != nil {
			t.Errorf("a salted hash does not verify: %v", err)
		}
	}
}
