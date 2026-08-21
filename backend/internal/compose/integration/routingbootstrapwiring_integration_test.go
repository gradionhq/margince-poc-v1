// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The declared binding reaching the database through the REAL boot, not through
// a direct call to the function that writes it.
//
// The unit and package suites cover seedRoutingBinding and routingSeedFrom by
// calling them, which proves the decode and the refusals. What they cannot
// prove is that anything CALLS them: EnsureInstallation → configuredSeed →
// seedRoutingBinding is a chain of three, and an unwired seed fails OPEN — the
// bootstrap succeeds, the installation comes up, and the binding the operator
// declared is silently not there. Every one of those tests would still pass.
//
// So this one starts from a deployment document and an empty installation and
// asks the database what it holds afterwards.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

func TestADeclaredBindingReachesTheDatabaseThroughTheRealBootstrap(t *testing.T) {
	e := apptest.SetupApp(t)
	ctx := context.Background()
	// An empty installation: the harness has already provisioned one, and
	// bootstrap only runs when there is none to find.
	if _, err := e.Owner.Exec(ctx, `UPDATE workspace SET archived_at = now() WHERE archived_at IS NULL`); err != nil {
		t.Fatalf("emptying the installation: %v", err)
	}
	// password_file rather than the newer `password:` reference form, which
	// BootstrapAdmin.validate still refuses (issue filed separately) even
	// though ResolvePassword handles it and the field's own doc prefers it.
	pwFile := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(pwFile, []byte("a-long-enough-password"), 0o600); err != nil {
		t.Fatalf("writing the bootstrap password: %v", err)
	}

	// Parsed, never hand-built: Seeds.AIRouting is a yaml.Node, and a node
	// assembled in a test is a shape the deployment file cannot produce — which
	// is how the pointer-typed spelling of this field passed its tests while
	// refusing every real binding.
	cfg, err := deployconfig.Parse([]byte(`version: 1
organization:
  name: Bootstrap Wiring
bootstrap_admin:
  email: admin@wiring.test
  display_name: Wiring Admin
  password_file: ` + pwFile + `
seeds:
  ai_routing:
    profile: sovereign
    tiers:
      local_small: {provider: ollama, model: wiring-model}
      cheap_cloud: {provider: ollama, model: wiring-model}
      premium: {provider: ollama, model: wiring-model}
      frontier: {provider: ollama, model: wiring-model}
    embeddings: {provider: ollama, model: wiring-embed, dimensions: 8}
`))
	if err != nil {
		t.Fatalf("parsing the deployment file: %v", err)
	}

	if err := compose.EnsureInstallation(ctx, e.Pool, slog.New(slog.DiscardHandler), cfg); err != nil {
		t.Fatalf("bootstrapping with a declared binding: %v", err)
	}

	// config.Static(nil), never a nil Lookup. ResolveRouting hands the lookup
	// down to the key vault, which CALLS it — a nil one panics deep inside
	// keyvault with an address, several frames from the test that supplied it.
	// No production caller passes nil; this one did, and it stayed invisible
	// until a change downstream started dereferencing it.
	stored, err := compose.ResolveRouting(ctx, e.Pool, "", config.Static(nil), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("reading the binding the bootstrap stored: %v", err)
	}
	if stored.Unconfigured() {
		t.Fatal("the bootstrap left no binding; the deployment declared one, so seedRoutingBinding is not wired into the boot that was supposed to call it")
	}
	if got := stored.Tiers["local_small"].Model; got != "wiring-model" {
		t.Errorf("the stored binding names model %q, want the declared wiring-model", got)
	}
}
