// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The installation's own settings (ADR-0090/A135). Identity owns them because
// it owns the installation: it is the module that bootstraps the singleton
// organization and resolves it on every boot (ADR-0061 §3).
//
// These moved off columns on the `workspace` row. Two of them were never
// reachable by a human at all — an installation that mistyped its base
// currency or timezone in margince.yaml on day one had no way to correct it
// through the product, which is the gap ADR-0085 §7 names.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/settings"
)

// installationSettingsObject is the RBAC object gating the installation
// settings surface. Read is broad — a rep seeing amounts in the base currency
// benefits from knowing which one it is — and write is admin/ops.
const installationSettingsObject = "installation_settings"

// iso4217 is the shape of a currency code, matching the CHECK that guarded
// the column this replaces (0002: base_currency ~ '^[A-Z]{3}$').
var iso4217 = regexp.MustCompile(`^[A-Z]{3}$`)

// Name is the organization's display name. Seeded from margince.yaml at
// bootstrap; the row is authoritative afterwards, so renaming the
// organization does not require a redeployment.
var Name = settings.Define[string](
	"installation.name",
	installationSettingsObject,
	"update",
	"",
	func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("the organization needs a name")
		}
		return nil
	},
).AsInstallationIdentity()

// Timezone is the IANA reporting-period zone every period boundary is
// computed in. Distinct from a user's own timezone (app_user.timezone), which
// only affects how times are displayed to them.
var Timezone = settings.Define[string](
	"installation.timezone",
	installationSettingsObject,
	"update",
	"UTC",
	func(v string) error {
		// LoadLocation accepts two values that are not IANA zones and would
		// silently change what a reporting period means: "" resolves to UTC,
		// and "Local" resolves to whatever zone the SERVER happens to run in —
		// so the same installation would compute different period boundaries
		// on different hosts. Both are refused before the lookup.
		if v == "" || v == "Local" {
			return fmt.Errorf("%q is not an IANA zone name — use one like Europe/Berlin", v)
		}
		// Then validated by loading it: the tzdata the server actually has is
		// the only authority on whether a name resolves at report time. A name
		// that passes a regex and fails at midnight is worse than one refused
		// here.
		if _, err := time.LoadLocation(v); err != nil {
			return fmt.Errorf("%q is not an IANA timezone this server knows", v)
		}
		return nil
	},
).AsInstallationIdentity()

// BaseCurrency is the ISO-4217 currency every money roll-up converts to.
//
// It freezes once a deal has frozen a conversion rate against it (ADR-0085
// §7). Before that point it is freely changeable — which is the case this
// serves: an installation that chose wrong in its configuration on day one and
// noticed in week one. After it, changing the base would silently re-mean
// every historical roll-up.
//
// Identity declares the entry because it owns the installation, but it does
// NOT own the freeze predicate: what makes a conversion rate "frozen" is the
// deals module's business, and identity may not read its tables. Compose
// injects the probe, the way every cross-module edge is wired (ADR-0054).
// Until it does, this setting is changeable — which is why the injection is
// asserted by a fitness test rather than left to wiring discipline.
var BaseCurrency = settings.Define[string](
	"installation.base_currency",
	installationSettingsObject,
	"update",
	"EUR",
	func(v string) error {
		if !iso4217.MatchString(v) {
			return fmt.Errorf("a base currency is three uppercase ISO-4217 letters, like EUR")
		}
		return nil
	},
).AsInstallationIdentity()

// Definitions is identity's contribution to the settings registry.
func Definitions() []settings.Definition {
	return []settings.Definition{Name, Timezone, BaseCurrency}
}

// BaseCurrencyOf resolves the installation's reporting currency inside a
// transaction the caller already holds.
//
// It lives here because identity OWNS the setting: the modules that convert
// money may not import this package, so compose injects this function into
// them (ADR-0054) — but the one spelling of "how the base currency is read"
// belongs with the entry that declares it, not copied into each wiring site.
//
// RequireTx rather than Get: an absent row refuses instead of reading as the
// registered default, because every caller of this is converting or freezing
// money against the answer.
func BaseCurrencyOf(ctx context.Context, tx pgx.Tx) (string, error) {
	return settings.RequireTx(ctx, tx, BaseCurrency)
}

// TimezoneOf resolves the installation's IANA zone inside a transaction the
// caller already holds — the zone a "today" is computed in.
//
// RequireTx, like BaseCurrencyOf: a close-date sweep or a forecast cutoff that
// silently fell back to UTC would move real dates for an installation that
// runs in Europe/Berlin, and would move them by a day only sometimes, which is
// the hardest kind of wrong to notice.
func TimezoneOf(ctx context.Context, tx pgx.Tx) (string, error) {
	return settings.RequireTx(ctx, tx, Timezone)
}

// NameOf resolves the installation's display name inside a transaction the
// caller already holds.
//
// RequireTx here too, though the name is display rather than arithmetic: an
// offer snapshot names its issuer, and an offer that went out identifying the
// installation as "" is not better than one that refused to go out. The three
// installation-identity settings are seeded together at bootstrap, so a tree
// where one is unset has the other two unset as well.
func NameOf(ctx context.Context, tx pgx.Tx) (string, error) {
	return settings.RequireTx(ctx, tx, Name)
}
