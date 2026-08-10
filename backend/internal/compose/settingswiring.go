// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The settings catalog is assembled HERE, the way every other cross-module
// edge is (ADR-0054): each module declares the settings it owns, compose
// concatenates them into the one registry every store reads. platform/settings
// owns the mechanism and knows no domain; the modules own the meaning.

import (
	"reflect"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
)

// settingsDefinitions is the whole catalog, in one list. A module that owns no
// setting contributes nothing — there is no interface every module must
// satisfy, because most of them have no settings and never will.
//
// This function is also the fitness gate's input: settingscatalog_test.go
// asserts uniqueness and module-prefix ownership over exactly this list, so a
// setting that is declared but never registered fails the same way a
// misprefixed one does.
var settingsDefinitions = sync.OnceValue(func() []settings.Definition {
	// The one cross-module edge in the catalog: identity owns
	// installation.base_currency because it owns the installation, but the
	// predicate that freezes it — a deal having stamped a conversion rate —
	// is the deals module's business, and identity may not read that table.
	// Injected here, once, the way ADR-0054 requires; settingscatalog_test.go
	// asserts it actually happened, because an unwired probe fails OPEN (the
	// setting stays changeable) rather than loudly.
	identity.BaseCurrency.WithFreeze(deals.BaseCurrencyFrozen)

	var defs []settings.Definition
	defs = append(defs, capture.Definitions()...)
	defs = append(defs, identity.Definitions()...)
	return defs
})

// NewSettingsStore builds a store over the pool and the assembled catalog.
// Exported because it is genuinely part of the composition surface: the
// integration suites in compose/integration assemble the same stores the
// server does, and a second catalog built by hand there would drift from this
// one the moment a module declares a setting.
//
// The Store is a stateless view — pool plus the static registry — so building
// one per consumer costs nothing and avoids threading a shared instance
// through every job constructor.
func NewSettingsStore(pool *pgxpool.Pool) *settings.Store {
	return settings.New(pool, settingsRegistry())
}

// settingsRegistry is the assembled catalog, built once. The data reset needs
// it without a pool — it runs inside a transaction the caller already holds.
var settingsRegistry = sync.OnceValue(func() *settings.Registry {
	return settings.NewRegistry(settingsDefinitions()...)
})

// SettingSpec is the fitness gate's view of one registered setting: plain
// data, so the root-package gates can judge the catalog without importing
// platform (arch-lint grants root only contracts + compose — the same
// constraint NewTaskCensus is built for).
type SettingSpec struct {
	Key        string
	Object     string
	AuditVerb  string
	DefaultErr error
	HasFreeze  bool
}

// SettingsCatalogForTest flattens the assembled catalog for
// settingscatalog_test.go. Nothing in the product calls it.
func SettingsCatalogForTest() []SettingSpec {
	defs := settingsDefinitions()
	out := make([]SettingSpec, 0, len(defs))
	for _, d := range defs {
		_, err := d.DefaultJSON()
		out = append(out, SettingSpec{
			Key: d.Key(), Object: d.Object(), AuditVerb: d.AuditVerb(), DefaultErr: err,
			HasFreeze: d.HasFreezeProbe(),
		})
	}
	return out
}

// DeploymentConfigKeysForTest returns every dotted yaml path in the
// deployment-config schema, so the surface-disjointness gate compares against
// the STRUCT rather than the illustrative example file — which omits whole
// sections and would let the check pass vacuously.
func DeploymentConfigKeysForTest() map[string]bool {
	return yamlPaths(reflect.TypeOf(deployconfig.Config{}), "")
}

// yamlPaths walks a config struct and returns every dotted yaml path in it,
// so the collision check tracks the schema rather than a hand-kept list.
func yamlPaths(t reflect.Type, prefix string) map[string]bool {
	// Unwrap pointers, and slices/arrays too: a config section can be a list
	// of structs (seeds.consent_purposes), and stopping at the slice would
	// leave its fields out of the collision set — a gate that is a subset of
	// the real config surface silently misses exactly the nested keys a
	// setting is most likely to shadow.
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	out := map[string]bool{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		out[path] = true
		for k, v := range yamlPaths(f.Type, path) {
			out[k] = v
		}
	}
	return out
}

// DealsInstallation is the installation seam the deals module reads through.
//
// Named HERE because it is a cross-module edge: deals may not import identity
// (ADR-0054), so compose is where the two meet. identity owns the readers
// themselves — it owns the settings they resolve.
func DealsInstallation() deals.Installation {
	return deals.Installation{
		Name:         identity.NameOf,
		BaseCurrency: identity.BaseCurrencyOf,
		Timezone:     identity.TimezoneOf,
	}
}
