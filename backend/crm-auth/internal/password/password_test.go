package password

import (
	"errors"
	"strings"
	"testing"
)

func TestHashVerify_roundTrip(t *testing.T) {
	phc, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(phc, "$argon2id$") {
		t.Errorf("hash %q is not PHC argon2id", phc)
	}
	if err := Verify("correct horse battery staple", phc); err != nil {
		t.Errorf("Verify(correct password): %v", err)
	}
	if err := Verify("wrong", phc); !errors.Is(err, ErrMismatch) {
		t.Errorf("Verify(wrong password) = %v, want ErrMismatch", err)
	}
}

func TestHash_saltsAreUnique(t *testing.T) {
	a, _ := Hash("same input")
	b, _ := Hash("same input")
	if a == b {
		t.Error("two hashes of the same input are identical; salt is not random")
	}
}

func TestVerify_rejectsMalformed(t *testing.T) {
	for _, phc := range []string{"", "plaintext", "$argon2i$v=19$m=1,t=1,p=1$aa$bb"} {
		if err := Verify("x", phc); err == nil {
			t.Errorf("Verify accepted malformed hash %q", phc)
		}
	}
}
