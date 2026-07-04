package search

import (
	"encoding/base64"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestRankedCursorRoundTripsFullPrecision(t *testing.T) {
	in := rankedCursor{Score: 0.1234567890123456789, Type: "activity", ID: ids.NewV7()}
	out, err := decodeCursor(encodeCursor(in))
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("cursor round trip lost information: %+v != %+v", out, in)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for name, cursor := range map[string]string{
		"not base64":    "%%%",
		"too few parts": base64.RawURLEncoding.EncodeToString([]byte("only-one")),
		"bad score":     base64.RawURLEncoding.EncodeToString([]byte("NaNish|person|" + ids.NewV7().String())),
		"bad id":        base64.RawURLEncoding.EncodeToString([]byte("0.5|person|not-a-uuid")),
	} {
		if _, err := decodeCursor(cursor); err == nil {
			t.Errorf("%s: malformed cursor accepted", name)
		}
	}
}

func TestClampLimitFollowsTheContractBounds(t *testing.T) {
	for in, want := range map[int]int{0: 50, -3: 50, 1: 1, 200: 200, 900: 200} {
		if got := clampLimit(in); got != want {
			t.Errorf("clampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}
