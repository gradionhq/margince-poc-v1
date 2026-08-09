// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmdemo

// The DECLARATION test: what New() must hold for the six surfaces to be live at
// all. Everything here is checkable without a database, and each assertion
// exists because its absence is a failure that looks like success.

import (
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func TestDeclarationIsValid(t *testing.T) {
	e := New()
	if err := e.Name.Validate(); err != nil {
		t.Fatalf("the unit name is not one the tier admits: %v", err)
	}
	if got := string(e.Name); got != "crm-demo" {
		t.Fatalf("Name = %q — the declaration's name IS the extensions/<name> directory, and the composition panics when they disagree", got)
	}
	if err := e.Version.Validate(); err != nil {
		t.Fatalf("the version is not one the boot inventory can record: %v", err)
	}
	for _, tool := range e.Tools {
		if err := tool.Validate(); err != nil {
			t.Errorf("tool %q: %v", tool.Name, err)
		}
		if tool.Handle == nil {
			t.Errorf("tool %q carries no Handle — a nil handler is a manifest request that serves nothing, and this unit exists to be driven", tool.Name)
		}
	}
	for _, job := range e.Jobs {
		if err := job.Validate(); err != nil {
			t.Errorf("job %q: %v", job.Name, err)
		}
		if job.Handle == nil {
			t.Errorf("job %q carries no Handle — the tick is the one surface that runs with nobody watching, so an inert one is invisible", job.Name)
		}
	}
	for _, secret := range e.Secrets {
		if err := secret.Validate(); err != nil {
			t.Errorf("secret %q: %v", secret.Key, err)
		}
	}
}

// TestEveryToolTheContractDeclaresHasBehavior pins the join the composer makes
// at boot from the other side. The contract is the surface, so the fragment is
// authoritative about WHICH verbs exist; what this unit owes is a function for
// each. A verb the fragment declares with no entry here is legitimate in
// general (a governed request awaiting an operator) but not for the reference
// unit — the whole point is that all six surfaces are clickable.
func TestEveryToolTheContractDeclaresHasBehavior(t *testing.T) {
	// Spelled out rather than read back from api/crm.yaml: reading the fragment
	// would make this test agree with itself. These are the verbs a reviewer
	// reads in the contract, restated here so the two have to be reconciled by
	// hand when one changes.
	want := []string{
		"demo_add_note",
		"demo_list_notes",
		"demo_remove_note",
		"demo_sign_payload",
		"demo_signing_key_status",
		"demo_store_signing_key",
	}
	got := make([]string, 0, len(New().Tools))
	for _, tool := range New().Tools {
		got = append(got, tool.Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("served verbs = %v, want %v", got, want)
	}
}

// TestTheSecretDeclarationNamesTheKeyTheHandlersUse: the manifest tells an
// operator this unit expects a workspace-scoped `signing` key, and the handlers
// read one under a constant. A declaration naming a key nothing reads describes
// a secret that does not exist, and a handler reading a key nothing declared is
// a capability no operator was shown.
func TestTheSecretDeclarationNamesTheKeyTheHandlersUse(t *testing.T) {
	secrets := New().Secrets
	if len(secrets) != 1 {
		t.Fatalf("declared secrets = %v, want exactly the signing key", secrets)
	}
	if secrets[0].Key != signingKeyName {
		t.Errorf("the declaration names %q but the handlers read %q", secrets[0].Key, signingKeyName)
	}
	if secrets[0].Scope != extension.SecretScopeWorkspace {
		t.Errorf("scope = %q — the signing key is the installation's own credential, not a member's", secrets[0].Scope)
	}
}

// TestMigrationsAreEmbedded is the trap this unit is most able to fall into,
// pinned.
//
// check-ext-migrations and gen-composition's collision check both key off the
// ON-DISK migrations/ directory; cmd/migrate applies the SQL out of the
// EMBEDDED filesystem. A unit that shipped the directory and left Migrations
// nil would pass every gate green — the SQL reviewed, the catalog checked — and
// ext_crm_demo_note would never be created on any installation. Nothing else in
// the tree can see that gap, because every other check is looking at the
// directory that is definitely there.
func TestMigrationsAreEmbedded(t *testing.T) {
	layer := New().Migrations
	if layer == nil {
		t.Fatal("Migrations is nil — the migrations/ directory ships, every gate reads it, and cmd/migrate applies NOTHING")
	}
	entries, err := fs.ReadDir(layer, extension.MigrationsDir)
	if err != nil {
		t.Fatalf("the embedded filesystem holds no %s/ directory: %v", extension.MigrationsDir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if want := []string{
		"0001_note.down.sql", "0001_note.up.sql",
		"0002_note_kind.down.sql", "0002_note_kind.up.sql",
		"0003_note_workspace_index.down.sql", "0003_note_workspace_index.up.sql",
	}; !slices.Equal(names, want) {
		t.Fatalf("embedded migrations = %v, want %v — a pair missing from the EMBED is a pair nothing applies, however present it is on disk", names, want)
	}

	// The bytes, not just the names: `//go:embed migrations` on the wrong
	// directory, or a pair emptied by a bad merge, would satisfy the check
	// above. cmd/migrate refuses an empty layer, and this says why here.
	up, err := fs.ReadFile(layer, extension.MigrationsDir+"/0001_note.up.sql")
	if err != nil || len(up) == 0 {
		t.Fatalf("the embedded up migration is unreadable or empty: %v", err)
	}
	for _, must := range []string{
		"ext.ext_crm_demo_note",
		"FORCE ROW LEVEL SECURITY",
		"ENABLE ROW LEVEL SECURITY",
		"REFERENCES workspace(id) ON DELETE CASCADE",
	} {
		if !strings.Contains(string(up), must) {
			t.Errorf("the embedded migration does not carry %q — the catalog gate would refuse it", must)
		}
	}

	// The SECOND pair, by its own bytes. 0002 adds the `kind` column the
	// heartbeat marks and prunes its rows by, and a unit that shipped the file
	// without embedding it would run every query against a column that is not
	// there — which the migrate step would not notice, because it applies only
	// what the FS carries.
	kind, err := fs.ReadFile(layer, extension.MigrationsDir+"/0002_note_kind.up.sql")
	if err != nil || !strings.Contains(string(kind), "ADD COLUMN kind") {
		t.Fatalf("the embedded 0002 does not add the kind column: %v", err)
	}
}
