// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// What a secret reference must do, and the two things it must never do: accept
// a literal, or repeat one back.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// The variable names in these tests are the OPERATOR's to choose — an ${env:}
// reference names whatever a deployment called its secret, and the product
// reads none of them. They are assembled rather than written out because
// TestEveryEnvVarIsDocumented reads any MARGINCE_* literal in Go as a variable
// the code consumes and demands a row in configuration.md; documenting these
// would claim the product reads a variable it has never heard of.
var (
	smtpPasswordVar = envNamespace + "SMTP_PASSWORD"
	absentVar       = envNamespace + "ABSENT"
)

const envNamespace = "MARGINCE" + "_"

func decodeSecret(t *testing.T, doc string) (Secret, error) {
	t.Helper()
	var out struct {
		S Secret `yaml:"s"`
	}
	err := yaml.Unmarshal([]byte(doc), &out)
	return out.S, err
}

func TestALiteralSecretIsRefusedAtDecode(t *testing.T) {
	// The whole point: refused before anything runs, so a password cannot reach
	// a live process even once. margince.yaml is the most-pasted artefact an
	// installation has — a value that CAN be written there eventually is.
	const literal = "hunter2-the-real-password"
	_, err := decodeSecret(t, "s: "+literal)
	if err == nil {
		t.Fatal("a literal secret was accepted; it must be refused")
	}
	if strings.Contains(err.Error(), literal) {
		t.Errorf("the refusal repeated the secret it refused: %v", err)
	}
	// And it says what to write instead, or an operator is stuck.
	for _, want := range []string{"${file:", "${env:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not show the %s form: %v", want, err)
		}
	}
}

func TestBothReferenceFormsResolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ doc, want string }{
		// Trimmed both ends: every editor and secret store terminates a file
		// its own way, and a trailing newline must not become part of a
		// password.
		"file": {doc: "s: ${file:" + path + "}", want: "from-the-file"},
		"env":  {doc: "s: ${env:PROBE_TOKEN}", want: "from-the-environment"},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := decodeSecret(t, tc.doc)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			got, err := s.Resolve(config.Static(map[string]string{"PROBE_TOKEN": " from-the-environment "}))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnUnconfiguredSecretIsNotAFailure(t *testing.T) {
	// An installation that names no licence token runs unlicensed. That is a
	// supported state, so absence is not an error at this layer — the caller
	// decides what is required.
	s, err := decodeSecret(t, "s: \"\"")
	if err != nil {
		t.Fatalf("an empty secret field is not an error: %v", err)
	}
	if s.Configured() {
		t.Error("an empty field reports as configured")
	}
	got, err := s.Resolve(config.Static(nil))
	if err != nil || got != "" {
		t.Errorf("Resolve on an unconfigured secret = %q, %v; want empty and no error", got, err)
	}
}

func TestAReferenceToNothingSaysSo(t *testing.T) {
	// The failures that otherwise present as "unlicensed" or "no password"
	// rather than as the mistakes they are.
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ doc, wantIn string }{
		"a file that is not there": {doc: "s: ${file:" + filepath.Join(dir, "absent") + "}", wantIn: "cannot be read"},
		"a variable nobody set":    {doc: "s: ${env:PROBE_UNSET}", wantIn: "unset or empty"},
		"a file holding nothing":   {doc: "s: ${file:" + empty + "}", wantIn: "holds nothing"},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := decodeSecret(t, tc.doc)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			got, resolveErr := s.withField("probe.secret").Resolve(config.Static(nil))
			// A missing FILE fails on read; an unset VARIABLE resolves to
			// empty and the caller reports it. Both end in a message naming
			// the field and what to do.
			msg := ""
			switch {
			case resolveErr != nil:
				msg = resolveErr.Error()
			case got == "":
				msg = s.withField("probe.secret").Missing().Error()
			default:
				t.Fatalf("resolved %q, want a failure", got)
			}
			if !strings.Contains(msg, tc.wantIn) {
				t.Errorf("message %q does not contain %q", msg, tc.wantIn)
			}
			if !strings.Contains(msg, "probe.secret") {
				t.Errorf("message %q does not name the field to fix", msg)
			}
		})
	}
}

func TestAReferenceNamingNothingIsRefused(t *testing.T) {
	for _, doc := range []string{"s: ${env:}", "s: ${file:}"} {
		if _, err := decodeSecret(t, doc); err == nil {
			t.Errorf("%q was accepted; a reference to nothing cannot resolve", doc)
		}
	}
}

func TestAnOversizedFileIsRefusedWithoutReadingIt(t *testing.T) {
	// A secret is a password, a key or a token. Anything larger is a path typed
	// wrong, and pulling it into memory to discover that is the wrong order.
	dir := t.TempDir()
	big := filepath.Join(dir, "not-a-secret")
	if err := os.WriteFile(big, make([]byte, secretFileLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := decodeSecret(t, "s: ${file:"+big+"}")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.withField("probe.secret").Resolve(config.Static(nil))
	if err == nil || !strings.Contains(err.Error(), "a secret is not") {
		t.Fatalf("an oversized file resolved to %v; want a refusal naming the size", err)
	}
}

// TestEverySecretFieldRefusesALiteral is the point of having one mechanism:
// the rule holds for each secret this file carries, not just the one whose
// test somebody remembered to write.
//
// Derived from a real document rather than from the Secret type directly, so a
// field that is declared as a plain string — the way all three of these were
// before — fails here instead of quietly accepting a password.
func TestEverySecretFieldRefusesALiteral(t *testing.T) {
	for name, doc := range map[string]string{
		"bootstrap_admin.password": "version: 1\nbootstrap_admin:\n  email: a@b.co\n  password: hunter2-literal\n",
		"email.smtp.password":      "version: 1\nemail:\n  smtp:\n    host: mail\n    password: hunter2-literal\n",
		"license.token":            "version: 1\nlicense:\n  token: hunter2-literal\n",
	} {
		t.Run(name, func(t *testing.T) {
			var cfg Config
			err := yaml.Unmarshal([]byte(doc), &cfg)
			if err == nil {
				t.Fatalf("%s accepted a literal secret", name)
			}
			if strings.Contains(err.Error(), "hunter2-literal") {
				t.Errorf("the refusal for %s repeated the secret: %v", name, err)
			}
		})
	}
}

// And each accepts the reference form, so the refusal above is not simply a
// field nobody can configure.
func TestEverySecretFieldAcceptsAReference(t *testing.T) {
	for name, doc := range map[string]string{
		"bootstrap_admin.password": "version: 1\nbootstrap_admin:\n  email: a@b.co\n  password: ${env:X}\n",
		"email.smtp.password":      "version: 1\nemail:\n  smtp:\n    host: mail\n    password: ${env:X}\n",
		"license.token":            "version: 1\nlicense:\n  token: ${file:/run/secrets/l}\n",
	} {
		t.Run(name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
				t.Fatalf("%s refused a reference: %v", name, err)
			}
		})
	}
}

// Resolving the SMTP credential, across the three states a relay can be in.
// The middle one is the reason this is not just "does the reference work": an
// operator who configured no credential has an UNAUTHENTICATED relay, which is
// a real deployment and must not be reported as a missing secret.
func TestSMTPPasswordResolvesEachConfiguredSource(t *testing.T) {
	dir := t.TempDir()
	mounted := filepath.Join(dir, "smtp-password")
	if err := os.WriteFile(mounted, []byte("from-the-secret-store\n"), 0o600); err != nil {
		t.Fatalf("writing the mounted secret: %v", err)
	}
	lookup := config.Static(map[string]string{smtpPasswordVar: "from-the-environment"})

	for name, tc := range map[string]struct {
		email Email
		want  string
	}{
		"a reference to the environment": {
			email: Email{SMTP: SMTP{Password: mustSecret(t, "${env:"+smtpPasswordVar+"}")}},
			want:  "from-the-environment",
		},
		"a reference to a mounted file": {
			email: Email{SMTP: SMTP{Password: mustSecret(t, "${file:"+mounted+"}")}},
			want:  "from-the-secret-store",
		},
		// The legacy spelling keeps working: a deployment that already names a
		// path must boot unchanged rather than be told to migrate first.
		"the original password_file spelling": {
			email: Email{SMTP: SMTP{PasswordFile: mounted}},
			want:  "from-the-secret-store",
		},
		"no credential at all": {
			email: Email{SMTP: SMTP{Host: "mail.example.test"}},
			want:  "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.email.SMTPPassword(lookup)
			if err != nil {
				t.Fatalf("SMTPPassword: %v", err)
			}
			if got != tc.want {
				t.Errorf("SMTPPassword = %q, want %q", got, tc.want)
			}
		})
	}
}

// The reference wins when a deployment carries both, because it is the form
// that can name the environment and the one an operator migrating forward would
// have added deliberately. Getting this backwards would silently keep reading
// the file somebody had just replaced.
func TestTheReferenceWinsOverTheLegacyPath(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale")
	if err := os.WriteFile(stale, []byte("the-old-credential"), 0o600); err != nil {
		t.Fatalf("writing the stale file: %v", err)
	}
	email := Email{SMTP: SMTP{
		Password:     mustSecret(t, "${env:"+smtpPasswordVar+"}"),
		PasswordFile: stale,
	}}
	got, err := email.SMTPPassword(config.Static(map[string]string{smtpPasswordVar: "the-new-credential"}))
	if err != nil {
		t.Fatalf("SMTPPassword: %v", err)
	}
	if got != "the-new-credential" {
		t.Errorf("SMTPPassword = %q; the reference must win over password_file", got)
	}
}

// A required secret that resolved to nothing names the source the deployment
// CHOSE, so an operator who wrote ${file:...} is told the file is empty rather
// than lectured about a form they did not use.
func TestMissingNamesTheSourceTheDeploymentChose(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want string
	}{
		"an environment reference": {raw: "${env:" + absentVar + "}", want: absentVar + " is unset or empty"},
		"a file reference":         {raw: "${file:/run/secrets/absent}", want: "/run/secrets/absent holds nothing"},
	} {
		t.Run(name, func(t *testing.T) {
			err := mustSecret(t, tc.raw).withField("license.token").Missing()
			if err == nil {
				t.Fatal("a missing required secret reported no error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Missing() = %q, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "license.token") {
				t.Errorf("Missing() = %q; it must name the key an operator edits", err)
			}
		})
	}
	// Nothing configured at all is the third state, and it has to say what to
	// write rather than which source failed — there is no source yet.
	var unset Secret
	err := unset.withField("license.token").Missing()
	if err == nil || !strings.Contains(err.Error(), "${file:") || !strings.Contains(err.Error(), "${env:") {
		t.Errorf("Missing() on an unconfigured secret = %v; it must show both forms", err)
	}
}

// mustSecret decodes a reference the way the loader does, so these tests
// exercise the real decode path rather than constructing the unexported fields.
func mustSecret(t *testing.T, raw string) Secret {
	t.Helper()
	var s Secret
	if err := yaml.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("decoding %q: %v", raw, err)
	}
	return s
}
