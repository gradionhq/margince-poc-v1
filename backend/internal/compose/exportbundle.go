// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The export bundle's serializer: the ZIP layout the reader (and the
// round-trip re-importer) sees — a CSV per object, one relational JSON
// dump, the files manifest, and the bundle manifest that carries the
// AC-OV-9 honest-scope disclosure. export.go decides WHAT the bundle
// contains and under whose visibility; this file decides how it is
// written down.

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// writeZip packs the collected members into the bundle: a CSV per object,
// the relational JSON dump, the files manifest, and the bundle manifest.
func writeZip(dst io.Writer, actor principal.Principal, wsID ids.UUID, incumbent string, members []memberData, summary BundleSummary) error {
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
	if incumbent != "" {
		// The honest-scope manifest (AC-OV-9): in overlay mode, canonical
		// data resides in the incumbent — this bundle is our augmentation
		// plus the mirror snapshot, and P7 is partial until the flip.
		manifest.CanonicalDataResidesIn = incumbent
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
	Format      string    `json:"format"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
	GeneratedBy string    `json:"generated_by"`
	// CanonicalDataResidesIn is the AC-OV-9 honest-scope disclosure: set
	// (to the incumbent's name) only while the workspace runs in overlay
	// mode, where this bundle is augmentation + mirror snapshot and the
	// canonical estate still lives in the incumbent.
	CanonicalDataResidesIn string           `json:"canonical_data_resides_in,omitempty"`
	Members                []manifestMember `json:"members"`
	OmittedObjects         []string         `json:"omitted_objects,omitempty"`
	Note                   string           `json:"note"`
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
