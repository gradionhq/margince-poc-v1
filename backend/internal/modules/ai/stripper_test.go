// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The conformance table (B-EP06.5): credentials never survive into a
// model-bound payload, whatever shape they were captured in.
func TestSecretStripperRemovesCredentials(t *testing.T) {
	stripper := NewSecretStripper()
	cases := []struct {
		name   string
		text   string
		secret string
		kind   string
	}{
		{"anthropic api key", "use sk-ant-api03-FAKEFAKEFAKEfakefakefake0000 to call it", "sk-ant-api03-FAKEFAKEFAKEfakefakefake0000", "api_key"},
		{"github token", "push with ghp_16C7e42F292c6912E7710c838347Ae178B4a here", "ghp_16C7e42F292c6912E7710c838347Ae178B4a", "api_key"},
		{"slack token", "bot token xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij", "api_key"},
		{"google api key", "maps key AIzaSyA1234567890abcdefghijklmnopqrstu", "AIzaSyA1234567890abcdefghijklmnopqrstu", "api_key"},
		{"aws access key", "login AKIAIOSFODNN7EXAMPLE region eu-central-1", "AKIAIOSFODNN7EXAMPLE", "aws_access_key"},
		{"bearer header", "Authorization: Bearer abcdef0123456789TOKEN", "abcdef0123456789TOKEN", "bearer_token"},
		{"jwt", "session eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U expired", "eyJhbGciOiJIUzI1NiJ9", "jwt"},
		{"password assignment", "the config has password=hunter2secret in it", "hunter2secret", "credential_assignment"},
		{"password colon", `settings: api_key: "sk_live_verysecretvalue"`, "sk_live_verysecretvalue", "credential_assignment"},
		{"pem block", "attached:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nsecretbody\n-----END RSA PRIVATE KEY-----\ndone", "secretbody", "private_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stripped, report, err := stripper.Strip(context.Background(), []byte(tc.text))
			if err != nil {
				t.Fatalf("Strip: %v", err)
			}
			if strings.Contains(string(stripped), tc.secret) {
				t.Fatalf("secret survived stripping: %s", stripped)
			}
			if report.Findings == 0 {
				t.Fatalf("no findings reported for %q", tc.text)
			}
			found := false
			for _, k := range report.Kinds {
				if k == tc.kind {
					found = true
				}
			}
			if !found {
				t.Fatalf("kind %q not in report %v", tc.kind, report.Kinds)
			}
		})
	}
}

// Hygiene, not privacy (A8 revised): PII passes through untouched. A
// stripper that scrubbed names or emails would imply a pseudonymization
// guarantee the product explicitly does not make.
func TestSecretStripperLeavesPIIAlone(t *testing.T) {
	stripper := NewSecretStripper()
	text := "Maria Schneider <maria.schneider@example.de> called from +49 170 1234567 about the Hamburg deal"
	stripped, report, err := stripper.Strip(context.Background(), []byte(text))
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	if string(stripped) != text {
		t.Fatalf("PII was altered: %s", stripped)
	}
	if report.Findings != 0 {
		t.Fatalf("PII reported as a secret: %+v", report)
	}
}

// Stripping inside a marshaled request body must leave the JSON valid —
// the adapter puts the stripped bytes on the wire as-is.
func TestSecretStripperPreservesJSONFraming(t *testing.T) {
	stripper := NewSecretStripper()
	body, err := json.Marshal(map[string]any{
		"system": "help the rep",
		"messages": []map[string]string{
			{"role": "user", "content": "the vendor sent password=Sup3rSecret! and key sk-ant-api03-FAKEFAKEFAKEfakefakefake0000 plus\n-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stripped, report, err := stripper.Strip(context.Background(), body)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	if report.Findings < 3 {
		t.Fatalf("expected all three secrets found, got %+v", report)
	}
	if !json.Valid(stripped) {
		t.Fatalf("stripped payload is no longer valid JSON: %s", stripped)
	}
	if strings.Contains(string(stripped), "Sup3rSecret") {
		t.Fatalf("password survived: %s", stripped)
	}
}
