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
company_context:
  rollout: tasks
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
	if cfg.CompanyContext.EffectiveRollout() != CompanyContextTasks {
		t.Fatalf("company-context rollout = %q", cfg.CompanyContext.EffectiveRollout())
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
	cases := map[string]string{ // #nosec G101 -- yaml documents that must FAIL validation, not credentials
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
		"smtp port out of range":  "version: 1\nemail: { enabled: true, from_address: a@b.co, smtp: { host: h, port: 70000 } }\n",
		"password auth disabled":  "version: 1\nauth: { password: { enabled: false } }\n",
		"unknown context rollout": "version: 1\ncompany_context: { rollout: everything }\n",
		"ovb cap at ceiling":      "version: 1\noverlay_budget: { hubspot: { search: { ceiling: 4, cap: 4 }, rest: { ceiling: 100000, cap: 90000 } } }\n",
		"ovb cap above ceiling":   "version: 1\noverlay_budget: { hubspot: { search: { ceiling: 5, cap: 4 }, rest: { ceiling: 100000, cap: 100001 } } }\n",
		"ovb zero cap":            "version: 1\noverlay_budget: { hubspot: { search: { ceiling: 5, cap: 0 }, rest: { ceiling: 100000, cap: 90000 } } }\n",
		"ovb warn not below shed": "version: 1\noverlay_budget: { hubspot: { search: { ceiling: 5, cap: 4 }, rest: { ceiling: 100000, cap: 90000 }, warn_fraction: 0.95, shed_fraction: 0.90 } }\n",
		"ovb shed above one":      "version: 1\noverlay_budget: { hubspot: { search: { ceiling: 5, cap: 4 }, rest: { ceiling: 100000, cap: 90000 }, warn_fraction: 0.7, shed_fraction: 1.5 } }\n",
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestEffectiveOverlayBudgetFillsDefaultsAndMerges(t *testing.T) {
	// No block → the built-in HubSpot default with spec warn/shed fractions.
	def, err := Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	hs := def.EffectiveOverlayBudget()["hubspot"]
	if hs.Search.Cap != 4 || hs.REST.Cap != 90000 {
		t.Fatalf("default hubspot caps = search %d / rest %d, want 4 / 90000", hs.Search.Cap, hs.REST.Cap)
	}
	if hs.WarnFraction != 0.70 || hs.ShedFraction != 0.90 {
		t.Fatalf("default hubspot fractions = %g / %g, want 0.70 / 0.90", hs.WarnFraction, hs.ShedFraction)
	}

	// An operator override with fractions left unset gets the spec defaults.
	over, err := Parse([]byte("version: 1\noverlay_budget: { hubspot: { search: { ceiling: 10, cap: 8 }, rest: { ceiling: 200000, cap: 150000 } } }\n"))
	if err != nil {
		t.Fatalf("parse override: %v", err)
	}
	got := over.EffectiveOverlayBudget()["hubspot"]
	if got.Search.Cap != 8 || got.REST.Cap != 150000 {
		t.Fatalf("override caps = search %d / rest %d, want 8 / 150000", got.Search.Cap, got.REST.Cap)
	}
	if got.WarnFraction != 0.70 || got.ShedFraction != 0.90 {
		t.Fatalf("override fractions defaulted = %g / %g, want 0.70 / 0.90", got.WarnFraction, got.ShedFraction)
	}
}

func TestCompanyContextRolloutDefaultsToOnboarding(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.CompanyContext.EffectiveRollout(); got != CompanyContextOnboarding {
		t.Fatalf("default rollout = %q, want onboarding", got)
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

func TestParseAICapturePayloads(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nai:\n  capture_payloads: true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.AI.CapturePayloads {
		t.Fatal("ai.capture_payloads should be true")
	}
	// Default is off.
	def, err := Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("parse default: %v", err)
	}
	if def.AI.CapturePayloads {
		t.Fatal("ai.capture_payloads must default to false")
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
