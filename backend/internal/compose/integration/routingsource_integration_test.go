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
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// Replacing the binding through the store, and a running Router picking that up
// without a restart — the loop increments 5 and 3 exist to close together.
//
// A write surface without the re-read would be worse than neither: the UI would
// confirm a change the process kept ignoring, which is a disagreement nobody
// can see. So the write and the adoption are proved in one test rather than two.
func TestAReplacedBindingReachesARunningRouterWithoutARestart(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()

	store := ai.NewRoutingStore(compose.NewSettingsStore(e.Pool), config.Static(nil))
	first, err := store.Replace(ctx, parsedRouting(t, "first"))
	if err != nil {
		t.Fatalf("storing the first binding: %v", err)
	}

	// A real ModelPath, built the way a boot builds one, so the watcher is
	// handed exactly what production hands it.
	path, err := compose.NewModelPath(ctx, first, e.Pool, false, discard())
	if err != nil {
		t.Fatalf("NewModelPath: %v", err)
	}
	router := path.Router()
	if router == nil {
		t.Fatal("the resolved path binds no router; the test can observe nothing")
	}
	watcher := compose.NewRoutingWatcher(e.Pool, &path, config.Static(nil), discard())

	// A tick against an unchanged binding must leave the Router alone —
	// otherwise every cached completion is dropped every interval.
	watcher.Recheck(ctx)
	if router.RoutingVersion() != first.RoutingVersion() {
		t.Fatalf("an unchanged binding moved the Router to %q", router.RoutingVersion())
	}

	second, err := store.Replace(ctx, parsedRouting(t, "second"))
	if err != nil {
		t.Fatalf("replacing the binding: %v", err)
	}
	if second.RoutingVersion() == first.RoutingVersion() {
		t.Fatal("the two bindings share a version; the test cannot tell adoption from inaction")
	}

	watcher.Recheck(ctx)
	if router.RoutingVersion() != second.RoutingVersion() {
		t.Errorf("the Router still serves %q after the binding was replaced with %q",
			router.RoutingVersion(), second.RoutingVersion())
	}
	if m, ok := router.CurrentModelForTier(ai.TierPremium); !ok || m.Model != "second" {
		t.Errorf("premium = %+v ok=%v; the replacement did not reach the bound models", m, ok)
	}
}

// A binding the store refuses never becomes what anything serves. The bar is
// the one the routing file was always held to, applied on the way in rather
// than discovered at the first model call.
func TestTheStoreRefusesABindingTheFileLoaderWouldHaveRefused(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.adminRoutingCtx()
	store := ai.NewRoutingStore(compose.NewSettingsStore(e.Pool), config.Static(nil))

	if _, err := store.Replace(ctx, ai.RoutingConfig{
		Profile: "nowhere",
		Tiers:   map[ai.Tier]ai.ProviderConfig{ai.TierPremium: {Provider: "fake", Model: "m"}},
	}); err == nil {
		t.Fatal("an unknown profile was stored; a bad binding must be refused on the way in")
	}
	stored, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stored.Unconfigured() {
		t.Errorf("the refused binding was stored anyway: %+v", stored.Tiers)
	}
}

// adminRoutingCtx is an admin holding the ai_routing grant the seeded role
// carries — read and update, no create or delete, which is what a setting has.
func (e *SearchEnv) adminRoutingCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"ai_routing": {Read: true, Update: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}

func parsedRouting(t *testing.T, model string) ai.RoutingConfig {
	t.Helper()
	cfg, err := ai.ParseRouting([]byte(`profile: eu_hosted
tiers:
  local_small: {provider: fake, model: ` + model + `}
  cheap_cloud: {provider: fake, model: ` + model + `}
  premium: {provider: fake, model: ` + model + `}
  frontier: {provider: fake, model: ` + model + `}
embeddings: {provider: fake, model: ` + model + `-embed, dimensions: 8}
`))
	if err != nil {
		t.Fatalf("ParseRouting: %v", err)
	}
	return cfg
}

// An UNCONFIGURED stored row is not the same as no row, and the difference is
// the whole reason the seed's answer is now read.
//
// `Unconfigured()` is `len(Tiers) == 0`, so a row can exist and still report
// unconfigured — which sends the boot down the adopt branch, where the seed's
// ON CONFLICT DO NOTHING then stores nothing because the row is already there.
// Announcing an adoption at that point names a binding the database does not
// hold. A concurrent second replica reaches the same state by racing; this
// fixture reaches it without needing one.
func TestAnUnconfiguredStoredRowIsNotAdoptedOverAndIsNotAnnouncedAsAdopted(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()

	// A stored row that binds nothing, written through the real seed path.
	if err := database.WithWorkspaceTx(e.adminRoutingCtx(), e.Pool, func(tx pgx.Tx) error {
		_, err := settings.SeedValue(ctx, tx, ai.Routing, ai.RoutingConfig{})
		return err
	}); err != nil {
		t.Fatalf("seeding the unconfigured row: %v", err)
	}

	var logged strings.Builder
	log := slog.New(slog.NewTextHandler(&logged, nil))
	got, err := compose.ResolveRouting(ctx, e.Pool, writeRouting(t, offlineRouting), config.Static(nil), log)
	if err != nil {
		t.Fatalf("ResolveRouting: %v", err)
	}

	// The stored row wins, so what comes back still binds nothing.
	if !got.Unconfigured() {
		t.Error("the file overwrote a stored row; ON CONFLICT DO NOTHING is what stops a restart replacing a binding an admin set")
	}
	// And the log does not claim otherwise. Asserting on the announcement, not
	// only on the value, is the point: a boot that stored nothing while saying
	// "adopted" sends an operator looking for a binding that is not there.
	if strings.Contains(logged.String(), "adopted the routing file") {
		t.Error("the boot announced an adoption it did not perform")
	}
	if !strings.Contains(logged.String(), "was not adopted") {
		t.Error("the boot stored nothing and said nothing about it")
	}
}
