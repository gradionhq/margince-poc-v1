// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// layered writes a base and (when non-empty) an overlay for the posture, and
// loads them the way a boot does.
func layered(t *testing.T, env runtimeenv.Environment, base, overlay string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "margince.yaml")
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatalf("writing the base: %v", err)
	}
	if overlay != "" {
		if err := os.WriteFile(OverlayPath(path, env), []byte(overlay), 0o600); err != nil {
			t.Fatalf("writing the overlay: %v", err)
		}
	}
	return Load(path, env)
}

func TestTheOverlayForThisPostureWinsKeyByKey(t *testing.T) {
	cfg, err := layered(t, runtimeenv.Development,
		"version: 1\norganization:\n  name: Acme\n  timezone: Europe/Berlin\nmcp:\n  connector_enabled: false\n",
		"mcp:\n  connector_enabled: true\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MCP.ConnectorEnabled {
		t.Error("the overlay set mcp.connector_enabled: true and the base's false survived")
	}
	// The keys the overlay is silent about are the reason for having a base at
	// all: an overlay states a difference, not a whole configuration.
	if cfg.Organization.Name != "Acme" || cfg.Organization.Timezone != "Europe/Berlin" {
		t.Errorf("the base's organization did not survive the overlay: %+v", cfg.Organization)
	}
}

// A posture that arms nothing must not inherit another posture's arming — the
// file is selected by name, so only the running posture's overlay is read.
func TestOnlyTheRunningPosturesOverlayIsRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "margince.yaml")
	if err := os.WriteFile(path, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatalf("writing the base: %v", err)
	}
	if err := os.WriteFile(OverlayPath(path, runtimeenv.Development),
		[]byte("operations:\n  allow_data_reset: true\n"), 0o600); err != nil {
		t.Fatalf("writing the dev overlay: %v", err)
	}
	prod, err := Load(path, runtimeenv.Production)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if prod.Operations.AllowDataReset {
		t.Fatal("production read the dev overlay and armed the tenant-data purge")
	}
	dev, err := Load(path, runtimeenv.Development)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !dev.Operations.AllowDataReset {
		t.Error("the dev overlay did not arm the reset for the posture it names")
	}
}

// The merge rule an operator reads off the YAML in front of them: a mapping
// merges, a list replaces. Both halves matter — a list that merged would make
// "which currencies does this installation propose" unanswerable without
// reading two files and knowing which way each key goes.
func TestAMappingMergesAndAListReplaces(t *testing.T) {
	cfg, err := layered(t, runtimeenv.Test,
		"version: 1\nrates:\n  fx_currencies: [USD, GBP, CHF]\n  model_pricing:\n    gemini: https://base.test/gemini\n    openai: https://base.test/openai\n",
		"rates:\n  fx_currencies: [SEK]\n  model_pricing:\n    openai: https://overlay.test/openai\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Rates.FxCurrencies; len(got) != 1 || got[0] != "SEK" {
		t.Errorf("fx_currencies = %v; a list the overlay names replaces the base's entirely", got)
	}
	want := map[string]string{"gemini": "https://base.test/gemini", "openai": "https://overlay.test/openai"}
	for k, v := range want {
		if cfg.Rates.ModelPricing[k] != v {
			t.Errorf("model_pricing[%s] = %q, want %q", k, cfg.Rates.ModelPricing[k], v)
		}
	}
	if len(cfg.Rates.ModelPricing) != len(want) {
		t.Errorf("model_pricing = %v; a mapping merges, so the base's untouched keys stay", cfg.Rates.ModelPricing)
	}
}

// An overlay committed as a placeholder — the posture exists, its keys are
// still commented out — is a file a boot must read and shrug at.
func TestACommentOnlyOverlayIsNotAFailure(t *testing.T) {
	cfg, err := layered(t, runtimeenv.Development,
		"version: 1\norganization:\n  name: Acme\n",
		"# nothing to change for dev yet\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Organization.Name != "Acme" {
		t.Errorf("organization.name = %q, want Acme", cfg.Organization.Name)
	}
}

// Strictness is per layer, not just for the base: an overlay is where a typo is
// LIKELIER, because it is the file edited per posture and the one with no
// example to copy from.
func TestATypoInTheOverlayFailsTheBootAndNamesTheFile(t *testing.T) {
	_, err := layered(t, runtimeenv.Development,
		"version: 1\n",
		"mcp:\n  conector_enabled: true\n")
	if err == nil {
		t.Fatal("a misspelled key in the overlay booted; a typo must never silently do nothing")
	}
	if !strings.Contains(err.Error(), "margince.dev.yaml") {
		t.Errorf("error = %q; it must name which of the two files holds the typo", err)
	}
}

// Validation runs over the MERGED result, never per file. An overlay that
// completes what the base only starts is the ordinary way a posture supplies
// its own admin, and validating the base alone would refuse it.
func TestValidationRunsOverTheMergedResult(t *testing.T) {
	cfg, err := layered(t, runtimeenv.Test,
		"version: 1\norganization:\n  name: Acme\nbootstrap_admin:\n  email: admin@acme.test\n  display_name: Admin\n  password_file: /run/secrets/base-admin\n",
		"bootstrap_admin:\n  password_file: /run/secrets/test-admin\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BootstrapAdmin == nil {
		t.Fatal("bootstrap_admin vanished across the merge")
	}
	if cfg.BootstrapAdmin.Email != "admin@acme.test" {
		t.Errorf("email = %q; the base's value survived an overlay that only replaced the password reference", cfg.BootstrapAdmin.Email)
	}
}

func TestOverlayPathInsertsThePostureBeforeTheExtension(t *testing.T) {
	for _, tc := range []struct {
		base string
		env  runtimeenv.Environment
		want string
	}{
		{"config/margince.yaml", runtimeenv.Development, "config/margince.dev.yaml"},
		{"config/margince.yaml", runtimeenv.Test, "config/margince.test.yaml"},
		{"config/margince.yaml", runtimeenv.Production, "config/margince.production.yaml"},
		{"/etc/margince/deploy.yml", runtimeenv.Production, "/etc/margince/deploy.production.yml"},
	} {
		if got := OverlayPath(tc.base, tc.env); got != tc.want {
			t.Errorf("OverlayPath(%q, %q) = %q, want %q", tc.base, tc.env, got, tc.want)
		}
	}
}

// Neither file is required. This is how an already-bootstrapped installation
// runs: the ADR permits deleting the file once the organization exists.
func TestNeitherLayerHasToExist(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), runtimeenv.Development)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("version = %d; the compiled default is the first layer", cfg.Version)
	}
}

// The dev overlay is tracked and every `make dev` reads it, so a typo in it
// breaks every engineer's stack at once — and it has no example beside it to
// diff against, unlike the base.
//
// Staged under the names the dev stack uses, because that is what the overlay
// derivation keys off: `make dev` copies the example to config/margince.yaml,
// which is what makes config/margince.dev.yaml its overlay. Loading the example
// under its own name would silently test nothing.
func TestShippedDevOverlayArmsTheResetForDevOnly(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "margince.yaml")
	copyRepoFile(t, "../../../../config/margince.example.yaml", base)
	copyRepoFile(t, "../../../../config/margince.dev.yaml", OverlayPath(base, runtimeenv.Development))

	dev, err := Load(base, runtimeenv.Development)
	if err != nil {
		t.Fatalf("config/margince.dev.yaml no longer parses over the base: %v", err)
	}
	if !dev.Operations.AllowDataReset {
		t.Error("the dev overlay must arm operations.allow_data_reset — a dev stack without the Reset data button is a silent regression")
	}
	// The same base at any other posture arms nothing. This is the property the
	// heredoc that used to append the switch could not have: it wrote into the
	// base, so the arming was not posture-scoped at all.
	prod, err := Load(base, runtimeenv.Production)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if prod.Operations.AllowDataReset {
		t.Fatal("the shipped example arms the tenant-data purge in production")
	}
}

func copyRepoFile(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("reading %s: %v", from, err)
	}
	if err := os.WriteFile(to, raw, 0o600); err != nil {
		t.Fatalf("writing %s: %v", to, err)
	}
}
