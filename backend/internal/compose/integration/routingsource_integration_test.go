// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Where the model binding comes from, end to end.
//
// Four things need a real database, and none of them can be shown with a unit
// test because each is about what the INSTALLATION holds rather than what this
// process was handed:
//
//   - an installation with no stored binding adopts the routing file, and what
//     it runs on afterwards is the row rather than the file;
//   - once stored, the file is not read again — which is what lets an operator
//     delete it, and what stops a stale one quietly becoming the authority;
//   - a stored binding carries a routing VERSION, which is a cache key: read
//     back without one, every brief in the installation would fingerprint
//     against an empty string;
//   - an installation that binds nothing runs with its AI lanes absent rather
//     than failing the boot.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// offlineRouting is a complete binding on the fake provider, so these tests
// need no credential and no network.
const offlineRouting = `profile: eu_hosted
tiers:
  local_small: {provider: fake, model: fake-small}
  cheap_cloud: {provider: fake, model: fake-small}
  premium: {provider: fake, model: fake-large}
  frontier: {provider: fake, model: fake-large}
embeddings: {provider: fake, model: fake-embed, dimensions: 8}
`

func writeRouting(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ai-routing.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the routing file: %v", err)
	}
	return path
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestAnInstallationWithNoStoredBindingAdoptsTheRoutingFile(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	path := writeRouting(t, offlineRouting)

	adopted, err := compose.ResolveRouting(ctx, e.Pool, path, config.Static(nil), discard())
	if err != nil {
		t.Fatalf("ResolveRouting: %v", err)
	}
	if adopted.Unconfigured() {
		t.Fatal("the routing file was not adopted — the installation still binds nothing")
	}
	// The version is a cache key (personbrief.Fingerprint). A binding read back
	// without one would silently fingerprint every brief against "".
	if adopted.RoutingVersion() == "" {
		t.Error("the adopted binding carries no routing version")
	}

	// The FILE is now gone. A second resolve must still return the binding,
	// which is the property that lets an operator delete it — and the one that
	// proves the row, not the file, is what the Router runs on.
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing the routing file: %v", err)
	}
	stored, err := compose.ResolveRouting(ctx, e.Pool, path, config.Static(nil), discard())
	if err != nil {
		t.Fatalf("ResolveRouting after removing the file: %v", err)
	}
	if stored.Unconfigured() {
		t.Fatal("the stored binding vanished once the file was removed — the file is still the authority")
	}
	if stored.RoutingVersion() != adopted.RoutingVersion() {
		t.Errorf("routing version changed across a re-read: %s then %s", adopted.RoutingVersion(), stored.RoutingVersion())
	}
}

// The seed is consumed exactly once. A file left behind after an admin has
// changed the binding must not re-assert itself on the next restart — that is
// the whole reason the file is read only when nothing is stored.
func TestARestartDoesNotLetTheFileOverwriteAStoredBinding(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()

	first, err := compose.ResolveRouting(ctx, e.Pool, writeRouting(t, offlineRouting), config.Static(nil), discard())
	if err != nil {
		t.Fatalf("ResolveRouting: %v", err)
	}

	// A DIFFERENT file, as if the operator edited it after provisioning.
	edited := writeRouting(t, `profile: eu_hosted
tiers:
  local_small: {provider: fake, model: fake-small}
  cheap_cloud: {provider: fake, model: fake-small}
  premium: {provider: fake, model: fake-small}
  frontier: {provider: fake, model: fake-small}
embeddings: {provider: fake, model: fake-embed, dimensions: 8}
`)
	again, err := compose.ResolveRouting(ctx, e.Pool, edited, config.Static(nil), discard())
	if err != nil {
		t.Fatalf("ResolveRouting: %v", err)
	}
	if again.RoutingVersion() != first.RoutingVersion() {
		t.Error("an edited routing file overwrote the stored binding on restart; the stored value is supposed to be authoritative once it exists")
	}
}

// An installation that names no routing file and has nothing stored is not a
// boot error: its AI lanes are simply absent, exactly as before the move.
func TestAnInstallationThatBindsNothingResolvesUnconfigured(t *testing.T) {
	e := SetupSearch(t)

	cfg, err := compose.ResolveRouting(context.Background(), e.Pool, "", config.Static(nil), discard())
	if err != nil {
		t.Fatalf("ResolveRouting with no file and nothing stored: %v", err)
	}
	if !cfg.Unconfigured() {
		t.Errorf("an installation that bound nothing resolved to %+v, want unconfigured", cfg.Tiers)
	}
}

// A routing file that cannot be read fails the boot rather than falling back to
// unconfigured — the boot-level half of the unit test in cmd/api.
func TestAnUnreadableRoutingFileFailsTheBoot(t *testing.T) {
	e := SetupSearch(t)

	_, err := compose.ResolveRouting(context.Background(), e.Pool,
		filepath.Join(t.TempDir(), "does-not-exist.yaml"), config.Static(nil), discard())
	if err == nil {
		t.Fatal("a missing routing file resolved without error; a typo'd path would silently disable the AI lanes")
	}
}

// A stored binding is held to the same bar the file loader applies, so a write
// that reaches the row some other way cannot land something the boot would have
// refused. ai.FromStored is where that bar is applied on the way out.
func TestAStoredBindingIsValidatedOnTheWayOut(t *testing.T) {
	if _, err := ai.FromStored(ai.RoutingConfig{Profile: ai.ProfileEUHosted}, config.Static(nil)); err == nil {
		t.Error("a binding with no tiers finalized without error")
	}
}
