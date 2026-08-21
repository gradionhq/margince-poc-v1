// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// A declared binding that did NOT store must say so, and this is the one seed
// where silence is dangerous.
//
// `setting` is not workspace-scoped, so a bootstrap over a database still
// holding a previous installation's rows keeps the old binding — and ai.Routing
// is installation identity, so a data reset spares it too. The operator's file
// can say `sovereign`, meaning zero egress, while the installation serves a
// cloud vendor the previous installation chose. Nothing else in the boot would
// mention it.
//
// It lives in package compose because seedRoutingBinding is unexported and the
// claim is about what THAT writes, not about what a test can write beside it.

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
)

const seedDoc = `version: 1
seeds:
  ai_routing:
    profile: sovereign
    tiers:
      local_small: {provider: ollama, model: declared}
      cheap_cloud: {provider: ollama, model: declared}
      premium: {provider: ollama, model: declared}
      frontier: {provider: ollama, model: declared}
    embeddings: {provider: ollama, model: declared-embed, dimensions: 8}
`

func declaredFromFile(t *testing.T) yaml.Node {
	t.Helper()
	cfg, err := deployconfig.Parse([]byte(seedDoc))
	if err != nil {
		t.Fatalf("the declared binding does not parse: %v", err)
	}
	return cfg.Seeds.AIRouting
}

func TestADeclaredBindingSeedsAnInstallationThatHoldsNone(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	var discarded []string
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return seedRoutingBinding(ctx, tx, declaredFromFile(t), &discarded)
	}); err != nil {
		t.Fatalf("seeding the declared binding: %v", err)
	}
	if len(discarded) != 0 {
		t.Errorf("reported %v discarded on an installation that held nothing", discarded)
	}

	stored, err := settings.Get(ctx, NewSettingsStore(e.Pool), ai.Routing)
	if err != nil {
		t.Fatalf("reading the seeded binding: %v", err)
	}
	if m, ok := stored.Tiers[ai.TierPremium]; !ok || m.Model != "declared" {
		t.Errorf("premium = %+v ok=%v, want the declared binding", m, ok)
	}
}

// The dangerous case. A row already there means the declaration is refused by
// ON CONFLICT — correct, because the stored binding may be one an admin set
// through the API — but the operator must be told, or their file says sovereign
// while their text goes to whatever the surviving row names.
func TestADeclarationRefusedByAnExistingBindingIsReported(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	// A binding already stored, naming a DIFFERENT vendor from the declaration.
	surviving, err := ai.ParseRouting([]byte(`profile: eu_hosted
tiers:
  local_small: {provider: gemini, model: survivor}
  cheap_cloud: {provider: gemini, model: survivor}
  premium: {provider: gemini, model: survivor}
  frontier: {provider: gemini, model: survivor}
embeddings: {provider: gemini, model: survivor-embed, dimensions: 8}
`))
	if err != nil {
		t.Fatalf("building the surviving binding: %v", err)
	}
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := settings.SeedValue(ctx, tx, ai.Routing, surviving)
		return err
	}); err != nil {
		t.Fatalf("storing the surviving binding: %v", err)
	}

	var discarded []string
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return seedRoutingBinding(ctx, tx, declaredFromFile(t), &discarded)
	}); err != nil {
		t.Fatalf("seeding over an existing binding: %v", err)
	}

	if !strings.Contains(strings.Join(discarded, ","), ai.RoutingKey) {
		t.Errorf("discarded = %v; a declaration the database refused must be reported, or the file and the served vendor disagree in silence", discarded)
	}
	stored, err := settings.Get(ctx, NewSettingsStore(e.Pool), ai.Routing)
	if err != nil {
		t.Fatalf("reading the binding back: %v", err)
	}
	if m := stored.Tiers[ai.TierPremium]; m.Model != "survivor" {
		t.Errorf("premium = %+v; the declaration overwrote a stored binding", m)
	}
}
