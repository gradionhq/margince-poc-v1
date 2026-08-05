// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package backendarch

// Every RBAC object must reach an EXISTING installation, not just a fresh one.
//
// The code-side seed (identity.seedSystemRoles) writes the role documents once
// at workspace creation and never re-syncs, so an object added to
// policy.coreObjects without a backfill migration is granted to nobody who
// bootstrapped earlier — it "works on a fresh database and 403s everywhere
// else", permanently. That is exactly how saved_view, webhook_subscription,
// relationship and partner reached production ungranted.
//
// The proof of the whole obligation is the replay in backend/migrations: seed
// the documents an old installation held, upgrade it to head, and assert the
// end state equals the matrix the server seeds today. That test executes the
// property instead of approximating it, which is why no list of objects, and
// no scan of the migration SQL for '{objects,<name>}', survives here.
//
// This file owns the replay's starting state. The cohort an old installation
// held is DERIVED from git rather than restated, because a hand-written cohort
// is a waiver in disguise: moving an object into it silently excuses that
// object from ever having to reach an existing installation, which is the
// defect the replay exists to catch. What IS pinned — and says so below — is
// the pair of coordinates that reach the derivation.

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	// legacyCommit is "Initial commit: WP0 foundation + WP1 core spine". Every
	// installation that predates every backfill started from this vocabulary.
	legacyCommit = "2cb50021"

	// legacyPolicyPath and legacyMigrationDir are where those files lived AT
	// legacyCommit; the tree has been restructured since (policyFile is the
	// declaration's home today, and the migrations moved under backend/).
	//
	// The distinction is the point: the object NAMES are derived, the
	// COORDINATES that reach them are pinned. A path cannot be recovered from a
	// tree it no longer appears in, so pinning it is not a shortcut — but it is
	// also not silently editable the way a name list is, because pointing it
	// anywhere else fails loudly below rather than yielding a smaller cohort.
	legacyPolicyPath   = "crm-auth/internal/policy/policy.go"
	legacyMigrationDir = "migrations/core"

	legacyInstallsFixture = "migrations/testdata/rbac_legacy_installs.json"
)

// installDocument is one role's permissions document as an old installation
// held it. Grants stay json.RawMessage: this gate owns which OBJECTS a cohort
// contains, never what they grant — the replay proves the grants by execution,
// and a second opinion on them here would only be a transcription to drift.
type installDocument struct {
	Objects  map[string]json.RawMessage `json:"objects"`
	RowScope string                     `json:"row_scope"`
}

// legacyInstalls is the committed fixture the replay seeds. Two installations,
// because one oldest-possible document is the worst case only while every
// backfill is an unconditional additive write — true of the SQL as written
// today, but an assumption about future migrations. A mid-life document that
// already holds half the vocabulary is where a conditionally-written backfill
// has somewhere to fail.
type legacyInstalls struct {
	LegacyCoreVersion string                                `json:"legacy_core_version"`
	Installs          map[string]map[string]installDocument `json:"installs"`
}

func TestLegacyInstallFixtureIsTheInitialCommitVocabulary(t *testing.T) {
	derived := legacyObjectsFromHistory(t)
	if len(derived) == 0 {
		t.Fatalf("derived an empty cohort from %s:%s. An empty cohort would make every object look "+
			"unbackfilled, or be skipped silently — it is never a legitimate result, so the "+
			"coordinates above have stopped resolving", legacyCommit, legacyPolicyPath)
	}

	fixture := readLegacyInstalls(t)
	install, ok := fixture.Installs["initial_commit"]
	if !ok {
		t.Fatalf("%s carries no 'initial_commit' installation; the replay has nothing to seed", legacyInstallsFixture)
	}

	for role, document := range install {
		held := slices.Sorted(maps.Keys(document.Objects))
		if !slices.Equal(held, derived) {
			t.Errorf("role %q in the initial_commit fixture holds objects %v, but %s:%s declares %v.\n"+
				"The fixture is the vocabulary an installation older than every backfill actually had; "+
				"an object may not be added to it to excuse writing that object's backfill migration.",
				role, held, legacyCommit, legacyPolicyPath, derived)
		}
	}
}

func TestLegacyInstallFixturePinsTheInitialCommitMigrationHead(t *testing.T) {
	derived := legacyCoreHeadFromHistory(t)
	fixture := readLegacyInstalls(t)
	if fixture.LegacyCoreVersion != derived {
		t.Errorf("%s pins legacy_core_version %q, but %s's newest core migration is %q.\n"+
			"The replay applies core up to and including this version before seeding, so a version "+
			"later than the initial commit's would silently run backfills the replay is meant to test.",
			legacyInstallsFixture, fixture.LegacyCoreVersion, legacyCommit, derived)
	}
}

// The mid-life installation is a pinned editorial choice, not a derived one:
// it models an installation that stopped upgrading partway. What must hold
// mechanically is that it stays genuinely mid-life — strictly larger than the
// initial-commit cohort and strictly smaller than today's vocabulary. If the
// vocabulary ever grows past it into equality, the conditional-backfill case
// has quietly become a duplicate of one of the other two.
func TestMidlifeInstallFixtureSitsBetweenTheInitialCohortAndHead(t *testing.T) {
	fixture := readLegacyInstalls(t)
	legacy := legacyObjectsFromHistory(t)
	head := coreObjectsFromSource(t)

	midlife, ok := fixture.Installs["midlife"]
	if !ok {
		t.Fatalf("%s carries no 'midlife' installation; a conditionally-written backfill would have "+
			"nowhere to fail", legacyInstallsFixture)
	}
	// A present-but-empty install would satisfy the lookup and then assert
	// nothing at all in the loop below — the mid-life case would be gone while
	// this gate reported success.
	if len(midlife) != len(fixture.Installs["initial_commit"]) {
		t.Fatalf("the midlife fixture carries %d roles and the initial_commit fixture %d; the replay "+
			"seeds both and compares every system role, so they must describe the same role set",
			len(midlife), len(fixture.Installs["initial_commit"]))
	}

	for role, document := range midlife {
		held := slices.Sorted(maps.Keys(document.Objects))
		for _, object := range legacy {
			if !slices.Contains(held, object) {
				t.Errorf("role %q in the midlife fixture is missing %q, which every installation held "+
					"from the initial commit; it is not a mid-life state", role, object)
			}
		}
		for _, object := range held {
			if !slices.Contains(head, object) {
				t.Errorf("role %q in the midlife fixture holds %q, which is no longer in "+
					"policy.coreObjects; drop the stale object", role, object)
			}
		}
		if len(held) <= len(legacy) {
			t.Errorf("role %q in the midlife fixture holds %d objects and the initial cohort is %d — "+
				"it has gained nothing since the initial commit, so it is a duplicate of that case "+
				"rather than a partial upgrade. Add the objects a mid-life installation would have held.",
				role, len(held), len(legacy))
		}
		if len(held) >= len(head) {
			t.Errorf("role %q in the midlife fixture holds %d objects and the vocabulary is now %d — "+
				"the mid-life case has caught up with head and no longer exercises a partial upgrade. "+
				"Add the newer objects it should already have held.", role, len(held), len(head))
		}
	}
}

// legacyObjectsFromHistory AST-parses coreObjects out of the declaration as it
// stood at legacyCommit. Reading the historical SOURCE rather than trusting a
// transcription is what makes the cohort underivable by hand.
func legacyObjectsFromHistory(t *testing.T) []string {
	t.Helper()
	source := gitShow(t, legacyPolicyPath)
	file, err := parser.ParseFile(token.NewFileSet(), legacyPolicyPath, source, 0)
	if err != nil {
		t.Fatalf("parsing %s:%s: %v", legacyCommit, legacyPolicyPath, err)
	}
	return slices.Sorted(slices.Values(coreObjectsIn(t, file)))
}

// legacyCoreHeadFromHistory returns the newest core migration version that
// existed at legacyCommit — the point the replay's prefix apply stops at.
func legacyCoreHeadFromHistory(t *testing.T) string {
	t.Helper()
	listing := gitLsTree(t, legacyMigrationDir)
	var versions []string
	for _, name := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
		base := filepath.Base(name)
		version, _, found := strings.Cut(base, "_")
		if !found || !strings.HasSuffix(base, ".up.sql") {
			continue
		}
		versions = append(versions, version)
	}
	if len(versions) == 0 {
		t.Fatalf("%s:%s lists no up migrations; the pinned migration directory has stopped resolving",
			legacyCommit, legacyMigrationDir)
	}
	slices.Sort(versions)
	return versions[len(versions)-1]
}

// gitShow reads one file as it stood at legacyCommit.
//
// It FAILS rather than skipping when history is unreachable. The backend unit
// lane (deterministic-gates) checks out at fetch-depth: 0 — the contract gate
// and the strict lint both diff against origin/main, so a shallow checkout
// would disarm them too. A gate that degraded to "no history, nothing to
// check" would look exactly like a passing one.
func gitShow(t *testing.T, path string) []byte {
	t.Helper()
	return runGit(t, "show", legacyCommit+":"+path)
}

func gitLsTree(t *testing.T, dir string) []byte {
	t.Helper()
	return runGit(t, "ls-tree", "-r", "--name-only", legacyCommit, "--", dir)
}

func runGit(t *testing.T, args ...string) []byte {
	t.Helper()
	// -C .. : the tests run from backend/, the repository root is one level up.
	out, err := exec.Command("git", append([]string{"-C", ".."}, args...)...).Output()
	if err != nil {
		var exit *exec.ExitError
		detail := err.Error()
		if errors.As(err, &exit) {
			detail = strings.TrimSpace(string(exit.Stderr))
		}
		t.Fatalf("git %s: %v\nThe cohort the migration replay seeds is derived from commit %s, so this "+
			"gate needs FULL history. If this is CI, the job running the backend unit lane checks out "+
			"shallow — give it `fetch-depth: 0`, as deterministic-gates already has. Deepen the "+
			"checkout; do not weaken the gate, because a cohort that silently shrinks makes every "+
			"object look as though it needs no backfill.",
			strings.Join(args, " "), detail, legacyCommit)
	}
	return out
}

func readLegacyInstalls(t *testing.T) legacyInstalls {
	t.Helper()
	raw, err := os.ReadFile(legacyInstallsFixture)
	if err != nil {
		t.Fatalf("reading %s: %v", legacyInstallsFixture, err)
	}
	var fixture legacyInstalls
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decoding %s: %v", legacyInstallsFixture, err)
	}
	if len(fixture.Installs) == 0 {
		t.Fatalf("%s declares no installations", legacyInstallsFixture)
	}
	return fixture
}
