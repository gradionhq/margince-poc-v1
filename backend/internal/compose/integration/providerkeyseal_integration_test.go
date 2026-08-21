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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

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

// A provider added AFTER the first seal must be recorded too.
//
// The refs accumulate in one row, so an insert-only write stores nothing once
// that row exists: the second vendor's blob would be sealed, its ref dropped on
// the floor, and the environment would keep answering while a stranded secret
// sat in the vault. Nothing about the installation would look wrong.
func TestAProviderAddedAfterTheFirstSealIsRecorded(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()
	vault := keyvault.NewMemory()
	ws := ids.From[ids.WorkspaceKind](e.WS)
	log := slog.New(slog.DiscardHandler)

	first := compose.SealProviderKeys(ctx, e.Pool, vault, ws,
		config.Static(map[string]string{"GEMINI_API_KEY": "a-gemini-key"}), log)
	if _, ok := first["gemini"]; !ok {
		t.Fatalf("the first seal recorded nothing: %v", first)
	}
	if _, ok := first["anthropic"]; ok {
		t.Fatal("anthropic was sealed before its key existed")
	}

	// A second vendor arrives. The row already exists.
	second := compose.SealProviderKeys(ctx, e.Pool, vault, ws, sealingEnv(), log)

	if _, ok := second["anthropic"]; !ok {
		t.Errorf("anthropic was not recorded: %v — its key stays in the environment and its blob is stranded", second)
	}
	if second["gemini"] != first["gemini"] {
		t.Errorf("gemini was repointed: %q then %q; the map is only ever grown", first["gemini"], second["gemini"])
	}

	// And it survives the process: what the next boot reads must hold both.
	stored, err := settings.Get(ctx, compose.NewSettingsStore(e.Pool), ai.ProviderKeys)
	if err != nil {
		t.Fatalf("reading the recorded refs: %v", err)
	}
	for _, provider := range []string{"gemini", "anthropic"} {
		if stored[provider] != second[provider] {
			t.Errorf("%s recorded as %q, want %q", provider, stored[provider], second[provider])
		}
	}
}

// refusingVault seals everything except one nominated secret, so the test can
// fail exactly one provider and watch what happens to the other.
//
// A stub rather than the memory vault because the behaviour under test is a
// FAILURE, and the memory vault has no way to produce one. It delegates every
// other method so the parts that are not under test stay real.
type refusingVault struct {
	keyvault.Vault
	refuse string
}

func (v refusingVault) Put(ctx context.Context, ws ids.WorkspaceID, secret []byte) (keyvault.Ref, error) {
	if string(secret) == v.refuse {
		return "", errors.New("keyvault: the custodian refused this write")
	}
	return v.Vault.Put(ctx, ws, secret)
}

// One provider's vault failure is not the others'.
//
// The comment in SealProviderKeys says exactly this and nothing proved it. It
// matters because the alternative is silent and total: a boot that abandoned
// the whole seal on the first refusal would leave every provider on the
// environment, and the log line naming one vendor would be the only clue that
// the other three had not moved either.
func TestAProviderThatCannotBeSealedLeavesTheOthersSealed(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()
	ws := ids.From[ids.WorkspaceKind](e.WS)
	vault := refusingVault{Vault: keyvault.NewMemory(), refuse: "a-gemini-key"}

	refs := compose.SealProviderKeys(ctx, e.Pool, vault, ws, sealingEnv(), slog.New(slog.DiscardHandler))

	if _, sealed := refs["gemini"]; sealed {
		t.Error("gemini is recorded as sealed although the vault refused it; the environment is the only place its key still is")
	}
	if refs["anthropic"] == "" {
		t.Fatal("anthropic was not sealed, so one vendor's refusal cost the others their move out of the environment")
	}
	// Recorded, not just returned — the next boot must not re-seal anthropic.
	stored, err := settings.Get(ctx, compose.NewSettingsStore(e.Pool), ai.ProviderKeys)
	if err != nil {
		t.Fatalf("reading the recorded refs: %v", err)
	}
	if stored["anthropic"] != refs["anthropic"] {
		t.Errorf("recorded %q, sealed %q", stored["anthropic"], refs["anthropic"])
	}
	// And the refused one resolves from the environment, which is the whole
	// reason the failure is survivable.
	if got := ai.SealedKeys(ctx, vault, ws, stored, sealingEnv())("GEMINI_API_KEY"); got != "a-gemini-key" {
		t.Errorf("the refused provider resolves to %q, want its environment value", got)
	}
}

// A database that cannot answer at boot costs the installation its seal, not
// its boot. The environment still holds every key, so falling back to it is a
// working posture — and it is the posture every installation had before the
// vault existed.
func TestAnUnreadableSettingRowFallsBackToTheEnvironment(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()

	// A pool that is closed is how a database looks when it has gone away
	// mid-boot: every query fails immediately rather than hanging.
	dead, err := pgxpool.New(context.Background(), os.Getenv("MARGINCE_TEST_APP_DSN"))
	if err != nil {
		t.Fatalf("opening the pool to close: %v", err)
	}
	dead.Close()

	refs := compose.SealProviderKeys(ctx, dead, keyvault.NewMemory(),
		ids.From[ids.WorkspaceKind](e.WS), sealingEnv(), slog.New(slog.DiscardHandler))

	if refs != nil {
		t.Errorf("returned %v from a database that cannot be read; the caller would treat those as sealed", refs)
	}
}

// The seal is reached from routing resolution, not merely reachable.
//
// ResolveRouting → sealedKeys → SealProviderKeys is the chain a booting role
// actually walks, and every case above enters it one function too late by
// calling SealProviderKeys itself. That proves the seal works and proves
// nothing about whether routing asks for it — and an unasked seal fails OPEN:
// the router comes up, resolves every key from the environment exactly as
// before, and the credentials never move. Nothing looks wrong.
//
// So this one sets the root key the way a deployment does and starts at
// ResolveRouting.
func TestRoutingResolutionSealsTheKeysItResolvesWith(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()
	// The root key rides the SAME lookup as the provider keys, because that is
	// how sealedKeys reads it — in production both come from config.FromOS. A
	// t.Setenv here would set the process environment and leave the static
	// lookup this call actually consults without a vault, which is a green
	// test that exercises nothing.
	env := config.Static(map[string]string{
		"GEMINI_API_KEY":    "a-gemini-key",
		"ANTHROPIC_API_KEY": "an-anthropic-key",
		keyvault.EnvRootKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)),
	})

	// A stored binding, because sealedKeys is reached on the LAST line of
	// ResolveRouting — an installation with nothing bound returns before it,
	// and a test without this would pass while proving nothing.
	if err := settings.Set(ctx, compose.NewSettingsStore(e.Pool), ai.Routing, parsedRouting(t, "resolver-model")); err != nil {
		t.Fatalf("storing the binding to resolve: %v", err)
	}

	if _, err := compose.ResolveRouting(ctx, e.Pool, "", env, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("resolving routing with a vault configured: %v", err)
	}

	// The keys the environment carried are now sealed and recorded, which only
	// happens if routing resolution called the seal.
	stored, err := settings.Get(ctx, compose.NewSettingsStore(e.Pool), ai.ProviderKeys)
	if err != nil {
		t.Fatalf("reading the recorded refs: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("routing resolved with a vault configured and sealed nothing; the seal is not wired into the boot that was supposed to call it")
	}
}
