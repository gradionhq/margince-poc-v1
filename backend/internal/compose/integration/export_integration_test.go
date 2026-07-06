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
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
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

func (e *searchEnv) exportAdmin() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: exportReadGrants(), RowScope: principal.RowScopeAll},
	})
}

func (e *searchEnv) exportRep(user, team ids.UUID) context.Context {
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

func (e *searchEnv) seedExportFixture(t *testing.T) exportFixture {
	t.Helper()
	pipelineID := e.seed(t, `INSERT INTO pipeline (id, workspace_id, name, is_default, position) VALUES ($1, $2, 'Sales', true, 0)`)
	stageID := e.seed(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability) VALUES ($1, $2, $3, 'Qualify', 0, 'open', 10)`, pipelineID)

	var f exportFixture
	// rep1 (team1) carries a jsonb column to prove the dump nests it.
	f.rep1Person = e.seed(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, social, source, captured_by)
		VALUES ($1, $2, $3, 'Rep1 Person', $4::jsonb, 'manual', 'human:x')`, e.Rep1, `{"linkedin":"in/rep1"}`)
	f.rep3Person = e.seed(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, $3, 'Rep3 Person', 'manual', 'human:x')`, e.Rep3)
	f.rep1Org = e.seed(t, `INSERT INTO organization (id, workspace_id, owner_id, display_name, source, captured_by)
		VALUES ($1, $2, $3, 'Rep1 Org', 'manual', 'human:x')`, e.Rep1)
	f.rep3Org = e.seed(t, `INSERT INTO organization (id, workspace_id, owner_id, display_name, source, captured_by)
		VALUES ($1, $2, $3, 'Rep3 Org', 'manual', 'human:x')`, e.Rep3)
	f.rep1Deal = e.seed(t, `INSERT INTO deal (id, workspace_id, owner_id, name, pipeline_id, stage_id, organization_id, amount_minor, source, captured_by)
		VALUES ($1, $2, $3, 'Rep1 Deal', $4, $5, $6, 100000, 'manual', 'human:x')`, e.Rep1, pipelineID, stageID, f.rep1Org)
	f.rep3Deal = e.seed(t, `INSERT INTO deal (id, workspace_id, owner_id, name, pipeline_id, stage_id, organization_id, amount_minor, source, captured_by)
		VALUES ($1, $2, $3, 'Rep3 Deal', $4, $5, $6, 200000, 'manual', 'human:x')`, e.Rep3, pipelineID, stageID, f.rep3Org)
	f.rep1Lead = e.seed(t, `INSERT INTO lead (id, workspace_id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, $3, 'Rep1 Lead', 'manual', 'human:x')`, e.Rep1)
	f.rep3Lead = e.seed(t, `INSERT INTO lead (id, workspace_id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, $3, 'Rep3 Lead', 'manual', 'human:x')`, e.Rep3)

	// Employment edges: each connects a rep's person to that rep's org, so
	// the whole edge is visible only to that rep (both endpoints owned).
	e.seed(t, `INSERT INTO relationship (id, workspace_id, kind, person_id, organization_id, source, captured_by)
		VALUES ($1, $2, 'employment', $3, $4, 'manual', 'human:x')`, f.rep1Person, f.rep1Org)
	e.seed(t, `INSERT INTO relationship (id, workspace_id, kind, person_id, organization_id, source, captured_by)
		VALUES ($1, $2, 'employment', $3, $4, 'manual', 'human:x')`, f.rep3Person, f.rep3Org)

	// Activities scope through their links.
	f.rep1Activity = e.seed(t, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'Rep1 note', now(), 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, person_id) VALUES ($1, $2, $3, 'person', $4)`, f.rep1Activity, f.rep1Person)
	f.rep3Activity = e.seed(t, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'Rep3 note', now(), 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, person_id) VALUES ($1, $2, $3, 'person', $4)`, f.rep3Activity, f.rep3Person)

	// Attachments on each rep's person — the files manifest source.
	e.seed(t, `INSERT INTO attachment (id, workspace_id, entity_type, entity_id, filename, storage_key, source, captured_by)
		VALUES ($1, $2, 'person', $3, 'rep1.pdf', 'blob/rep1', 'manual', 'human:x')`, f.rep1Person)
	e.seed(t, `INSERT INTO attachment (id, workspace_id, entity_type, entity_id, filename, storage_key, source, captured_by)
		VALUES ($1, $2, 'person', $3, 'rep3.pdf', 'blob/rep3', 'manual', 'human:x')`, f.rep3Person)

	// Audit rows targeting each rep's person, and a login row (no entity).
	e.seed(t, `INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, action, entity_type, entity_id)
		VALUES ($1, $2, 'human', $3, 'create', 'person', $4)`, "human:"+e.Rep1.String(), f.rep1Person)
	e.seed(t, `INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, action, entity_type, entity_id)
		VALUES ($1, $2, 'human', $3, 'create', 'person', $4)`, "human:"+e.Rep3.String(), f.rep3Person)
	return f
}

// bundleEntries reads the produced ZIP into name→bytes.
func bundleEntries(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("opening bundle zip: %v", err)
	}
	entries := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading %s: %v", f.Name, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("closing %s: %v", f.Name, err)
		}
		entries[f.Name] = data
	}
	return entries
}

// csvColumn parses a CSV entry and returns the values under one column —
// the format-validity check (csv.Reader fails loudly on a malformed file)
// doubling as the content probe.
func csvColumn(t *testing.T, raw []byte, column string) []string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("parsing csv: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("csv has no header row")
	}
	idx := -1
	for i, h := range records[0] {
		if h == column {
			idx = i
		}
	}
	if idx == -1 {
		t.Fatalf("csv has no %q column; header=%v", column, records[0])
	}
	var out []string
	for _, row := range records[1:] {
		out = append(out, row[idx])
	}
	return out
}

func TestExportBundleCompleteAndValidOpenFormat(t *testing.T) {
	e := setupSearch(t)
	f := e.seedExportFixture(t)

	var buf bytes.Buffer
	summary, err := compose.NewExportWriter(e.Pool).WriteBundle(e.exportAdmin(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	entries := bundleEntries(t, buf.Bytes())

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
	if got := len(csvColumn(t, entries["person.csv"], "id")); got != 2 {
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
	var social map[string]any
	for _, p := range dump.Objects["person"] {
		if p["full_name"] == "Rep1 Person" {
			social, _ = p["social"].(map[string]any)
		}
	}
	if social == nil || social["linkedin"] != "in/rep1" {
		t.Fatalf("jsonb column did not round-trip as nested JSON: %v", social)
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
// exactly its own records and none of the other team's — across every
// scoped member (records, edges, activities, files, audit).
func TestExportRowScopeExcludesInvisibleRecords(t *testing.T) {
	e := setupSearch(t)
	f := e.seedExportFixture(t)

	var buf bytes.Buffer
	summary, err := compose.NewExportWriter(e.Pool).WriteBundle(e.exportRep(e.Rep1, e.Team1), &buf)
	if err != nil {
		t.Fatal(err)
	}
	entries := bundleEntries(t, buf.Bytes())

	assertOnlyID := func(file string, want, hidden ids.UUID) {
		rowIDs := csvColumn(t, entries[file], "id")
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
	assertOnlyID("person.csv", f.rep1Person, f.rep3Person)
	assertOnlyID("organization.csv", f.rep1Org, f.rep3Org)
	assertOnlyID("deal.csv", f.rep1Deal, f.rep3Deal)
	assertOnlyID("lead.csv", f.rep1Lead, f.rep3Lead)
	assertOnlyID("activity.csv", f.rep1Activity, f.rep3Activity)

	// One employment edge each; the caller sees only its own.
	if got := summary.RowCounts["relationship"]; got != 1 {
		t.Fatalf("row-scope leak: relationship count = %d, want 1", got)
	}
	// The attachment (files manifest) hides the other rep's file.
	entIDs := csvColumn(t, entries["attachment.csv"], "entity_id")
	if len(entIDs) != 1 || entIDs[0] != f.rep1Person.String() {
		t.Fatalf("row-scope leak in files manifest: attachment entity_ids = %v", entIDs)
	}
	// The audit_log excludes the row about the invisible person.
	auditEntities := csvColumn(t, entries["audit_log.csv"], "entity_id")
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
	e := setupSearch(t)
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
	entries := bundleEntries(t, buf.Bytes())

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
