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
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/platform/config"
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
	License        License         `yaml:"license"`
	Email          Email           `yaml:"email"`
	AI             AIConfig        `yaml:"ai"`
	Rates          RatesConfig     `yaml:"rates"`
	MCP            MCP             `yaml:"mcp"`
	Capture        Capture         `yaml:"capture"`
	CompanyContext CompanyContext  `yaml:"company_context"`
	OverlayBudget  OverlayBudget   `yaml:"overlay_budget"`
	Operations     Operations      `yaml:"operations"`
	Uploads        Uploads         `yaml:"uploads"`
}

// Operations carries the ops kill switches (OPS-CFG-9): capabilities an
// installation turns on deliberately, in the file layer, where the deployment
// reviews them.
//
// Deliberately NOT `setting` rows. A setting is product configuration an
// installation admin changes through the API at runtime; what lives here is
// whether a destructive capability EXISTS for this deployment at all. An admin
// who could switch that on through the API could switch on the purge of their
// own tenant's data, which is precisely the authority the file layer is for.
type Operations struct {
	// AllowDataReset arms POST /v1/admin/reset-data, the two reset lanes that
	// serve it, and the /me flag that offers the action in the UI.
	//
	// The zero value is false, which is the whole design: this used to be
	// inferred from MARGINCE_ENV, so a deployment labelled `staging` — full of
	// real internal users — could have its data purged because a label said it
	// was not production. A capability that erases tenant data is stated, never
	// inferred from what the deployment happens to be called.
	AllowDataReset bool `yaml:"allow_data_reset"`
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

// Organization names the installation's singleton organization. Consumed
// only when the organization is created; it never reconciles into an
// existing installation (§6.3 of the ratified concept).
type Organization struct {
	Name         string `yaml:"name"`
	BaseCurrency string `yaml:"base_currency"`
	Timezone     string `yaml:"timezone"`
}

// BootstrapAdmin identifies the first administrator. The password is a
// reference so the secret can be deleted after first boot — once the
// organization exists this whole section may be removed.
type BootstrapAdmin struct {
	Email       string `yaml:"email"`
	DisplayName string `yaml:"display_name"`
	// Password is the reference form: ${file:...} or ${env:...}.
	Password Secret `yaml:"password"`
	// PasswordFile is the original spelling, still honoured so an existing
	// deployment boots unchanged. It is a bare path rather than a reference,
	// which is why it is not a Secret: it names a file and cannot name the
	// environment. Prefer `password`.
	PasswordFile string `yaml:"password_file"`
}

// ResolvePassword reads the bootstrap admin's password from whichever source
// the deployment named. Called only on the bootstrap path — an
// already-bootstrapped installation never needs (or reads) the secret.
//
// The reference wins when both are present, because it is the form that can
// name the environment and the one an operator migrating forward would have
// added deliberately.
func (b BootstrapAdmin) ResolvePassword(lookup config.Lookup) (string, error) {
	var pw string
	var err error
	switch {
	case b.Password.Configured():
		pw, err = b.Password.withField("bootstrap_admin.password").Resolve(lookup)
	case b.PasswordFile != "":
		pw, err = Secret{kind: secretFromFile, arg: b.PasswordFile, field: "bootstrap_admin.password_file"}.Resolve(lookup)
	default:
		return "", errors.New("deployconfig: bootstrap_admin names no password — set bootstrap_admin.password to ${file:/run/secrets/admin-password} or ${env:MARGINCE_ADMIN_PASSWORD}")
	}
	if err != nil {
		return "", err
	}
	if pw == "" {
		return "", b.passwordRef().Missing()
	}
	// The floor is the product's, not this file's: a bootstrap that planted a
	// password the change route would refuse would strand the account it
	// created.
	if len([]rune(pw)) < 12 {
		return "", errors.New("deployconfig: the bootstrap_admin password must be at least 12 characters")
	}
	return pw, nil
}

// passwordRef is whichever of the two spellings this deployment used, so a
// failure names the key the operator actually wrote.
func (b BootstrapAdmin) passwordRef() Secret {
	if b.Password.Configured() {
		return b.Password.withField("bootstrap_admin.password")
	}
	return Secret{kind: secretFromFile, arg: b.PasswordFile, field: "bootstrap_admin.password_file"}
}

// RetentionSeed selects the retention POSTURE a fresh installation is
// bootstrapped into (GCS-PARAM-7). It does not select the policy rows: those stay
// the data-model's pins (DM-SEED-1..6) under either posture, so an installation
// that must keep everything still SEES the ladder it is not running.
//
// The key exists because bootstrap plants the rows and the nightly pass can fire
// before an admin first logs in. A deployment under a contractual
// keep-everything obligation closes that window here rather than racing it in the
// UI.
type RetentionSeed struct {
	// DefaultPolicy is `standard` or `retain_only`. Empty means standard, which
	// is the historical behaviour byte for byte.
	DefaultPolicy string `yaml:"default_policy"`
}

// The two postures seeds.retention.default_policy admits.
const (
	// RetentionStandardPosture plants DM-SEED-1..6 with destruction enabled —
	// storage limitation (Art. 5(1)(e)) as the out-of-the-box posture.
	RetentionStandardPosture = "standard"
	// RetentionRetainOnlyPosture plants the same rows and turns the retain-only
	// posture on, so nothing is anonymized or erased however over-age it becomes.
	RetentionRetainOnlyPosture = "retain_only"
)

// RetainOnly reports whether the configured posture suppresses every destructive
// retention action. An absent block is the standard posture, so a minimal file
// behaves exactly like the historical bootstrap.
func (s Seeds) RetainOnly() bool {
	return s.Retention != nil && s.Retention.DefaultPolicy == RetentionRetainOnlyPosture
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
// reference, never a value.
type SMTP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	// Password is the reference form: ${file:...} or ${env:...}.
	Password Secret `yaml:"password"`
	// PasswordFile is the original spelling, still honoured so an existing
	// deployment boots unchanged. Prefer `password`.
	PasswordFile string `yaml:"password_file"`
}

// SMTPPassword resolves the SMTP credential; empty when none is configured,
// which is an unauthenticated relay rather than a mistake.
func (e Email) SMTPPassword(lookup config.Lookup) (string, error) {
	switch {
	case e.SMTP.Password.Configured():
		return e.SMTP.Password.withField("email.smtp.password").Resolve(lookup)
	case e.SMTP.PasswordFile != "":
		return Secret{kind: secretFromFile, arg: e.SMTP.PasswordFile, field: "email.smtp.password_file"}.Resolve(lookup)
	default:
		return "", nil
	}
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

// MCP is the connector's deployment gate (Gate 1, DESIGN §5.5). Off by
// default: turning it on exposes an internet-facing authorization server
// plus the agent tool surface, so it is an explicit operator decision.
type MCP struct {
	ConnectorEnabled bool `yaml:"connector_enabled"`
}

// Parse strictly decodes and validates a single configuration document.
// Unknown fields, invalid values, and incompatible combinations are errors — a
// typo must never silently disable authentication.
//
// One document: what a running process reads is the base file plus its
// posture's overlay, which is Load in layers.go.
func Parse(raw []byte) (Config, error) {
	cfg := Config{Version: 1}
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
	if cur := c.Organization.BaseCurrency; cur != "" && !values.ValidCurrency(cur) {
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
	if err := c.Uploads.validate(); err != nil {
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
	if s.Retention != nil {
		if err := s.Retention.validate(); err != nil {
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

// validate refuses an unrecognized posture loudly. A typo here would otherwise
// fall back to `standard` in silence — which for the one deployment that set the
// key on purpose is the exact failure it was set to prevent.
func (r RetentionSeed) validate() error {
	switch r.DefaultPolicy {
	case "", RetentionStandardPosture, RetentionRetainOnlyPosture:
		return nil
	default:
		return fmt.Errorf("deployconfig: seeds.retention.default_policy %q is not %s or %s",
			r.DefaultPolicy, RetentionStandardPosture, RetentionRetainOnlyPosture)
	}
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
