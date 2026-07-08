// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The open-format export bundle writer (B-E11.10a, features/04 §5): a
// full-workspace data handover — every core object, the typed
// relationships, the activity timeline, a files manifest, and the
// audit_log — as CSV-per-object plus one relational JSON dump, packed in
// a ZIP (all open formats, no proprietary container; P7). It lives in
// compose because the bundle reads across every domain module's tables,
// which is exactly the composition layer's charter (the report engine
// next door is the same shape).
//
// The bundle is a ROW-SCOPED read: each member applies the very same
// visibility predicate its list endpoint uses (own/team owner scope OR a
// live record grant, the activity link-walk, the relationship
// endpoint-visibility rule), so an export can never hand a caller a row
// their lists would hide — an unscoped export would be a data breach.
// The self-serve endpoint, the export-level object gate, the audit of the
// export operation itself, and the River dispatch for large workspaces
// are B-E11.10b; this ticket is the writer they drive.

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// exportFormat is the bundle's self-describing version tag; a recipient
// (and the round-trip re-importer, B-E11.12) keys off it.
const exportFormat = "margince-export/1"

// scopeMode selects the row-visibility rule a member applies — one per
// distinct scope shape already in use by the module read paths, reused
// verbatim so export sees exactly what the lists see.
type scopeMode uint8

const (
	// scopeShareable is the own/team owner predicate OR a live record
	// grant — person, organization, deal, lead (auth.ScopeClauseFor).
	scopeShareable scopeMode = iota
	// scopeActivity walks activity_link: an activity is visible when any
	// linked record is, or when it has no links (auth.ActivityScopeClause).
	scopeActivity
	// scopeRelationship requires every non-null endpoint to be visible.
	scopeRelationship
	// scopeAttachment / scopeAudit scope a polymorphic (entity_type,
	// entity_id) row by the referenced record's visibility.
	scopeAttachment
	scopeAudit
	// scopeWorkspace is workspace-shared configuration with no per-row
	// owner (pipeline, stage): the workspace RLS boundary is the whole
	// scope, so members see the same config their deals point at.
	scopeWorkspace
	// scopePersonChild scopes a person child row (person_social) by its
	// parent person's visibility  14 the same rule the person read applies.
	scopePersonChild
)

// exportMember is one bundle entry: a table, its row-scope rule, and the
// RBAC object whose read grant gates it (empty = reference data that
// travels with the records it supports, gated only by the workspace RLS
// boundary).
type exportMember struct {
	table      string
	scope      scopeMode
	objectGate string
}

// exportMembers is the bundle contents, in a stable order (the ZIP entry
// order and the manifest order both follow it). app_user/owner rows are
// deliberately not exported here: app_user carries password_hash and is
// identity, not CRM data — owner references remain as owner_id in the
// exported rows, and resolving them to user records is left to the
// round-trip re-importer's concern (B-E11.12), not this writer.
var exportMembers = []exportMember{
	{table: "person", scope: scopeShareable, objectGate: "person"},
	{table: "person_social", scope: scopePersonChild, objectGate: "person"},
	{table: "organization", scope: scopeShareable, objectGate: "organization"},
	{table: "deal", scope: scopeShareable, objectGate: "deal"},
	{table: "lead", scope: scopeShareable, objectGate: "lead"},
	{table: "activity", scope: scopeActivity, objectGate: "activity"},
	{table: "relationship", scope: scopeRelationship, objectGate: "relationship"},
	{table: "pipeline", scope: scopeWorkspace},
	{table: "stage", scope: scopeWorkspace},
	{table: "attachment", scope: scopeAttachment},
	{table: "audit_log", scope: scopeAudit},
}

// ExportWriter assembles the open-format bundle for the caller's
// workspace, scoped to what the caller may see.
type ExportWriter struct {
	pool *pgxpool.Pool
}

// NewExportWriter builds the writer over the shared pool.
func NewExportWriter(pool *pgxpool.Pool) *ExportWriter {
	return &ExportWriter{pool: pool}
}

// BundleSummary reports what the writer produced: the per-object row
// counts and any objects omitted because the caller lacked the read
// grant (RBAC bounds what the export contains).
type BundleSummary struct {
	RowCounts map[string]int
	Omitted   []string
}

// memberData holds one member's read result: the ordered column list
// (drives the CSV header and cell order) and the raw driver rows.
type memberData struct {
	table   string
	columns []string
	rows    [][]any
}

// WriteBundle reads every visible member inside one workspace-bound
// transaction, then streams the ZIP to dst: a CSV per object, one
// relational JSON dump, a files manifest, and a bundle manifest.
func (w *ExportWriter) WriteBundle(ctx context.Context, dst io.Writer) (BundleSummary, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return BundleSummary{}, errors.New("compose: no actor bound to export context")
	}
	summary := BundleSummary{RowCounts: make(map[string]int, len(exportMembers))}

	var collected []memberData
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		for _, m := range exportMembers {
			if m.objectGate != "" {
				if err := auth.Require(ctx, m.objectGate, principal.ActionRead); err != nil {
					if errors.Is(err, apperrors.ErrPermissionDenied) {
						summary.Omitted = append(summary.Omitted, m.table)
						continue
					}
					return err
				}
			}
			data, err := readMember(ctx, tx, m)
			if err != nil {
				return err
			}
			collected = append(collected, data)
			summary.RowCounts[m.table] = len(data.rows)
		}
		return nil
	})
	if err != nil {
		return BundleSummary{}, err
	}

	wsID, _ := principal.WorkspaceID(ctx)
	if err := writeZip(dst, actor, wsID, collected, summary); err != nil {
		return BundleSummary{}, err
	}
	return summary, nil
}

// readMember runs one member's scoped read: it derives the real
// (non-generated) columns from the live catalog — so the dump carries
// honest relational columns and never the internal search_tsv, with no
// column list duplicated in code — then selects them under the member's
// visibility predicate, ordered by id for a deterministic bundle.
func readMember(ctx context.Context, tx pgx.Tx, m exportMember) (memberData, error) {
	columns, err := exportableColumns(ctx, tx, m.table)
	if err != nil {
		return memberData{}, err
	}

	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := memberScope(ctx, m, "t", arg)
	if err != nil {
		return memberData{}, err
	}

	selects := make([]string, len(columns))
	for i, col := range columns {
		selects[i] = "t." + col
	}
	sql := fmt.Sprintf("SELECT %s FROM %s t", strings.Join(selects, ", "), m.table)
	if scope != "" {
		sql += " WHERE " + scope
	}
	sql += " ORDER BY t.id"

	pgRows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return memberData{}, fmt.Errorf("export %s: %w", m.table, err)
	}
	defer pgRows.Close()

	data := memberData{table: m.table, columns: columns}
	for pgRows.Next() {
		values, err := pgRows.Values()
		if err != nil {
			return memberData{}, err
		}
		data.rows = append(data.rows, values)
	}
	if err := pgRows.Err(); err != nil {
		return memberData{}, err
	}
	return data, nil
}

// exportableColumns lists a table's persisted columns in definition
// order, excluding generated columns (the tsvector search indexes) — the
// single source of truth is the live schema, so the export can never
// drift from the tables it dumps.
func exportableColumns(ctx context.Context, tx pgx.Tx, table string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1 AND is_generated = 'NEVER'
		 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		// A member table with no catalog columns means the schema and this
		// registry disagree — fail loudly rather than write an empty file.
		return nil, fmt.Errorf("export: table %q has no exportable columns", table)
	}
	return columns, nil
}

// writeZip packs the collected members into the bundle: a CSV per object,
// the relational JSON dump, the files manifest, and the bundle manifest.
func writeZip(dst io.Writer, actor principal.Principal, wsID ids.UUID, members []memberData, summary BundleSummary) error {
	zw := zip.NewWriter(dst)

	dump := make(map[string]any, len(members))
	var filesManifest []map[string]any
	generatedAt := time.Now().UTC()

	manifest := bundleManifest{
		Format:         exportFormat,
		WorkspaceID:    wsID.String(),
		GeneratedAt:    generatedAt,
		GeneratedBy:    actor.ID,
		OmittedObjects: summary.Omitted,
		Note: "Row-scoped to the exporting principal; open formats only (CSV per object + a relational JSON dump). " +
			"File bytes are referenced by storage_key, not embedded — see files-manifest.json.",
	}

	for _, m := range members {
		if err := writeCSV(zw, m); err != nil {
			return err
		}
		dump[m.table] = rowsAsMaps(m)
		manifest.Members = append(manifest.Members, manifestMember{
			Object: m.table, File: m.table + ".csv", Rows: len(m.rows),
		})
		if m.table == "attachment" {
			filesManifest = rowsAsMaps(m)
		}
	}

	if err := writeJSON(zw, "data.json", relationalDump{
		Format: exportFormat, GeneratedAt: generatedAt, Objects: dump,
	}); err != nil {
		return err
	}
	if err := writeJSON(zw, "files-manifest.json", filesManifestDoc{
		Note: "Every attachment with its integrity checksum. File bytes live in the object store under storage_key; " +
			"the blob substrate (B-EP02.27) is not yet wired in this build, so bytes are referenced here, not bundled.",
		Files: filesManifest,
	}); err != nil {
		return err
	}
	if err := writeJSON(zw, "manifest.json", manifest); err != nil {
		return err
	}

	return zw.Close()
}

// writeCSV writes one member as a CSV entry: the derived column list is
// the header, each driver value rendered as a flat cell.
func writeCSV(zw *zip.Writer, m memberData) error {
	f, err := zw.Create(m.table + ".csv")
	if err != nil {
		return err
	}
	cw := csv.NewWriter(f)
	if err := cw.Write(m.columns); err != nil {
		return err
	}
	record := make([]string, len(m.columns))
	for _, row := range m.rows {
		for i := range m.columns {
			record[i] = csvCell(row[i])
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// writeJSON marshals v into a ZIP entry as indented JSON.
//
//craft:ignore naked-any the bundle documents are heterogeneous JSON shapes assembled for handover, not a single typed record
func writeJSON(zw *zip.Writer, name string, v any) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// rowsAsMaps shapes a member's rows as column→value maps for the JSON
// dump, re-embedding jsonb bytes as raw JSON (never base64) and uuids as
// their canonical strings.
func rowsAsMaps(m memberData) []map[string]any {
	out := make([]map[string]any, 0, len(m.rows))
	for _, row := range m.rows {
		rec := make(map[string]any, len(m.columns))
		for i, col := range m.columns {
			rec[col] = jsonValue(row[i])
		}
		out = append(out, rec)
	}
	return out
}

// jsonValue normalizes a driver value for the JSON dump.
//
//craft:ignore naked-any a driver row value spans every SQL type the exported tables carry; the switch narrows each
func jsonValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case [16]byte:
		return ids.UUID(t).String()
	case []byte:
		// jsonb columns arrive as raw bytes; embed them as JSON so the
		// dump nests the object instead of base64-encoding it.
		return json.RawMessage(t)
	default:
		return v
	}
}

// csvCell renders a driver value as a single CSV field.
//
//craft:ignore naked-any a driver row value spans every SQL type the exported tables carry; the switch narrows each
func csvCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case [16]byte:
		return ids.UUID(t).String()
	case []byte:
		return string(t)
	case string:
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// bundleManifest describes the bundle: format, provenance, the members
// present, and any objects the caller's grants excluded.
type bundleManifest struct {
	Format         string           `json:"format"`
	WorkspaceID    string           `json:"workspace_id,omitempty"`
	GeneratedAt    time.Time        `json:"generated_at"`
	GeneratedBy    string           `json:"generated_by"`
	Members        []manifestMember `json:"members"`
	OmittedObjects []string         `json:"omitted_objects,omitempty"`
	Note           string           `json:"note"`
}

type manifestMember struct {
	Object string `json:"object"`
	File   string `json:"file"`
	Rows   int    `json:"rows"`
}

// relationalDump is the single JSON view of every exported object.
//
//craft:ignore naked-any Objects maps each table name to its exported rows, whose columns are schema-derived at runtime
type relationalDump struct {
	Format      string         `json:"format"`
	GeneratedAt time.Time      `json:"generated_at"`
	Objects     map[string]any `json:"objects"`
}

// filesManifestDoc is the files manifest: every attachment plus a note on
// where the bytes live.
type filesManifestDoc struct {
	Note  string           `json:"note"`
	Files []map[string]any `json:"files"`
}
