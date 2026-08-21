// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Moving a BYOK key out of the process environment and into the vault, once.
//
// An installation exporting GEMINI_API_KEY today needs to do nothing: the key
// is sealed on the next boot and the variable becomes how the key ARRIVED
// rather than where it lives. That is the same shape the routing file took when
// routing moved into the database, and for the same reason — a migration an
// operator has to perform is a migration half of them will not.
//
// What it does NOT do is remove the variable from their deployment. Nothing
// here can: the process cannot edit its own orchestrator. So the variable stays
// readable until an operator drops it, and the sentence this logs is what tells
// them they can.

import (
	"context"
	"log/slog"
	"maps"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SealProviderKeys stores any BYOK key this installation supplies in the
// environment but has not sealed yet, and returns the refs to resolve with.
//
// Returns the refs it knows about even when sealing something fails, so a
// vault that refuses one provider does not cost the installation the keys it
// already sealed for the others.
func SealProviderKeys(ctx context.Context, pool *pgxpool.Pool, vault keyvault.Vault, ws ids.WorkspaceID, env config.Lookup, log *slog.Logger) map[string]string {
	stored, err := settings.Get(ctx, NewSettingsStore(pool), ai.ProviderKeys)
	if err != nil {
		log.WarnContext(ctx, "cannot read the sealed provider credentials; falling back to the environment for this boot", "error", err)
		return nil
	}
	if vault == nil {
		// No vault configured. The environment is still the source, which is
		// exactly where every installation was before this existed.
		return stored
	}

	next := maps.Clone(stored)
	if next == nil {
		next = map[string]string{}
	}
	sealedNow := make([]string, 0, len(ai.CloudProvidersNeedingKeys()))
	for _, provider := range ai.CloudProvidersNeedingKeys() {
		if _, already := next[provider]; already {
			continue
		}
		secret := env(ai.KeyEnvVarFor(provider))
		if secret == "" {
			continue
		}
		ref, err := vault.Put(ctx, ws, []byte(secret))
		if err != nil {
			// One provider's failure is not the others'. The environment still
			// answers for this one, so the installation keeps working.
			log.ErrorContext(ctx, "cannot seal a provider credential; it stays in the environment for now",
				"provider", provider, "error", err)
			continue
		}
		next[provider] = string(ref)
		sealedNow = append(sealedNow, provider)
	}
	if len(sealedNow) == 0 {
		return stored
	}

	// Set, not SeedValue. Seeding is insert-only by design — it exists so a
	// restart never overwrites a value a human changed — and that is exactly
	// wrong here: the refs accumulate. An installation that sealed gemini last
	// boot and adds anthropic this one has a row already, so a seed would store
	// nothing, the new ref would never be recorded, and its blob would be
	// stranded in the vault while the environment silently kept answering.
	//
	// Overwriting is safe because this map is only ever GROWN: an existing
	// provider's ref is carried forward untouched a few lines above, so a Set
	// can add a key and can never drop or repoint one.
	if err := settings.Set(ctx, NewSettingsStore(pool), ai.ProviderKeys, next); err != nil {
		// The blobs are sealed and nothing references them — inert, encrypted
		// at rest, and collected by nobody. The installation keeps running on
		// the environment and the next boot tries again, which will strand
		// another. Loud because a stranded secret is not a non-event.
		log.ErrorContext(ctx, "sealed provider credentials could not be recorded; they are stranded in the vault and the environment is still the source",
			"providers", sealedNow, "error", err)
		return stored
	}
	// The one sentence that tells an operator they may now drop the variables.
	// Nothing here can remove them: the process cannot edit its orchestrator.
	log.InfoContext(ctx, "sealed provider credentials into the key vault; the environment variables that carried them can be removed",
		"providers", sealedNow)
	return next
}
