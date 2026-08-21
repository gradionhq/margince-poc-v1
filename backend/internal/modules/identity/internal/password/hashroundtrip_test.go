// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package password

import "testing"

// keyLength and saltLength are NOT cost parameters and are deliberately absent
// from the tagged files. This pins the property that makes that separation
// matter: Verify refuses a key outside 16..64 bytes, so a keyLength below that
// floor would make every hash this package writes fail its own verification.
//
// The failure would not look like a bad constant. It would look like a correct
// password being rejected, somewhere in a fixture, with nothing pointing here.
func TestTheKeyAndSaltLengthsStayInsideWhatVerifyAccepts(t *testing.T) {
	if keyLength < 16 || keyLength > 64 {
		t.Errorf("keyLength is %d, outside the 16..64 Verify accepts — every hash written "+
			"with it would fail its own Verify", keyLength)
	}
	if saltLength < 16 {
		t.Errorf("saltLength is %d; RFC 9106 wants 16 bytes for password hashing", saltLength)
	}
}

// A hash written by THIS build must verify under it. Trivially true today and
// the point is that it stays true under the integration tag as well: the tagged
// build compiles this same test with the cheap parameters, so if a future edit
// puts an invalid combination there — a memory below the 8*threads floor argon2
// requires, say — the integration lane fails here rather than somewhere a
// fixture logs in.
func TestAHashThisBuildWritesVerifiesUnderIt(t *testing.T) {
	const plaintext = "correct horse battery staple"
	phc, err := Hash(plaintext)
	if err != nil {
		t.Fatalf("hashing with this build's parameters: %v", err)
	}
	if err := Verify(plaintext, phc); err != nil {
		t.Fatalf("this build wrote a hash it cannot verify: %v", err)
	}
	if err := Verify("wrong", phc); err == nil {
		t.Fatal("Verify accepted the wrong password, so the comparison proves nothing")
	}
}
