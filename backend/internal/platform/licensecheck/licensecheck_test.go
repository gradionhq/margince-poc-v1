// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package licensecheck

import (
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"
	"time"
)

// checkedAt is the fixed instant every posture in this file is stamped with, so
// a test that cares about the stamp compares against a value and not a window.
var checkedAt = time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)

// The rejection cases below run the REAL bundled module. They are the whole
// behaviour available to this side: the published module trusts only the
// production keyset, so no token this repository can produce is ever accepted,
// and the accepted path stays unproven here until upstream also publishes the
// test-authority build (issue #1190).
func TestResolveRejectsAnythingTheBundledModuleWillNotHonor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "not a token at all", token: "hello"},
		{name: "three dots but no JWT", token: "a.b.c"},
		{
			name: "well-formed JWT signed by nobody the keyset trusts",
			// header {"alg":"EdDSA","kid":"x"}, an empty-ish claim set, and a
			// signature of the right shape over the wrong key.
			token: "eyJhbGciOiJFZERTQSIsImtpZCI6IngifQ." +
				"eyJpc3MiOiJtYXJnaW5jZS1saWNlbnNlLWF1dGhvcml0eSJ9." +
				strings.Repeat("A", 86),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Resolve(context.Background(), tc.token, checkedAt)
			if got.State != StateRejected {
				t.Fatalf("Resolve(%q) state = %q, want %q", tc.token, got.State, StateRejected)
			}
			if got.Reason == "" {
				t.Error("a rejected posture carries no reason; an operator is told nothing to fix")
			}
			if got.Grants != nil {
				t.Errorf("a rejected posture carries grants %v; nothing was granted", got.Grants)
			}
			if !got.CheckedAt.Equal(checkedAt) {
				t.Errorf("CheckedAt = %v, want the injected %v", got.CheckedAt, checkedAt)
			}
		})
	}
}

// The token never reaches the rejection reason. It is the one secret in this
// path, and the reason is copied into the boot error and the process log.
func TestRejectionReasonDoesNotEchoTheToken(t *testing.T) {
	t.Parallel()
	const token = "eyJhbGciOiJFZERTQSJ9.c3VwZXItc2VjcmV0LWxpY2Vuc2U.AAAA"
	got := Resolve(context.Background(), token, checkedAt)
	if got.State != StateRejected {
		t.Fatalf("state = %q, want %q", got.State, StateRejected)
	}
	if strings.Contains(got.Reason, token) {
		t.Errorf("the reason quotes the whole token: %q", got.Reason)
	}
	if strings.Contains(got.Reason, "c3VwZXItc2VjcmV0LWxpY2Vuc2U") {
		t.Errorf("the reason quotes the token's payload segment: %q", got.Reason)
	}
}

func TestResolveReportsAbsentForNoToken(t *testing.T) {
	t.Parallel()
	// A whitespace-only file reference reads as no license, not as a token the
	// module should be asked about.
	for _, token := range []string{"", "   ", "\n"} {
		got := Resolve(context.Background(), token, checkedAt)
		if got.State != StateAbsent {
			t.Errorf("Resolve(%q) state = %q, want %q", token, got.State, StateAbsent)
		}
		if got.Reason != "" {
			t.Errorf("Resolve(%q) reason = %q, want empty: nothing was refused", token, got.Reason)
		}
	}
}

// A module this build cannot execute is a packaging fault, not an absence:
// reading either failure below as "unlicensed" would turn one into a silent
// downgrade. Both are errors, and each names its own stage — a blob that never
// unwrapped and one that unwrapped into something wazero refused are different
// things to go and fix.
func TestAModuleThatCannotRunIsRejectedRatherThanTreatedAsUnlicensed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		module []byte
		stage  string
	}{
		{
			// Not raw wasm and not gzip, so it is read as brotli and is not that
			// either — the shape a truncated download has.
			name:   "bytes that unwrap as nothing",
			module: []byte("this is not a module in any framing"),
			stage:  "decompress module",
		},
		{
			name:   "a well-formed archive of something that is not wasm",
			module: gzipped(t, []byte("still not webassembly")),
			stage:  "run module",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := check(context.Background(), tc.module, issuer, product, generation, "any-token")
			if err == nil {
				t.Fatal("check accepted a module that is not WebAssembly")
			}
			if !strings.Contains(err.Error(), tc.stage) {
				t.Errorf("error = %q, want it to name the %q stage so the fault is placeable", err, tc.stage)
			}
		})
	}
}

// gzipped frames payload the way the older published artifact was framed, which
// the host still accepts.
func gzipped(t *testing.T, payload []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return out.Bytes()
}

func TestSeats(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		posture Posture
		want    int
		wantOK  bool
	}{
		{
			name:    "a granted count",
			posture: Posture{State: StateValid, Grants: Grants{SeatsAttribute: float64(25)}},
			want:    25,
			wantOK:  true,
		},
		{
			name:    "a grant of zero seats is a count, not an absence",
			posture: Posture{State: StateValid, Grants: Grants{SeatsAttribute: float64(0)}},
			want:    0,
			wantOK:  true,
		},
		{
			name:    "a license that caps nothing",
			posture: Posture{State: StateValid, Grants: Grants{"feature": true}},
			wantOK:  false,
		},
		{
			name:    "a fractional count is not a seat count",
			posture: Posture{State: StateValid, Grants: Grants{SeatsAttribute: 2.5}},
			wantOK:  false,
		},
		{
			name:    "a negative count is not a seat count",
			posture: Posture{State: StateValid, Grants: Grants{SeatsAttribute: float64(-1)}},
			wantOK:  false,
		},
		{
			name:    "a string where a number belongs",
			posture: Posture{State: StateValid, Grants: Grants{SeatsAttribute: "25"}},
			wantOK:  false,
		},
		{
			name:    "grants that survived a rejection are not read",
			posture: Posture{State: StateRejected, Grants: Grants{SeatsAttribute: float64(25)}},
			wantOK:  false,
		},
		{
			name:    "no license grants no seats",
			posture: Posture{State: StateAbsent},
			wantOK:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tc.posture.Seats()
			if ok != tc.wantOK {
				t.Fatalf("Seats() ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("Seats() = %d, want %d", got, tc.want)
			}
		})
	}
}

// The grant map is carried whole. A build that projected it into known fields
// would drop the attributes a later license adds, which is the one thing the
// open attribute format exists to prevent.
func TestDecodeGrantsCarriesUnknownAttributes(t *testing.T) {
	t.Parallel()
	grants, err := decodeGrants([]byte(`{"seats":10,"feature":true,"something_new":7}`))
	if err != nil {
		t.Fatalf("decodeGrants: %v", err)
	}
	if len(grants) != 3 {
		t.Fatalf("decoded %d attributes, want 3: %v", len(grants), grants)
	}
	if grants["something_new"] != float64(7) {
		t.Errorf("an attribute this build does not know was dropped: %v", grants)
	}
}

func TestDecodeGrantsRefusesOutputThatIsNotAGrant(t *testing.T) {
	t.Parallel()
	if _, err := decodeGrants([]byte("not json")); err == nil {
		t.Error("decodeGrants accepted output that is not JSON")
	}
}
