// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A BYOK key leaving the process environment, against a real database.
//
// Two things need one and neither can be shown with a unit test: that the ref
// is actually recorded (so the next boot resolves from the vault rather than
// sealing a second blob), and that sealing is idempotent — a boot that sealed
// nothing new must not rewrite the setting, or every restart strands another
// copy of the same secret in the vault.

import (
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func sealingEnv() config.Lookup {
	return config.Static(map[string]string{
		"GEMINI_API_KEY":    "a-gemini-key",
		"ANTHROPIC_API_KEY": "an-anthropic-key",
	})
}

func TestAnExportedKeyIsSealedOnceAndResolvedFromTheVaultAfterwards(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()
	vault := keyvault.NewMemory()
	ws := ids.From[ids.WorkspaceKind](e.WS)
	log := slog.New(slog.DiscardHandler)

	refs := compose.SealProviderKeys(ctx, e.Pool, vault, ws, sealingEnv(), log)
	if len(refs) != 2 {
		t.Fatalf("sealed %d credential(s), want the two the environment carries: %v", len(refs), refs)
	}

	// Recorded, not merely sealed. A ref held only in memory would leave the
	// next boot sealing the same secret again.
	stored, err := settings.Get(ctx, compose.NewSettingsStore(e.Pool), ai.ProviderKeys)
	if err != nil {
		t.Fatalf("reading the recorded refs: %v", err)
	}
	if stored["gemini"] != refs["gemini"] {
		t.Errorf("recorded %q, sealed %q", stored["gemini"], refs["gemini"])
	}

	// And what the router will actually resolve with reads back the secret.
	got := ai.SealedKeys(ctx, vault, ws, stored, config.Static(nil))("GEMINI_API_KEY")
	if got != "a-gemini-key" {
		t.Errorf("resolved %q from the vault, want the sealed key", got)
	}
}

// Idempotent across boots. Without this every restart seals another copy of the
// same secret and strands the previous one — inert, encrypted, and collected by
// nobody.
func TestASecondBootSealsNothingNew(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()
	vault := keyvault.NewMemory()
	ws := ids.From[ids.WorkspaceKind](e.WS)
	log := slog.New(slog.DiscardHandler)

	first := compose.SealProviderKeys(ctx, e.Pool, vault, ws, sealingEnv(), log)
	second := compose.SealProviderKeys(ctx, e.Pool, vault, ws, sealingEnv(), log)

	for provider, ref := range first {
		if second[provider] != ref {
			t.Errorf("%s was resealed: %q then %q — the previous blob is stranded", provider, ref, second[provider])
		}
	}
}

// An installation with no vault keeps resolving from the environment, which is
// where every installation was before this existed. Nothing is sealed and
// nothing is recorded.
func TestWithNoVaultNothingIsSealed(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()

	refs := compose.SealProviderKeys(ctx, e.Pool, nil, ids.From[ids.WorkspaceKind](e.WS), sealingEnv(), slog.New(slog.DiscardHandler))

	if len(refs) != 0 {
		t.Errorf("sealed %v without a vault to seal into", refs)
	}
}
