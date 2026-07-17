// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package deployconfig loads the installation's deployment configuration
// file (`margince.yaml`, A107/ADR-0061). It carries bootstrap and
// authentication, and a small set of operator-posture runtime switches
// (e.g. ai.capture_payloads) that are deployment choices rather than
// secrets or per-request settings. Decoding is strict (an unknown key is a
// boot error, never a silent ignore) and secrets arrive only as `*_file`
// references (OPS-CFG-3): the file itself never carries a credential.
package deployconfig

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// Config is the root of margince.yaml. Every section beyond `version` is
// optional: a missing file (or one holding only `version: 1`) boots an
// already-bootstrapped installation; bootstrap of an empty database
// additionally requires `organization` and `bootstrap_admin`.
type Config struct {
	Version        int             `yaml:"version"`
	Organization   Organization    `yaml:"organization"`
	BootstrapAdmin *BootstrapAdmin `yaml:"bootstrap_admin"`
	Seeds          Seeds           `yaml:"seeds"`
	Auth           Auth            `yaml:"auth"`
	Email          Email           `yaml:"email"`
	AI             AIConfig        `yaml:"ai"`
}

// Organization names the installation's singleton organization. Consumed
// only when the organization is created; it never reconciles into an
// existing installation (§6.3 of the ratified concept).
type Organization struct {
	Name         string `yaml:"name"`
	BaseCurrency string `yaml:"base_currency"`
	Timezone     string `yaml:"timezone"`
}

// BootstrapAdmin identifies the first administrator. The password is a
// file reference so the secret can be deleted after first boot — once the
// organization exists this whole section may be removed.
type BootstrapAdmin struct {
	Email        string `yaml:"email"`
	DisplayName  string `yaml:"display_name"`
	PasswordFile string `yaml:"password_file"`
}

// Password reads the bootstrap admin's password from its file reference.
// Called only on the bootstrap path — an already-bootstrapped installation
// never needs (or reads) the secret.
func (b BootstrapAdmin) Password() (string, error) {
	raw, err := os.ReadFile(b.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("deployconfig: reading bootstrap_admin.password_file: %w", err)
	}
	pw := strings.TrimRight(string(raw), "\r\n")
	if len(pw) < 12 {
		return "", errors.New("deployconfig: bootstrap_admin password must be at least 12 characters")
	}
	return pw, nil
}

// Seeds externalizes the workspace defaults bootstrap previously seeded
// from code. Every key is optional — an omitted key seeds the built-in
// default, so a minimal file behaves exactly like the historical
// bootstrap. Values are consumed once, at organization creation.
type Seeds struct {
	Pipeline           *PipelineSeed    `yaml:"pipeline"`
	ConsentPurposes    []ConsentPurpose `yaml:"consent_purposes"`
	StarterAutomations *bool            `yaml:"starter_automations"`
	BookingPage        *bool            `yaml:"booking_page"`
}

// PipelineSeed configures the default pipeline's open stages. Won/Lost
// terminal stages are appended by the deals module — stage semantics are
// a module invariant, not an operator choice.
type PipelineSeed struct {
	Name   string          `yaml:"name"`
	Stages []PipelineStage `yaml:"stages"`
}

// PipelineStage is one configured open stage: display name + win
// probability.
type PipelineStage struct {
	Name        string `yaml:"name"`
	Probability int    `yaml:"probability"`
}

// ConsentPurpose seeds one row of the consent purpose catalog.
type ConsentPurpose struct {
	Key         string `yaml:"key"`
	Label       string `yaml:"label"`
	DoubleOptIn bool   `yaml:"double_opt_in"`
}

// Auth selects the enabled authentication methods. Password login
// defaults to enabled; OIDC arrives with its complete flow (ADR-0061 §6)
// and has no configuration surface until then — strict decoding makes a
// premature `oidc:` block a boot error rather than a silent no-op.
type Auth struct {
	Password PasswordAuth `yaml:"password"`
}

// PasswordAuth is the email+password method's switch.
type PasswordAuth struct {
	Enabled *bool `yaml:"enabled"`
}

// PasswordEnabled defaults to true: an installation without an `auth`
// section authenticates by email + password.
func (a Auth) PasswordEnabled() bool {
	return a.Password.Enabled == nil || *a.Password.Enabled
}

// Email configures the outbound transactional-email transport
// (A74/ADR-0056). Its first consumer is password-reset delivery; when
// disabled the forgot-password flow is absent rather than broken.
type Email struct {
	Enabled     bool   `yaml:"enabled"`
	SMTP        SMTP   `yaml:"smtp"`
	FromAddress string `yaml:"from_address"`
}

// SMTP names the operator's outbound relay; the credential arrives as a
// file reference.
type SMTP struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"password_file"`
}

// SMTPPassword reads the SMTP credential's file reference; empty when no
// reference is configured (an unauthenticated relay).
func (e Email) SMTPPassword() (string, error) {
	if e.SMTP.PasswordFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(e.SMTP.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("deployconfig: reading email.smtp.password_file: %w", err)
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

// AIConfig carries operator-posture switches for the AI runtime. It names
// no providers or models (that is ai-routing.yaml) and holds no secret —
// only deployment posture. capture_payloads turns on Layer-3 AI payload
// capture (ai_call_payload); OFF by default, because it stores
// special-category-adjacent content that then ages under the retention
// engine and the Art. 17 erasure cascade.
type AIConfig struct {
	CapturePayloads bool `yaml:"capture_payloads"`
}

// Load reads and strictly validates the configuration file. A missing
// file is not an error: it returns the zero configuration (version 1,
// all defaults), which boots an existing installation but cannot
// bootstrap an empty database.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator's own --config path; reading it is the function's purpose
	if errors.Is(err, os.ErrNotExist) {
		return Config{Version: 1}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("deployconfig: reading %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse strictly decodes and validates configuration bytes. Unknown
// fields, invalid values, and incompatible combinations are errors — a
// typo must never silently disable authentication.
func Parse(raw []byte) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("deployconfig: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("deployconfig: unsupported version %d (this build supports version 1)", c.Version)
	}
	if c.Organization.Timezone != "" {
		if _, err := values.ParseTimezone(c.Organization.Timezone); err != nil {
			return fmt.Errorf("deployconfig: organization.timezone: %w", err)
		}
	}
	if cur := c.Organization.BaseCurrency; cur != "" && !isCurrencyCode(cur) {
		return fmt.Errorf("deployconfig: organization.base_currency %q is not a 3-letter ISO 4217 code", cur)
	}
	if c.BootstrapAdmin != nil {
		if err := c.BootstrapAdmin.validate(); err != nil {
			return err
		}
	}
	if !c.Auth.PasswordEnabled() {
		// Fail closed (A107 §14): password login is the only implemented
		// method — disabling it would brick every human sign-in. The
		// switch becomes meaningful when OIDC ships its complete flow.
		return errors.New("deployconfig: auth.password.enabled=false would disable the only implemented login method — refused until another method (OIDC) exists")
	}
	if err := c.Seeds.validate(); err != nil {
		return err
	}
	if c.Email.Enabled {
		return c.Email.validate()
	}
	return nil
}

func (b BootstrapAdmin) validate() error {
	if _, err := values.ParseEmail(b.Email); err != nil {
		return fmt.Errorf("deployconfig: bootstrap_admin.email: %w", err)
	}
	if b.DisplayName == "" {
		return errors.New("deployconfig: bootstrap_admin.display_name is required")
	}
	if b.PasswordFile == "" {
		return errors.New("deployconfig: bootstrap_admin.password_file is required (secrets are file references, never inline values)")
	}
	return nil
}

func (e Email) validate() error {
	if e.SMTP.Host == "" {
		return errors.New("deployconfig: email.enabled requires email.smtp.host")
	}
	if e.SMTP.Port < 1 || e.SMTP.Port > 65535 {
		return errors.New("deployconfig: email.smtp.port must be between 1 and 65535")
	}
	if _, err := values.ParseEmail(e.FromAddress); err != nil {
		return fmt.Errorf("deployconfig: email.from_address: %w", err)
	}
	return nil
}

func (s Seeds) validate() error {
	if s.Pipeline != nil {
		if err := s.Pipeline.validate(); err != nil {
			return err
		}
	}
	seenKeys := map[string]bool{}
	for _, p := range s.ConsentPurposes {
		if p.Key == "" || p.Label == "" {
			return errors.New("deployconfig: seeds.consent_purposes entries need key and label")
		}
		if seenKeys[p.Key] {
			return fmt.Errorf("deployconfig: seeds.consent_purposes key %q is listed twice", p.Key)
		}
		seenKeys[p.Key] = true
	}
	return nil
}

func (p PipelineSeed) validate() error {
	if p.Name == "" {
		return errors.New("deployconfig: seeds.pipeline.name is required")
	}
	if len(p.Stages) == 0 {
		return errors.New("deployconfig: seeds.pipeline.stages must name at least one open stage")
	}
	seen := map[string]bool{}
	for _, st := range p.Stages {
		if st.Name == "" {
			return errors.New("deployconfig: seeds.pipeline.stages entries need a name")
		}
		if seen[st.Name] {
			return fmt.Errorf("deployconfig: seeds.pipeline stage %q is listed twice", st.Name)
		}
		seen[st.Name] = true
		if st.Probability < 0 || st.Probability > 100 {
			return fmt.Errorf("deployconfig: seeds.pipeline stage %q probability %d is outside 0–100", st.Name, st.Probability)
		}
	}
	return nil
}

// isCurrencyCode accepts the ISO 4217 shape (three ASCII uppercase
// letters); the currency's existence is the workspace table's concern.
func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
