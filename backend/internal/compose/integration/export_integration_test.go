// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The open-format export bundle writer (B-E11.10a, features/04 §5):
// completeness + open-format validity of the CSV-per-object + relational
// JSON dump + files manifest + audit_log, and — the headline security
// property — that the bundle is a row-scoped read: a team-scoped caller's
// export excludes every record their lists would hide. An unscoped export
// would be a data breach, so that exclusion is a pinned test.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// exportReadGrants grants read on every object the bundle members gate on
// — the shared searchReadGrants omits relationship, which the export
// exercises, so this suite carries its own.
func exportReadGrants() map[string]principal.ObjectGrant {
	grants := map[string]principal.ObjectGrant{}
	for _, object := range []string{"person", "organization", "deal", "lead", "activity", "relationship"} {
		grants[object] = principal.ObjectGrant{Read: true}
	}
	return grants
}

func (e *SearchEnv) exportAdmin() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: exportReadGrants(), RowScope: principal.RowScopeAll},
	})
}

func (e *SearchEnv) exportRep(user, team ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs:     []ids.UUID{team},
		Permissions: principal.Permissions{Objects: exportReadGrants(), RowScope: principal.RowScopeTeam},
	})
}

// exportFixture is the two-tenant-of-one-workspace seed: a rep1 (team1)
// slice and a rep3 (team2) slice, so a team-scoped caller must see its
// own and none of the other's.
type exportFixture struct {
	rep1Person, rep3Person ids.UUID
	rep1Org, rep3Org       ids.UUID
	rep1Deal, rep3Deal     ids.UUID
	rep1Lead, rep3Lead     ids.UUID
	rep1Activity           ids.UUID
	rep3Activity           ids.UUID
}

func (e *SearchEnv) seedExportFixture(t *testing.T) exportFixture {
	t.Helper()
	pipelineID := e.SeedID(t, `INSERT INTO pipeline (id, name, is_default, position) VALUES ($1, 'Sales', true, 0)`)
	stageID := e.SeedID(t, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability) VALUES ($1, $2, 'Qualify', 0, 'open', 10)`, pipelineID)

	var f exportFixture
	// rep1 (team1) carries a social row to prove the child relation
	// travels with its parent.
	f.rep1Person = e.SeedID(t, `INSERT INTO person (id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Rep1 Person', 'manual', 'human:x')`, e.Rep1)
	e.SeedID(t, `INSERT INTO person_social (id, person_id, platform, handle)
		VALUES ($1, $2, 'linkedin', 'in/rep1')`, f.rep1Person)
	f.rep3Person = e.SeedID(t, `INSERT INTO person (id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Rep3 Person', 'manual', 'human:x')`, e.Rep3)
	f.rep1Org = e.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, source, captured_by)
		VALUES ($1, $2, 'Rep1 Org', 'manual', 'human:x')`, e.Rep1)
	f.rep3Org = e.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, source, captured_by)
		VALUES ($1, $2, 'Rep3 Org', 'manual', 'human:x')`, e.Rep3)
	f.rep1Deal = e.SeedID(t, `INSERT INTO deal (id, owner_id, name, pipeline_id, stage_id, organization_id, amount_minor, currency, source, captured_by)
		VALUES ($1, $2, 'Rep1 Deal', $3, $4, $5, 100000, 'EUR', 'manual', 'human:x')`, e.Rep1, pipelineID, stageID, f.rep1Org)
	f.rep3Deal = e.SeedID(t, `INSERT INTO deal (id, owner_id, name, pipeline_id, stage_id, organization_id, amount_minor, currency, source, captured_by)
		VALUES ($1, $2, 'Rep3 Deal', $3, $4, $5, 200000, 'EUR', 'manual', 'human:x')`, e.Rep3, pipelineID, stageID, f.rep3Org)
	f.rep1Lead = e.SeedID(t, `INSERT INTO lead (id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Rep1 Lead', 'manual', 'human:x')`, e.Rep1)
	f.rep3Lead = e.SeedID(t, `INSERT INTO lead (id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Rep3 Lead', 'manual', 'human:x')`, e.Rep3)

	// Employment edges: each connects a rep's person to that rep's org, so
	// the whole edge is visible only to that rep (both endpoints owned).
	e.SeedID(t, `INSERT INTO relationship (id, kind, person_id, organization_id, source, captured_by)
		VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, f.rep1Person, f.rep1Org)
	e.SeedID(t, `INSERT INTO relationship (id, kind, person_id, organization_id, source, captured_by)
		VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, f.rep3Person, f.rep3Org)

	// Activities scope through their links.
	f.rep1Activity = e.SeedID(t, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'note', 'Rep1 note', now(), 'manual', 'human:x')`)
	e.SeedID(t, `INSERT INTO activity_link (id, activity_id, entity_type, person_id) VALUES ($1, $2, 'person', $3)`, f.rep1Activity, f.rep1Person)
	f.rep3Activity = e.SeedID(t, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'note', 'Rep3 note', now(), 'manual', 'human:x')`)
	e.SeedID(t, `INSERT INTO activity_link (id, activity_id, entity_type, person_id) VALUES ($1, $2, 'person', $3)`, f.rep3Activity, f.rep3Person)

	// Attachments on each rep's person — the files manifest source.
	e.SeedID(t, `INSERT INTO attachment (id, entity_type, entity_id, filename, storage_key, source, captured_by)
		VALUES ($1, 'person', $2, 'rep1.pdf', 'blob/rep1', 'manual', 'human:x')`, f.rep1Person)
	e.SeedID(t, `INSERT INTO attachment (id, entity_type, entity_id, filename, storage_key, source, captured_by)
		VALUES ($1, 'person', $2, 'rep3.pdf', 'blob/rep3', 'manual', 'human:x')`, f.rep3Person)

	// Audit rows targeting each rep's person (audit_log is record-mutations-only).
	e.SeedID(t, `INSERT INTO audit_log (id, actor_type, actor_id, action, entity_type, entity_id)
		VALUES ($1, 'human', $2, 'create', 'person', $3)`, "human:"+e.Rep1.String(), f.rep1Person)
	e.SeedID(t, `INSERT INTO audit_log (id, actor_type, actor_id, action, entity_type, entity_id)
		VALUES ($1, 'human', $2, 'create', 'person', $3)`, "human:"+e.Rep3.String(), f.rep3Person)
	return f
}

func TestExportBundleCompleteAndValidOpenFormat(t *testing.T) {
	e := SetupSearch(t)
	f := e.seedExportFixture(t)

	var buf bytes.Buffer
	summary, err := compose.NewExportWriter(e.Pool).WriteBundle(e.exportAdmin(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	entries := BundleEntries(t, buf.Bytes())

	// Every member CSV, the relational dump, the files manifest, and the
	// bundle manifest are present.
	for _, name := range []string{
		"person.csv", "organization.csv", "deal.csv", "lead.csv", "activity.csv",
		"relationship.csv", "pipeline.csv", "stage.csv", "attachment.csv", "audit_log.csv",
		"data.json", "files-manifest.json", "manifest.json",
	} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("bundle is missing %s; got %v", name, keys(entries))
		}
	}

	// The admin (row_scope=all) sees both reps' rows — completeness.
	if got := len(CSVColumn(t, entries["person.csv"], "id")); got != 2 {
		t.Fatalf("person.csv has %d rows, want 2 (both reps)", got)
	}
	if summary.RowCounts["deal"] != 2 || summary.RowCounts["relationship"] != 2 {
		t.Fatalf("summary counts wrong: %+v", summary.RowCounts)
	}

	// The relational JSON dump validates and nests every object; the
	// jsonb column round-trips as a nested object, never base64.
	var dump struct {
		Format  string                      `json:"format"`
		Objects map[string][]map[string]any `json:"objects"`
	}
	if err := json.Unmarshal(entries["data.json"], &dump); err != nil {
		t.Fatalf("data.json is not valid JSON: %v", err)
	}
	if dump.Format == "" || len(dump.Objects["person"]) != 2 {
		t.Fatalf("data.json dump incomplete: format=%q persons=%d", dump.Format, len(dump.Objects["person"]))
	}
	var handle any
	for _, s := range dump.Objects["person_social"] {
		if s["platform"] == "linkedin" {
			handle = s["handle"]
		}
	}
	if handle != "in/rep1" {
		t.Fatalf("the person_social child relation did not travel with its parent: handle=%v", handle)
	}

	// The files manifest lists both attachments.
	var manifest struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(entries["files-manifest.json"], &manifest); err != nil {
		t.Fatalf("files-manifest.json invalid: %v", err)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("files manifest has %d files, want 2", len(manifest.Files))
	}
	_ = f
}

// The pinned security property: a team-scoped caller's export contains
// exactly what that caller may read — across every scoped member (records,
// edges, activities, files, audit). Contacts, accounts, deals and leads are
// readable by every seat, so the other team's deal and lead ARE in the
// bundle; what stays out is the other rep's capture-private contact and
// account, and everything reachable only through them.
func TestExportRowScopeExcludesInvisibleRecords(t *testing.T) {
	e := SetupSearch(t)
	f := e.seedExportFixture(t)
	for _, private := range []struct {
		table string
		id    ids.UUID
	}{
		{"person", f.rep3Person}, {"organization", f.rep3Org},
	} {
		if _, err := e.Owner.Exec(context.Background(),
			`UPDATE `+private.table+` SET visibility = 'owner' WHERE id = $1`, private.id); err != nil {
			t.Fatalf("making the other rep's %s capture-private: %v", private.table, err)
		}
	}

	var buf bytes.Buffer
	summary, err := compose.NewExportWriter(e.Pool).WriteBundle(e.exportRep(e.Rep1, e.Team1), &buf)
	if err != nil {
		t.Fatal(err)
	}
	entries := BundleEntries(t, buf.Bytes())

	assertOnlyID := func(file string, want, hidden ids.UUID) {
		rowIDs := CSVColumn(t, entries[file], "id")
		set := map[string]bool{}
		for _, id := range rowIDs {
			set[id] = true
		}
		if !set[want.String()] {
			t.Fatalf("%s dropped the caller's own row %s: got %v", file, want, rowIDs)
		}
		if set[hidden.String()] {
			t.Fatalf("%s LEAKED an invisible row %s: got %v", file, hidden, rowIDs)
		}
	}
	assertBothIDs := func(file string, mine, theirs ids.UUID) {
		rowIDs := CSVColumn(t, entries[file], "id")
		set := map[string]bool{}
		for _, id := range rowIDs {
			set[id] = true
		}
		if !set[mine.String()] || !set[theirs.String()] {
			t.Fatalf("%s is missing a row every seat may read (mine %s, theirs %s): got %v", file, mine, theirs, rowIDs)
		}
	}
	assertOnlyID("person.csv", f.rep1Person, f.rep3Person)
	assertOnlyID("organization.csv", f.rep1Org, f.rep3Org)
	assertBothIDs("deal.csv", f.rep1Deal, f.rep3Deal)
	assertBothIDs("lead.csv", f.rep1Lead, f.rep3Lead)
	assertOnlyID("activity.csv", f.rep1Activity, f.rep3Activity)

	// One employment edge each; the other rep's joins two private endpoints,
	// so the caller sees only its own.
	if got := summary.RowCounts["relationship"]; got != 1 {
		t.Fatalf("row-scope leak: relationship count = %d, want 1", got)
	}
	// The attachment (files manifest) hides the other rep's file.
	entIDs := CSVColumn(t, entries["attachment.csv"], "entity_id")
	if len(entIDs) != 1 || entIDs[0] != f.rep1Person.String() {
		t.Fatalf("row-scope leak in files manifest: attachment entity_ids = %v", entIDs)
	}
	// The audit_log excludes the row about the invisible person.
	auditEntities := CSVColumn(t, entries["audit_log.csv"], "entity_id")
	for _, id := range auditEntities {
		if id == f.rep3Person.String() {
			t.Fatalf("row-scope leak: audit_log exposed an invisible person's row %s", id)
		}
	}
	// Pipeline/stage are workspace-shared reference data — present for
	// every member so exported deals resolve their stage.
	if summary.RowCounts["pipeline"] != 1 || summary.RowCounts["stage"] != 1 {
		t.Fatalf("workspace-shared config missing from a scoped export: %+v", summary.RowCounts)
	}
}

// RBAC bounds what the export contains: an object with no read grant is
// omitted from the bundle entirely, not silently dumped.
func TestExportOmitsObjectsWithoutReadGrant(t *testing.T) {
	e := SetupSearch(t)
	e.seedExportFixture(t)

	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	personOnly := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"person": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	var buf bytes.Buffer
	summary, err := compose.NewExportWriter(e.Pool).WriteBundle(personOnly, &buf)
	if err != nil {
		t.Fatal(err)
	}
	entries := BundleEntries(t, buf.Bytes())

	if _, ok := entries["person.csv"]; !ok {
		t.Fatal("granted object person was omitted")
	}
	for _, denied := range []string{"deal.csv", "organization.csv", "lead.csv", "activity.csv", "relationship.csv"} {
		if _, ok := entries[denied]; ok {
			t.Fatalf("ungranted object %s was exported", denied)
		}
	}
	omitted := strings.Join(summary.Omitted, ",")
	for _, want := range []string{"deal", "organization", "lead", "activity", "relationship"} {
		if !strings.Contains(omitted, want) {
			t.Fatalf("summary.Omitted missing %q: %v", want, summary.Omitted)
		}
	}
}

// keys lists a map's keys for failure messages.
func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
