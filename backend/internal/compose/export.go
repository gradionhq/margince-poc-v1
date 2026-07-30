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
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// exportFormat is the bundle's self-describing version tag; a recipient
// (and the round-trip re-importer, B-E11.12) keys off it.
const exportFormat = "margince-export/1"

// auditFieldFormat names the bundle format in the export audit row.
const auditFieldFormat = "format"

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
	// scopeMirror gates a mirror row by the caller's mirror_visibility
	// deny-join — the same fail-closed rule every overlay read applies
	// (ADR-0044): an unmapped caller exports zero mirror rows.
	scopeMirror
	// scopeMirrorAssoc requires BOTH endpoints of a mirrored association
	// edge to be visible — an edge never discloses a record on the far
	// side the caller cannot see (the relationship member's rule, on
	// mirror visibility).
	scopeMirrorAssoc
)

// exportMember is one bundle entry: a table, its row-scope rule, and the
// RBAC object whose read grant gates it (empty = reference data that
// travels with the records it supports, gated only by the workspace RLS
// boundary).
type exportMember struct {
	table      string
	scope      scopeMode
	objectGate string
	// updateGate additionally requires the UPDATE grant on objectGate —
	// the admin-only members (the incumbent user map's surface is
	// admin-managed, RC-15/ADR-0057, so its export follows that gate).
	updateGate bool
	// orderBy overrides the deterministic ordering for tables without an
	// id column (the overlay mirror's composite keys). Empty = "t.id".
	orderBy string
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

// overlayExportMembers join the bundle for a workspace in OVERLAY mode
// (AC-OV-9: the export contains our augmentation PLUS the mirror
// snapshot, and documents that canonical data resides in the incumbent).
// Mirror rows and edges ride the caller's mirror-visibility deny-join —
// the bundle keeps its "never a row their lists would hide" contract;
// the user map is admin-gated like its own surface.
var overlayExportMembers = []exportMember{
	{table: "overlay_mirror", scope: scopeMirror, objectGate: string(recordTypeOverlayConnection), orderBy: "t.object_class, t.external_id"},
	{table: "overlay_association", scope: scopeMirrorAssoc, objectGate: string(recordTypeOverlayConnection), orderBy: "t.from_type, t.from_id, t.to_type, t.to_id, t.type_id"},
	{table: "mirror_user_map", scope: scopeWorkspace, objectGate: string(recordTypeOverlayConnection), updateGate: true, orderBy: "t.app_user_id, t.incumbent"},
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
	var incumbent string
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		// In overlay mode the bundle additionally carries the mirror
		// snapshot and documents where canonical data lives (AC-OV-9) —
		// P7 stays honestly partial until the flip.
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(x_incumbent, '') FROM workspace
			WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`,
		).Scan(&incumbent); err != nil {
			return fmt.Errorf("export: resolving the workspace's SoR mode: %w", err)
		}
		members := exportMembers
		if incumbent != "" {
			members = append(append([]exportMember{}, exportMembers...), overlayExportMembers...)
		}
		for _, m := range members {
			if m.objectGate != "" {
				action := principal.ActionRead
				if m.updateGate {
					action = principal.ActionUpdate
				}
				if err := auth.Require(ctx, m.objectGate, action); err != nil {
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
		// The single export audit entry (features/04 §5: who exported
		// what, when) — in the same transaction as the reads it
		// describes; the flip preflight's export-recency check reads it.
		wsID, ok := principal.WorkspaceID(ctx)
		if !ok {
			return errors.New("compose: no workspace bound to export context")
		}
		_, err := storekit.Audit(ctx, tx, "export", "workspace", wsID, nil, map[string]any{
			auditFieldFormat: exportFormat, "row_counts": summary.RowCounts, "omitted": summary.Omitted,
			"canonical_data_resides_in": incumbent,
		})
		return err
	})
	if err != nil {
		return BundleSummary{}, err
	}

	wsID, _ := principal.WorkspaceID(ctx)
	if err := writeZip(dst, actor, wsID, incumbent, collected, summary); err != nil {
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
	orderBy := m.orderBy
	if orderBy == "" {
		orderBy = "t.id"
	}
	sql += " ORDER BY " + orderBy

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
