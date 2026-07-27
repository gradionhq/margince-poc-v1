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
	Rates          RatesConfig     `yaml:"rates"`
	Capture        Capture         `yaml:"capture"`
	CompanyContext CompanyContext  `yaml:"company_context"`
	OverlayBudget  OverlayBudget   `yaml:"overlay_budget"`
}

// CompanyContextRollout is the ordered deployment capability for company
// knowledge. The empty YAML value resolves to onboarding so an upgrade keeps
// today's behavior until an operator deliberately stages it backward.
type CompanyContextRollout string

const (
	// CompanyContextOff disables context reads, task injection, and onboarding.
	CompanyContextOff CompanyContextRollout = "off"
	// CompanyContextRead enables the canonical read model and settings surface.
	CompanyContextRead CompanyContextRollout = "read"
	// CompanyContextTasks additionally enables declared AI task injection.
	CompanyContextTasks CompanyContextRollout = "tasks"
	// CompanyContextOnboarding additionally enables the first-run experience.
	CompanyContextOnboarding CompanyContextRollout = "onboarding"
)

// CompanyContext configures the operator-controlled company-context rollout.
type CompanyContext struct {
	Rollout CompanyContextRollout `yaml:"rollout"`
}

// EffectiveRollout applies the compiled-in default without mutating the
// decoded configuration.
func (c CompanyContext) EffectiveRollout() CompanyContextRollout {
	if c.Rollout == "" {
		return CompanyContextOnboarding
	}
	return c.Rollout
}

// ReadEnabled reports whether typed reads, refresh, and settings are active.
func (c CompanyContext) ReadEnabled() bool {
	stage := c.EffectiveRollout()
	return stage == CompanyContextRead || stage == CompanyContextTasks || stage == CompanyContextOnboarding
}

// TasksEnabled reports whether declared model tasks may receive company data.
func (c CompanyContext) TasksEnabled() bool {
	stage := c.EffectiveRollout()
	return stage == CompanyContextTasks || stage == CompanyContextOnboarding
}

// OnboardingEnabled reports whether the five-step first-run surface is active.
func (c CompanyContext) OnboardingEnabled() bool {
	return c.EffectiveRollout() == CompanyContextOnboarding
}

// Capture is the deployment's mail-capture pipeline tuning (ADR-0063).
type Capture struct {
	// FreemailExtra appends deployment-specific consumer mail domains to
	// the pinned baseline blocklist (CAP-PARAM-5): mail from these domains
	// still creates the person, never a company.
	FreemailExtra []string `yaml:"freemail_extra"`
	// TransactionalExtra appends deployment-specific mail-infrastructure
	// eSLDs to the pinned baseline (CAP-PARAM-6, ADR-0072): mail from these
	// senders keeps the activity but derives no counterparty at all.
	TransactionalExtra []string `yaml:"transactional_extra"`
	// TransactionalNever is the operator allowlist of registrable domains
	// that must never be suppressed as transactional (CAP-PARAM-6) — it wins
	// over every baseline/prefix rule.
	TransactionalNever []string `yaml:"transactional_never"`
}

// OverlayBudget is the per-incumbent OVB consumption-meter configuration
// (overlay-budget chapter): keyed by incumbent name (e.g. "hubspot"), each
// entry pins that incumbent's two windows' ceilings/caps and the shared
// warn/shed fractions. An incumbent with no entry falls back to the
// built-in default (defaultIncumbentBudgets) so an installation that never
// wrote the block still meters HubSpot safely rather than fail-closing
// every force-fresh read. The numeric values are unpinned upstream (the
// corpus leaves per-incumbent calibration open, OVB Parameters appendix);
// these are this build's conservative defaults, tunable here.
type OverlayBudget map[string]IncumbentBudget

// IncumbentBudget is one incumbent's meter config: a per-second Search
// window and a daily REST window, each a ceiling/cap pair (the cap the
// conservative distance below the published ceiling we share with
// integrations we cannot see — OVB-PARAM-3), plus the warn/shed fractions
// of the cap (OVB-PARAM-1/2) shared by both windows.
type IncumbentBudget struct {
	// Search is the per-second search-API window (fixed 1s bucket).
	Search WindowBudget `yaml:"search"`
	// REST is the daily REST-allocation window — a fixed UTC-day bucket
	// (resets at UTC midnight), the meter's "fixed-window counters with
	// expiry" mechanism, not a sliding 24h window.
	REST WindowBudget `yaml:"rest"`
	// WarnFraction/ShedFraction are the consumed/cap ratios at which a
	// charge answers warn then shed (OVB-PARAM-1/2). Zero means "use the
	// spec default" (0.70 / 0.90) — resolved by Effective, not stored as 0.
	WarnFraction float64 `yaml:"warn_fraction"`
	ShedFraction float64 `yaml:"shed_fraction"`
}

// WindowBudget is one rolling window's published ceiling and our own
// (lower) cap. Consumption is metered against the cap; the ceiling is the
// incumbent's published rate limit the cap must stay strictly below.
type WindowBudget struct {
	Ceiling int `yaml:"ceiling"`
	Cap     int `yaml:"cap"`
}

// Default warn/shed fractions when an incumbent leaves them unset
// (OVB-PARAM-1/2 defaults).
const (
	defaultOverlayWarnFraction = 0.70
	defaultOverlayShedFraction = 0.90
)

// defaultIncumbentBudgets is the built-in per-incumbent OVB configuration
// used for any incumbent the YAML does not override. HubSpot: the Search
// API's per-second ceiling (~5 req/s) with a conservative 4 req/s cap
// (AC-overlay-budget-1's "x / 4.0 req/s"), and a rolling-24h REST
// allocation (the ~100k/day org ceiling) capped conservatively below it.
func defaultIncumbentBudgets() OverlayBudget {
	return OverlayBudget{
		"hubspot": {
			Search:       WindowBudget{Ceiling: 5, Cap: 4},
			REST:         WindowBudget{Ceiling: 100000, Cap: 90000},
			WarnFraction: defaultOverlayWarnFraction,
			ShedFraction: defaultOverlayShedFraction,
		},
	}
}

// EffectiveOverlayBudget merges the operator's overlay_budget block over
// the built-in defaults: a configured incumbent wins entirely, an absent
// one gets its default, and an entry that leaves warn/shed at zero gets
// the spec-default fractions. This is the map the meter is constructed
// from — validate() has already rejected any unsafe explicit values.
func (c Config) EffectiveOverlayBudget() OverlayBudget {
	out := defaultIncumbentBudgets()
	for name, ib := range c.OverlayBudget {
		if ib.WarnFraction == 0 {
			ib.WarnFraction = defaultOverlayWarnFraction
		}
		if ib.ShedFraction == 0 {
			ib.ShedFraction = defaultOverlayShedFraction
		}
		out[name] = ib
	}
	return out
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
	if err := c.Rates.validate(); err != nil {
		return err
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
	switch c.CompanyContext.EffectiveRollout() {
	case CompanyContextOff, CompanyContextRead, CompanyContextTasks, CompanyContextOnboarding:
	default:
		return fmt.Errorf("deployconfig: company_context.rollout %q is not off, read, tasks, or onboarding", c.CompanyContext.Rollout)
	}
	for name, ib := range c.OverlayBudget {
		if err := ib.validate(name); err != nil {
			return err
		}
	}
	if c.Email.Enabled {
		return c.Email.validate()
	}
	return nil
}

// validate rejects an unsafe OVB config at load (OVB-AC-4): each window's
// cap must be strictly below its published ceiling (OVB-PARAM-3), and the
// warn/shed fractions must be ordered inside (0,1] (OVB-PARAM-4) — a
// misconfiguration fails fast at boot rather than silently overrunning the
// shared incumbent quota at runtime. Zero warn/shed is allowed here (it
// means "use the default", resolved by EffectiveOverlayBudget); a non-zero
// value must be in range.
func (ib IncumbentBudget) validate(name string) error {
	for _, w := range []struct {
		label  string
		window WindowBudget
	}{{"search", ib.Search}, {"rest", ib.REST}} {
		if w.window.Ceiling <= 0 || w.window.Cap <= 0 {
			return fmt.Errorf("deployconfig: overlay_budget.%s.%s ceiling and cap must both be positive (got ceiling=%d cap=%d)", name, w.label, w.window.Ceiling, w.window.Cap)
		}
		if w.window.Cap >= w.window.Ceiling {
			return fmt.Errorf("deployconfig: overlay_budget.%s.%s cap %d must be strictly below the published ceiling %d (OVB-PARAM-3)", name, w.label, w.window.Cap, w.window.Ceiling)
		}
	}
	warn, shed := ib.WarnFraction, ib.ShedFraction
	if warn == 0 {
		warn = defaultOverlayWarnFraction
	}
	if shed == 0 {
		shed = defaultOverlayShedFraction
	}
	if !(warn > 0 && warn < shed && shed <= 1) {
		return fmt.Errorf("deployconfig: overlay_budget.%s fractions must satisfy 0 < warn < shed <= 1 (got warn=%g shed=%g, OVB-PARAM-4)", name, warn, shed)
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
