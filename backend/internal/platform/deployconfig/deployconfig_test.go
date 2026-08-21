// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
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
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"), runtimeenv.Production)
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

func TestParseRatesFxCurrencies(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nrates:\n  fx_source: https://example.test/fx\n  fx_currencies: [USD, GBP, CHF]\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := cfg.Rates.Fx, "https://example.test/fx"; got != want {
		t.Fatalf("rates.fx_source = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.Rates.FxCurrencies, ","), "USD,GBP,CHF"; got != want {
		t.Fatalf("rates.fx_currencies = %q, want %q", got, want)
	}
	// Unset ⇒ nil (the worker supplies the USD/GBP/CHF default); parsing must
	// not invent a value.
	def, err := Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("parse default: %v", err)
	}
	if def.Rates.FxCurrencies != nil {
		t.Fatalf("rates.fx_currencies must default to nil, got %v", def.Rates.FxCurrencies)
	}
}

// The annotated example shipped for operators must always satisfy the real
// parser — a schema change here without a matching edit there would hand every
// new deployment (and every `make dev`, which copies it) a config that fails at
// boot. It also guards the now-active rates block against a typo.
func TestShippedExampleConfigParses(t *testing.T) {
	cfg, err := Load("../../../../config/margince.example.yaml", runtimeenv.Production)
	if err != nil {
		t.Fatalf("config/margince.example.yaml no longer parses: %v", err)
	}
	if got := strings.Join(cfg.Rates.FxCurrencies, ","); got != "USD,GBP,CHF" {
		t.Fatalf("example rates.fx_currencies = %q, want the documented USD,GBP,CHF", got)
	}
	// The example declares the connector so a local stack serves /mcp without a
	// hand edit. It is safe to ship on only because the api refuses to boot on
	// this gate with no --public-base-url; the CODE default stays off
	// (TestMCPConnectorGateDefaultsOff), which is what protects an installation
	// that writes its own file.
	if !cfg.MCP.ConnectorEnabled {
		t.Fatal("example mcp.connector_enabled must stay true — commenting it out silently costs every local stack the /mcp surface")
	}
}

func TestParseRatesFxCurrenciesFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"non-iso code", "version: 1\nrates:\n  fx_currencies: [USD, US]\n"},
		{"non-letter code", "version: 1\nrates:\n  fx_currencies: [USD, \"12$\"]\n"},
		{"duplicate", "version: 1\nrates:\n  fx_currencies: [USD, usd]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.doc)); err == nil {
				t.Fatalf("%s: Parse accepted a malformed fx_currencies set; a typo must fail at boot", tc.name)
			}
		})
	}
}

// writeTemp writes doc to a fresh file under t.TempDir() and returns its
// path, for tests that exercise Load (rather than Parse) against a real
// file on disk.
func writeTemp(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "margince.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return path
}

func TestMCPConnectorGateDefaultsOff(t *testing.T) {
	cfg, err := Load(writeTemp(t, "version: 1\norganization:\n  name: T\n"), runtimeenv.Production)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.ConnectorEnabled {
		t.Fatal("the connector gate must default OFF — an unset flag must never expose /mcp")
	}
	on, err := Load(writeTemp(t, "version: 1\norganization:\n  name: T\nmcp:\n  connector_enabled: true\n"), runtimeenv.Production)
	if err != nil {
		t.Fatal(err)
	}
	if !on.MCP.ConnectorEnabled {
		t.Fatal("mcp.connector_enabled: true must parse")
	}
}

// The retention posture defaults to the historical behaviour byte for byte: an
// omitted block, an empty value and the explicit `standard` all leave storage
// limitation enforcing. An installation under a keep-everything obligation is
// the one that has to say so, and only `retain_only` says it.
func TestRetentionSeedPostureDefaultsToStandard(t *testing.T) {
	for name, doc := range map[string]string{
		"block omitted":     "version: 1\n",
		"seeds without it":  "version: 1\nseeds:\n  starter_automations: false\n",
		"empty value":       "version: 1\nseeds:\n  retention:\n    default_policy: \"\"\n",
		"explicit standard": "version: 1\nseeds:\n  retention:\n    default_policy: standard\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cfg.Seeds.RetainOnly() {
				t.Error("the retain-only posture was seeded ON without the operator asking; " +
					"a fresh installation must enforce storage limitation (Art. 5(1)(e)) out of the box")
			}
		})
	}
}

func TestRetentionSeedRetainOnlyTurnsThePostureOn(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nseeds:\n  retention:\n    default_policy: retain_only\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Seeds.Retention == nil || cfg.Seeds.Retention.DefaultPolicy != RetentionRetainOnlyPosture {
		t.Fatalf("seeds.retention did not parse: %+v", cfg.Seeds.Retention)
	}
	if !cfg.Seeds.RetainOnly() {
		t.Error("default_policy: retain_only did not seed the posture; the nightly pass could destroy " +
			"records between bootstrap and the first admin login, which is the window this key closes")
	}
}

// A typo must not fall back to `standard` in silence: for the one deployment
// that set the key on purpose, silence is the exact failure it set it to prevent.
func TestRetentionSeedRefusesAnUnknownPosture(t *testing.T) {
	for _, value := range []string{"retain-only", "retainonly", "keep_everything", "Standard"} {
		_, err := Parse([]byte("version: 1\nseeds:\n  retention:\n    default_policy: " + value + "\n"))
		if err == nil {
			t.Fatalf("Parse accepted seeds.retention.default_policy: %q", value)
		}
		for _, want := range []string{"seeds.retention.default_policy", value, RetentionStandardPosture, RetentionRetainOnlyPosture} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal for %q omits %q, so the operator cannot fix the file from it: %v", value, want, err)
			}
		}
	}
}

func TestBootstrapAdminPasswordComesFromTheFileReference(t *testing.T) {
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("a bootstrap password!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The original spelling still works: an existing deployment must boot
	// unchanged now that the reference form sits beside it.
	b := BootstrapAdmin{Email: "a@b.co", DisplayName: "A", PasswordFile: pwFile}
	pw, err := b.ResolvePassword(config.Static(nil))
	if err != nil {
		t.Fatalf("ResolvePassword: %v", err)
	}
	if pw != "a bootstrap password!" {
		t.Fatalf("password = %q, want the file content without the trailing newline", pw)
	}

	if err := os.WriteFile(pwFile, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ResolvePassword(config.Static(nil)); err == nil {
		t.Fatal("an under-12-character bootstrap password was accepted")
	}
}

// The reference form, and which spelling wins when a deployment carries both.
func TestBootstrapAdminPasswordComesFromEitherSpelling(t *testing.T) {
	dir := t.TempDir()
	fromFile := filepath.Join(dir, "from-file")
	if err := os.WriteFile(fromFile, []byte("the file password!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := config.Static(map[string]string{"PROBE_ADMIN_PW": "the environment password!"})

	var withRef struct {
		B BootstrapAdmin `yaml:"b"`
	}
	doc := "b:\n  password: ${env:PROBE_ADMIN_PW}\n"
	if err := yaml.Unmarshal([]byte(doc), &withRef); err != nil {
		t.Fatal(err)
	}
	got, err := withRef.B.ResolvePassword(env)
	if err != nil {
		t.Fatalf("resolving the reference form: %v", err)
	}
	if got != "the environment password!" {
		t.Errorf("password = %q, want the environment value", got)
	}

	// Both present: the reference wins, because it is the form that can name
	// the environment and the one an operator migrating forward added on
	// purpose.
	withRef.B.PasswordFile = fromFile
	got, err = withRef.B.ResolvePassword(env)
	if err != nil {
		t.Fatal(err)
	}
	if got != "the environment password!" {
		t.Errorf("with both spellings the password was %q; the reference must win", got)
	}
}

// A deployment naming no password at all is told so, rather than bootstrapping
// an account with an empty credential.
func TestBootstrapAdminWithNoPasswordSourceRefuses(t *testing.T) {
	_, err := BootstrapAdmin{Email: "a@b.co", DisplayName: "A"}.ResolvePassword(config.Static(nil))
	if err == nil {
		t.Fatal("an admin with no password source was accepted")
	}
	if !strings.Contains(err.Error(), "${file:") || !strings.Contains(err.Error(), "${env:") {
		t.Errorf("the refusal does not show how to supply one: %v", err)
	}
}

// The documented `seeds.ai_routing` block must actually parse.
//
// It ships commented out, so TestShippedExampleConfigParses cannot see it — and
// a block nothing decodes is a block free to be wrong. It was: the field was
// declared `*yaml.Node`, and yaml.v3 special-cases a field only when its type
// is exactly `yaml.Node`. A pointer is dereferenced and decoded as an ordinary
// struct, so an operator who uncommented the shipped example got
// "field profile not found in type yaml.Node" and an api, worker and migrate
// that all refused to start.
//
// This uncomments what the example documents and decodes it, so the block a
// deployment is invited to copy is held to the parser every boot runs.
func TestTheDocumentedRoutingSeedBlockParses(t *testing.T) {
	raw, err := os.ReadFile("../../../../config/margince.example.yaml")
	if err != nil {
		t.Fatalf("reading the shipped example: %v", err)
	}
	block := commentedBlock(t, string(raw), "seeds:", "ai_routing:")

	cfg, err := Parse([]byte("version: 1\n" + block))
	if err != nil {
		t.Fatalf("the documented seeds.ai_routing block does not parse: %v\n%s", err, block)
	}
	if cfg.Seeds.AIRouting.IsZero() {
		t.Fatal("the block parsed but carried no binding; the seed would read as undeclared")
	}
	// The node has to survive being re-encoded, which is how compose hands it
	// to the ai parser. A field that decoded into the wrong destination
	// round-trips to something the router cannot read.
	out, err := yaml.Marshal(&cfg.Seeds.AIRouting)
	if err != nil {
		t.Fatalf("re-encoding the declared binding: %v", err)
	}
	for _, want := range []string{"profile:", "tiers:", "embeddings:"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the re-encoded binding lost %q:\n%s", want, out)
		}
	}
}

// commentedBlock lifts a commented-out YAML block out of the example and
// uncomments it, starting at the line whose comment body is start and running
// while the lines stay commented. It fails loudly rather than returning
// nothing: a block this cannot find is one the example no longer documents,
// which is a finding rather than a reason to pass.
func commentedBlock(t *testing.T, doc, start, contains string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "# "+start {
			continue
		}
		var out []string
		for _, l := range lines[i:] {
			trimmed := strings.TrimSpace(l)
			if trimmed == "" || !strings.HasPrefix(trimmed, "#") {
				break
			}
			// A bare "#" is a blank line inside the block; anything else loses
			// its "# " lead. Both spellings appear in the example.
			out = append(out, strings.TrimPrefix(strings.TrimSuffix(l, "#"), "# "))
		}
		block := strings.Join(out, "\n") + "\n"
		if strings.Contains(block, contains) {
			return block
		}
	}
	t.Fatalf("the example no longer documents a commented %s block containing %q", start, contains)
	return ""
}
