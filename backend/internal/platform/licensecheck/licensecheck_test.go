// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package licensecheck

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
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
			got, err := Resolve(context.Background(), tc.token, checkedAt)
			if err != nil {
				t.Fatalf("Resolve(%q) reported a module fault rather than a verdict: %v", tc.token, err)
			}
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
	got, err := Resolve(context.Background(), token, checkedAt)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
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
		got, err := Resolve(context.Background(), token, checkedAt)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
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

// moduleOutput is the success output of the module release this tree pins: the
// license it verified beside the grant it was asked about.
//
// The shape is pinned HERE because nothing in this repository can mint a token
// the bundled keyset trusts, so the success path never runs end to end in a
// test. When upstream moved the grants under their own key, every test still
// passed and only an installation would have found out: the grant map decoded
// as {"license":…, "grants":…} and every seat count silently disappeared. This
// fixture is the tripwire that was missing.
const moduleOutput = `{"license":{"id":"01890a5d-ac96-774b-bcce-b302099a8057","subject":"acme-prod",` +
	`"org":"Acme GmbH","name":"Ada Lovelace","email":"ada@acme.example","key_id":"Ujmh",` +
	`"issued_at":"2026-08-15T09:00:00Z","not_before":"2026-08-15T09:00:00Z",` +
	`"expiry":"2027-08-15T09:00:00Z","in_grace":false},` +
	`"grants":{"seats":10,"feature":true,"something_new":7}}`

// The grant map is carried whole. A build that projected it into known fields
// would drop the attributes a later license adds, which is the one thing the
// open attribute format exists to prevent.
func TestDecodeResultCarriesUnknownAttributes(t *testing.T) {
	t.Parallel()
	result, err := decodeResult([]byte(moduleOutput))
	if err != nil {
		t.Fatalf("decodeResult: %v", err)
	}
	if len(result.Grants) != 3 {
		t.Fatalf("decoded %d attributes, want 3: %v", len(result.Grants), result.Grants)
	}
	if result.Grants["something_new"] != float64(7) {
		t.Errorf("an attribute this build does not know was dropped: %v", result.Grants)
	}
}

// The module names the license it verified. Nothing in this build acts on the
// detail yet, but a decode that dropped it would be found only by whoever
// needed it, and the grants sit in the same document.
func TestDecodeResultCarriesTheLicenseItVerified(t *testing.T) {
	t.Parallel()
	result, err := decodeResult([]byte(moduleOutput))
	if err != nil {
		t.Fatalf("decodeResult: %v", err)
	}
	if result.License.Subject != "acme-prod" || result.License.Org != "Acme GmbH" {
		t.Errorf("subject/org = %q/%q, want acme-prod/Acme GmbH", result.License.Subject, result.License.Org)
	}
	if result.License.ID == "" || result.License.Expiry.IsZero() {
		t.Errorf("id/expiry = %q/%s, want both set", result.License.ID, result.License.Expiry)
	}
	if result.License.InGrace {
		t.Error("a license inside its validity reports in_grace")
	}
}

func TestDecodeResultRefusesOutputThatIsNotAResult(t *testing.T) {
	t.Parallel()
	if _, err := decodeResult([]byte("not json")); err == nil {
		t.Error("decodeResult accepted output that is not JSON")
	}
}

// craftedToken builds an unsigned token whose CLAIMS carry payload. The module
// decodes and quotes claim content before it verifies the signature, so this is
// the shape that puts attacker-chosen text on the path to a log line.
func craftedToken(t *testing.T, claims string) string {
	t.Helper()
	segment := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return segment(`{"alg":"EdDSA","kid":"x"}`) + "." + segment(claims) + "." + strings.Repeat("A", 86)
}

// The reason reaches a boot error and a process log verbatim, and the module
// quotes claim content it has NOT verified — so a token supplied by a reseller, a
// deploy pipeline, or a compromised secret store chooses that text. Left raw it
// forges log records: two quotes and a newline write a plausible
// `level=INFO msg="license verified"` line into a stderr-parsing collector.
func TestARejectionReasonCannotForgeALogLine(t *testing.T) {
	t.Parallel()
	// An object-valued attribute is quoted back as raw JSON, so its newlines
	// survive; the forged record carries no double quotes of its own because they
	// would make the claim invalid JSON and the module would reject it before it
	// ever quoted anything.
	const forgery = `level=INFO msg=license_verified state=valid seats=9999`
	token := craftedToken(t, `{"iss":"margince-license-authority","jti":"01890a5d-ac96-774b-bcce-b302099a8057",`+
		`"exp":9999999999,"iat":1,"pgs":{"margince":{"generation":0,"seats":{"a":1,`+"\n"+`"b":"`+forgery+`",`+"\n"+`"c":2}}}}`)

	// The premise first: without this, the assertions below would hold for a
	// token the module never quoted, and the test would prove nothing.
	_, raw := check(context.Background(), bundledModule, issuer, product, generation, token)
	if raw == nil {
		t.Fatal("the bundled module accepted an unsigned token")
	}
	if !strings.Contains(raw.Error(), forgery) || !strings.ContainsAny(raw.Error(), "\n") {
		t.Fatalf("this token no longer reaches the module's claim-quoting path, so the case below is vacuous: %q", raw)
	}

	got, err := Resolve(context.Background(), token, checkedAt)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.State != StateRejected {
		t.Fatalf("state = %q, want %q", got.State, StateRejected)
	}
	if strings.ContainsAny(got.Reason, "\n\r") {
		t.Errorf("the reason spans lines, so it can write a log record of its own: %q", got.Reason)
	}
	for _, control := range got.Reason {
		if unicode.IsControl(control) {
			t.Errorf("the reason carries the control character %q: %q", control, got.Reason)
			break
		}
	}
}

// An oversized claim would otherwise write its whole self on every boot of a
// crashlooping role, and on every posture transition after that.
func TestARejectionReasonIsBounded(t *testing.T) {
	t.Parallel()
	token := craftedToken(t, `{"iss":"margince-license-authority","jti":"01890a5d-ac96-774b-bcce-b302099a8057",`+
		`"exp":9999999999,"iat":1,"pgs":{"margince":{"generation":0,"seats":"`+strings.Repeat("A", 200_000)+`"}}}`)

	got, err := Resolve(context.Background(), token, checkedAt)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got.Reason) > reasonLimit+len("… (truncated)") {
		t.Errorf("the reason is %d bytes, past the %d-byte bound", len(got.Reason), reasonLimit)
	}
	if !strings.HasSuffix(got.Reason, "(truncated)") {
		t.Errorf("a cut reason does not say it was cut: %q", got.Reason)
	}
	if !utf8.ValidString(got.Reason) {
		t.Error("the cut left invalid UTF-8, which reads as corruption rather than as a cut")
	}
}

// The seam between a VERDICT and a module that could not run, asserted on both
// sides against the real bundled module. Everything downstream turns on it: get
// it backwards and a memory-pressure fault reports a customer's license as
// refused, or a genuinely refused license reads as a machine problem.
func TestAVerdictAndAModuleFaultAreDistinguishableByType(t *testing.T) {
	t.Parallel()
	_, verdict := check(context.Background(), bundledModule, issuer, product, generation, "not-a-license")
	if verdict == nil {
		t.Fatal("the bundled module accepted a token that is not a license")
	}
	if !errors.Is(verdict, ErrVerdict) {
		t.Fatalf("a refused license is not an ErrVerdict, so Resolve would report it as a module fault: %v", verdict)
	}
	_, fault := check(context.Background(), gzipped(t, []byte("not webassembly")), issuer, product, generation, "t")
	if fault == nil {
		t.Fatal("check accepted a module that is not WebAssembly")
	}
	if errors.Is(fault, ErrVerdict) {
		t.Errorf("a module that could not run reads as a verdict about the license: %v", fault)
	}
}

// A module that cannot run is reported as a FAULT, separately from any verdict:
// the boot refuses on it either way, but a re-check must not report a license as
// refused on the strength of an error nobody's license caused.
func TestResolveReportsAModuleFaultSeparatelyFromAVerdict(t *testing.T) {
	t.Parallel()
	posture, err := Resolve(context.Background(), "not-a-license", checkedAt)
	if err != nil {
		t.Fatalf("a refused license came back as a module fault: %v", err)
	}
	if posture.State != StateRejected {
		t.Errorf("state = %q, want %q", posture.State, StateRejected)
	}
}
