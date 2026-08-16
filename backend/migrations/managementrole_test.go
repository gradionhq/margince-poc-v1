// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

// The obligation: the management document 0268 inserts into an upgraded
// installation is the document the server seeds into a fresh one. The
// migration carries a JSON literal because SQL cannot call
// policy.MustDefaultJSON; this test is what keeps that literal from becoming a
// second authority. rbac_seeded_defaults.json is itself held to
// MustDefaultJSON by identity's rbacfixture_test, so the chain is
// policy.defaults → fixture → migration, with a gate on each arrow.

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"testing"
)

const managementRoleMigration = "0268"

// managementLiteral finds the one JSON literal 0268 casts to jsonb.
var managementLiteral = regexp.MustCompile(`'(\{.*\})'::jsonb`)

func TestManagementRoleMigrationInsertsTheSeededDocument(t *testing.T) {
	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	var upSQL string
	for _, migration := range core.Migrations {
		if migration.Version == managementRoleMigration {
			upSQL = migration.UpSQL
		}
	}
	if upSQL == "" {
		t.Fatalf("core migration %s is missing", managementRoleMigration)
	}
	match := managementLiteral.FindStringSubmatch(upSQL)
	if match == nil {
		t.Fatalf("%s carries no '{...}'::jsonb literal; the management document has to be inserted from one", managementRoleMigration)
	}
	var inserted map[string]any
	if err := json.Unmarshal([]byte(match[1]), &inserted); err != nil {
		t.Fatalf("%s: the management literal is not valid JSON: %v", managementRoleMigration, err)
	}

	raw, err := os.ReadFile("testdata/rbac_seeded_defaults.json")
	if err != nil {
		t.Fatalf("reading the seeded-defaults fixture: %v", err)
	}
	var seeded map[string]map[string]any
	if err := json.Unmarshal(raw, &seeded); err != nil {
		t.Fatalf("decoding the seeded-defaults fixture: %v", err)
	}
	want, ok := seeded["management"]
	if !ok {
		t.Fatalf("the seeded-defaults fixture holds no management role; regenerate it with: go test ./internal/modules/identity/ -run RBAC -update")
	}
	if !reflect.DeepEqual(inserted, want) {
		t.Errorf("%s inserts a management document that differs from what the server seeds.\n"+
			"Regenerate the literal from testdata/rbac_seeded_defaults.json[\"management\"] "+
			"(compact JSON) and paste it into the migration; the two must stay one authority.",
			managementRoleMigration)
	}
}
