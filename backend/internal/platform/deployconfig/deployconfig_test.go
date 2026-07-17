// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fullConfig = `
version: 1
organization:
  name: Gradion
  base_currency: EUR
  timezone: Europe/Berlin
bootstrap_admin:
  email: lars@example.com
  display_name: Lars
  password_file: /run/secrets/admin-password
seeds:
  pipeline:
    name: Sales
    stages:
      - { name: Qualified, probability: 10 }
      - { name: Proposal, probability: 40 }
  consent_purposes:
    - { key: marketing_email, label: Marketing email, double_opt_in: true }
  starter_automations: false
auth:
  password:
    enabled: true
email:
  enabled: true
  smtp:
    host: smtp.example.com
    port: 587
    username: crm@example.com
  from_address: crm@example.com
`

func TestParseAcceptsTheFullDocumentedShape(t *testing.T) {
	cfg, err := Parse([]byte(fullConfig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Organization.Name != "Gradion" || cfg.BootstrapAdmin.Email != "lars@example.com" {
		t.Fatalf("parsed organization/admin = %+v / %+v", cfg.Organization, cfg.BootstrapAdmin)
	}
	if cfg.Seeds.Pipeline.Name != "Sales" || len(cfg.Seeds.Pipeline.Stages) != 2 {
		t.Fatalf("parsed pipeline seed = %+v", cfg.Seeds.Pipeline)
	}
	if cfg.Seeds.StarterAutomations == nil || *cfg.Seeds.StarterAutomations {
		t.Fatal("starter_automations: false did not parse")
	}
	if !cfg.Auth.PasswordEnabled() || !cfg.Email.Enabled {
		t.Fatalf("auth/email switches lost: %+v %+v", cfg.Auth, cfg.Email)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	// A typo must never silently disable authentication: strict decoding
	// makes `auth.passwrd` a boot error, not an ignored key.
	_, err := Parse([]byte("version: 1\nauth:\n  passwrd:\n    enabled: false\n"))
	if err == nil || !strings.Contains(err.Error(), "passwrd") {
		t.Fatalf("unknown key parsed silently: %v", err)
	}
}

func TestParseValidatesFailClosed(t *testing.T) {
	cases := map[string]string{
		"unsupported version":     "version: 2\n",
		"bad timezone":            "version: 1\norganization: { name: X, timezone: Mars/Olympus }\n",
		"bad currency":            "version: 1\norganization: { name: X, base_currency: euros }\n",
		"admin without password":  "version: 1\nbootstrap_admin: { email: a@b.co, display_name: A }\n",
		"inline secret refused":   "version: 1\nbootstrap_admin: { email: a@b.co, display_name: A, password: hunter2hunter2 }\n",
		"empty pipeline":          "version: 1\nseeds: { pipeline: { name: Sales, stages: [] } }\n",
		"duplicate stage":         "version: 1\nseeds: { pipeline: { name: S, stages: [ { name: A, probability: 10 }, { name: A, probability: 20 } ] } }\n",
		"probability out of band": "version: 1\nseeds: { pipeline: { name: S, stages: [ { name: A, probability: 140 } ] } }\n",
		"purpose without label":   "version: 1\nseeds: { consent_purposes: [ { key: marketing_email } ] }\n",
		"email without smtp":      "version: 1\nemail: { enabled: true, from_address: a@b.co }\n",
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestLoadMissingFileBootsExistingInstallation(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing file must not be an error (it cannot bootstrap, only bind): %v", err)
	}
	if cfg.BootstrapAdmin != nil || !cfg.Auth.PasswordEnabled() {
		t.Fatalf("zero config = %+v, want no bootstrap admin + password auth on", cfg)
	}
}

func TestBootstrapAdminPasswordComesFromTheFileReference(t *testing.T) {
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("a bootstrap password!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := BootstrapAdmin{Email: "a@b.co", DisplayName: "A", PasswordFile: pwFile}
	pw, err := b.Password()
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	if pw != "a bootstrap password!" {
		t.Fatalf("password = %q, want the file content without the trailing newline", pw)
	}

	if err := os.WriteFile(pwFile, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Password(); err == nil {
		t.Fatal("an under-12-character bootstrap password was accepted")
	}
}
