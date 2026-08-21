// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Where a provider's BYOK key lives.
//
// It was the process environment, which is the 12-factor answer and has one
// property nobody chose: an environment variable is readable for the life of
// the process by anything that can read /proc, by a crash dump, and by every
// child process the server ever forks. A key an installation holds for years
// sits in all of those the whole time.
//
// Sealed in the key vault it is at rest under AEAD, addressed by an opaque ref,
// and reachable only by a process holding the vault's root key. The root key
// still comes from the environment, so the environment does not leave the
// picture — it holds ONE secret instead of one per vendor, which is the actual
// improvement.
//
// The environment stays the way a key ARRIVES. An installation exporting
// GEMINI_API_KEY today keeps working and needs no action: the key is sealed on
// the next boot and the variable becomes a seed rather than the source. That is
// the same shape the routing file took when routing moved into the database.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// providerKeysObject is the RBAC object gating the sealed credentials.
//
// The same object routing carries, deliberately: both are admin/ops-only on
// both verbs, and no seeded role holds one without the other. A second object
// would be a distinction the role matrix does not make — and every object costs
// a backfill migration that has to reach installations that already exist.
const providerKeysObject = routingSettingsObject

// ProviderKeysKey is the settings key the vault refs are stored under.
const ProviderKeysKey = "ai.provider_keys"

// ProviderKeys maps a provider name to the vault ref holding its BYOK key.
//
// The REF is stored, never the key. A ref is opaque and workspace-bound — the
// vault refuses one presented under another workspace — so a reader who can
// see this setting learns which vendors have a credential and nothing about
// any of them.
var ProviderKeys = settings.Define[map[string]string](
	ProviderKeysKey,
	providerKeysObject,
	"update",
	map[string]string{},
	validateProviderKeyRefs,
).AsInstallationIdentity()

// validateProviderKeyRefs refuses a ref for a provider this build cannot serve.
// A key sealed against a name nothing routes is a credential nobody can use and
// nobody will remember is there.
func validateProviderKeyRefs(refs map[string]string) error {
	for provider, ref := range refs {
		if _, cloud := cloudKeyEnv[provider]; !cloud {
			return fmt.Errorf("ai: %q takes no api key", provider)
		}
		if ref == "" {
			return fmt.Errorf("ai: %s has an empty credential reference", provider)
		}
	}
	return nil
}

// KeyDefinitions is the ai module's credential contribution to the settings
// registry, kept beside Definitions so a module declares its catalog in one
// place.
func keyDefinitions() []settings.Definition {
	return []settings.Definition{ProviderKeys}
}

// SealedKeys resolves BYOK keys from the vault, falling back to the environment
// for one that has not been sealed yet.
//
// It satisfies config.Lookup so nothing downstream changes: buildClients still
// asks for a variable NAME, cloudKey still resolves it, and byokKeyRequired
// still names the variable to set when there is no key at all. Only where the
// bytes come from moved.
//
// The environment fallback is what makes the move invisible to a running
// installation. It is also what the seeder uses to fill the vault, so an
// operator who exported a key once never exports it again.
func SealedKeys(ctx context.Context, vault keyvault.Vault, ws ids.WorkspaceID, refs map[string]string, env config.Lookup) config.Lookup {
	byName := make(map[string]string, len(cloudKeyEnv))
	for provider, name := range cloudKeyEnv {
		byName[name] = provider
	}
	return func(name string) string {
		provider, cloud := byName[name]
		if !cloud || vault == nil {
			return envOrEmpty(env, name)
		}
		ref, sealed := refs[provider]
		if !sealed {
			return envOrEmpty(env, name)
		}
		secret, err := vault.Get(ctx, ws, keyvault.Ref(ref))
		if err != nil {
			// Falling back rather than failing: a vault that cannot answer
			// leaves the installation exactly where it was before the key was
			// sealed, and the caller's own byokKeyRequired says what is missing
			// if the environment cannot answer either. Refusing here would turn
			// an unreadable vault into "this provider does not work" with no
			// sentence naming the vault.
			return envOrEmpty(env, name)
		}
		return string(secret)
	}
}

func envOrEmpty(env config.Lookup, name string) string {
	if env == nil {
		return ""
	}
	return env(name)
}

// CloudProvidersNeedingKeys names the providers whose credential this module
// holds, in a stable order so a log line reads the same on every boot.
//
// Derived from cloudKeyEnv rather than restated, so a provider added there is
// covered by the sealer the day it appears — the alternative is a second list
// that silently leaves a new vendor's key in the environment.
func CloudProvidersNeedingKeys() []string {
	out := make([]string, 0, len(cloudKeyEnv))
	for _, provider := range knownProviders {
		if _, cloud := cloudKeyEnv[provider]; cloud {
			out = append(out, provider)
		}
	}
	return out
}

// KeyEnvVarFor is the variable a provider's key arrives in, or "" for one that
// takes no key. The names match each vendor's own convention, which is why they
// carry no MARGINCE_ prefix.
func KeyEnvVarFor(provider string) string { return cloudKeyEnv[provider] }
