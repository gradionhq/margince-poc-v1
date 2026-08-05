// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The migration replay gate (backend/migrations) proves that an installation
// predating every backfill, upgraded to head, holds exactly the matrix the
// server seeds today. It cannot ask policy for that matrix directly: the
// package sits behind identity/internal/, so Go's own import fence rejects it,
// and .go-arch-lint.yml independently lets `migrations` depend on `platform`
// alone. Widening either to serve a test would grant a production edge.
//
// This test is the bridge. It lives inside the fence, renders the seeded
// documents, and pins them to a fixture the replay reads. The fixture cannot
// drift away from the live values, because this comparison runs on every unit
// pass — which is the same two-lane shape the legacy-install fixture uses, for
// the same reason.

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"
)

// repoArtifact resolves a path relative to the backend module root. This
// package sits three levels down (internal/modules/identity), and the
// artifacts it owns — the replay fixture and the published matrix — belong to
// the repository, not to the module. The walk-up is forced: the golden files
// cannot live under identity, and identity cannot be moved out from behind its
// own fence.
func repoArtifact(name string) string {
	return filepath.Join("..", "..", "..", name)
}

var seededDefaultsFixture = repoArtifact(filepath.Join("migrations", "testdata", "rbac_seeded_defaults.json"))

// updateGolden rewrites every artifact this package owns. Shared with the
// rbac-matrix golden test — one flag, so a regeneration never leaves half the
// artifacts stale.
var updateGolden = flag.Bool("update", false,
	"rewrite the RBAC artifacts this package owns (the replay fixture and docs/reference/rbac-matrix.md)")

func TestSeededRBACRoleDocumentsMatchTheCommittedReplayFixture(t *testing.T) {
	documents := map[string]any{}
	for _, role := range systemRoles {
		var doc any
		if err := json.Unmarshal(policy.MustDefaultJSON(role.key), &doc); err != nil {
			t.Fatalf("decoding the seeded document for role %q: %v", role.key, err)
		}
		documents[role.key] = doc
	}

	rendered, err := json.MarshalIndent(documents, "", "  ")
	if err != nil {
		t.Fatalf("rendering the seeded documents: %v", err)
	}
	syncArtifact(t, seededDefaultsFixture, append(rendered, '\n'))
}

// syncArtifact compares a rendered artifact against its committed copy, or
// rewrites it under -update.
//
// The failure names the resolved path and the regeneration command on purpose:
// this package reads its artifacts through a relative walk-up, so a future
// package move surfaces here as a missing file, and the person doing the move
// needs to be told what to fix rather than handed a bare "no such file".
func syncArtifact(t *testing.T, path string, want []byte) {
	t.Helper()
	if *updateGolden {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("rewriting %s: %v", path, err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		absolute, resolveErr := filepath.Abs(path)
		if resolveErr != nil {
			absolute = path
		}
		t.Fatalf("reading the committed artifact %s (resolved to %s): %v\n"+
			"This test renders it from the seeded values and reaches it by walking up out of "+
			"internal/modules/identity. If this package has moved, fix the walk-up in repoArtifact. "+
			"Otherwise regenerate with: go test ./internal/modules/identity/ -run RBAC -update",
			path, absolute, err)
	}
	if string(got) == string(want) {
		return
	}
	t.Errorf("%s is stale — it no longer matches the values the server seeds.\n"+
		"Regenerate it with: go test ./internal/modules/identity/ -run RBAC -update\n"+
		"and commit the result together with the change that moved the matrix.\n%s",
		path, firstDifference(string(got), string(want)))
}

// firstDifference reports the first line where two renderings diverge. A full
// dump of two role matrices is unreadable in test output; the first divergent
// line is what a reader needs to see which cell moved.
func firstDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
		if gotLines[i] != wantLines[i] {
			return "first divergence at line " + strconv.Itoa(i+1) +
				":\n  committed: " + gotLines[i] + "\n  rendered:  " + wantLines[i]
		}
	}
	return "the committed artifact has " + strconv.Itoa(len(gotLines)) +
		" lines, the rendering has " + strconv.Itoa(len(wantLines))
}
