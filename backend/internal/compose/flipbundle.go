// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Reconstruction from the pre-flip export (OVA-AC-6 d, ADR-0071 §5):
// reversibility is REBUILD, not rollback — the bundle's mirror snapshot
// re-imports into a clean native instance through the same migration
// engine the flip ran, with zero incumbent calls (nothing in this path
// holds an incumbent adapter at all). It does not restore the incumbent
// as system of record and makes no native→overlay reverse claim.
//
// This is the engine's `bundle` connector. It has no HTTP surface yet on
// purpose: the /import/* wire is IEM-GAP-2's contract extension; until
// that lands, reconstruction is an operator/compose-level entry point.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// bundleFlipSource serves a margince-export/1 bundle's mirror snapshot
// as a migration.Source: rows come from the relational dump's
// overlay_mirror member, edges from overlay_association — the same
// estate shape the mirror source serves live.
type bundleFlipSource struct {
	rows   map[string][]migration.Row
	assocs []migration.Assoc
}

var _ migration.Source = bundleFlipSource{}

func (s bundleFlipSource) Objects() []string { return flipImportOrder }

func (s bundleFlipSource) Counts(context.Context) (map[string]int, error) {
	counts := make(map[string]int, len(s.rows))
	for class, rows := range s.rows {
		counts[class] = len(rows)
	}
	return counts, nil
}

func (s bundleFlipSource) Rows(_ context.Context, object string, offset, limit int) ([]migration.Row, error) {
	rows := s.rows[object]
	if offset >= len(rows) {
		return nil, nil
	}
	end := min(offset+limit, len(rows))
	return rows[offset:end], nil
}

func (s bundleFlipSource) Associations(context.Context) ([]migration.Assoc, error) {
	return s.assocs, nil
}

// NewBundleFlipSource parses an export bundle into a reconstruction
// source, returning the incumbent name its manifest discloses
// (canonical_data_resides_in — the provenance stamp reconstruction
// re-applies, so a rebuilt row carries the same source its flipped
// sibling would).
func NewBundleFlipSource(bundle []byte) (migration.Source, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		return nil, "", fmt.Errorf("reconstruction: opening the export bundle: %w", err)
	}
	var dump struct {
		Format  string                      `json:"format"`
		Objects map[string][]map[string]any `json:"objects"`
	}
	if err := readBundleJSON(zr, "data.json", &dump); err != nil {
		return nil, "", err
	}
	if dump.Format != exportFormat {
		return nil, "", fmt.Errorf("reconstruction: bundle format %q is not %q", dump.Format, exportFormat)
	}
	var manifest struct {
		CanonicalDataResidesIn string `json:"canonical_data_resides_in"`
	}
	if err := readBundleJSON(zr, "manifest.json", &manifest); err != nil {
		return nil, "", err
	}
	if manifest.CanonicalDataResidesIn == "" {
		return nil, "", fmt.Errorf("reconstruction: the bundle carries no mirror snapshot (no canonical_data_resides_in manifest line) — only a pre-flip overlay bundle reconstructs: %w", errNotAPreflipBundle)
	}

	src := bundleFlipSource{rows: map[string][]migration.Row{}}
	for _, raw := range dump.Objects["overlay_mirror"] {
		row, class, err := bundleMirrorRow(raw)
		if err != nil {
			return nil, "", err
		}
		src.rows[class] = append(src.rows[class], row)
	}
	for class := range src.rows {
		rows := src.rows[class]
		sort.Slice(rows, func(i, j int) bool { return rows[i].ExternalID < rows[j].ExternalID })
	}
	for _, raw := range dump.Objects["overlay_association"] {
		src.assocs = append(src.assocs, migration.Assoc{
			FromType: bundleString(raw, "from_type"), FromID: bundleString(raw, "from_id"),
			ToType: bundleString(raw, "to_type"), ToID: bundleString(raw, "to_id"),
			Category: bundleString(raw, "category"), Label: bundleString(raw, "label"),
		})
	}
	return src, manifest.CanonicalDataResidesIn, nil
}

var errNotAPreflipBundle = fmt.Errorf("not a pre-flip overlay bundle")

// ReconstructFromBundle rebuilds a clean native instance from a pre-flip
// export bundle: a `bundle`-connector migration run through the same
// engine and native writers the flip used. The target workspace is the
// ctx's — reconstruction assumes a clean instance and is idempotent on
// the rows' provenance if re-run.
func ReconstructFromBundle(ctx context.Context, pool *pgxpool.Pool, bundle []byte) (migration.Report, error) {
	src, incumbent, err := NewBundleFlipSource(bundle)
	if err != nil {
		return migration.Report{}, err
	}
	runs := migration.NewRunStore(pool)
	run, err := runs.Create(ctx, migration.CreateRunInput{
		Connector: migration.ConnectorBundle,
		SourceRef: exportFormat,
		Source:    "bundle:reconstruction",
	})
	if err != nil {
		return migration.Report{}, err
	}
	writers := newFlipWriters(pool, overlay.NewMirrorStore(pool, nil), incumbent)
	assocs, err := src.Associations(ctx)
	if err != nil {
		return migration.Report{}, err
	}
	writers.SetAssociations(assocs)
	return migration.NewEngine(runs, writers).Run(ctx, run.ID, src)
}

func readBundleJSON(zr *zip.Reader, name string, out any) error {
	f, err := zr.Open(name)
	if err != nil {
		return fmt.Errorf("reconstruction: the bundle has no %s: %w", name, err)
	}
	raw, readErr := io.ReadAll(f)
	if closeErr := f.Close(); closeErr != nil && readErr == nil {
		readErr = closeErr
	}
	if readErr != nil {
		return fmt.Errorf("reconstruction: reading %s: %w", name, readErr)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("reconstruction: decoding %s: %w", name, err)
	}
	return nil
}

// bundleMirrorRow converts one dumped overlay_mirror row back into the
// engine's Row shape.
func bundleMirrorRow(raw map[string]any) (migration.Row, string, error) {
	class := bundleString(raw, "object_class")
	ext := bundleString(raw, "external_id")
	if class == "" || ext == "" {
		return migration.Row{}, "", fmt.Errorf("reconstruction: a mirror row is missing object_class/external_id: %v", raw)
	}
	row := migration.Row{ExternalID: ext}
	if fields, ok := raw["fields"].(map[string]any); ok {
		row.Fields = fields
	}
	if owner := bundleString(raw, "owner_external_id"); owner != "" {
		row.Fields = cloneFieldsWith(row.Fields, flipFieldOwnerExternalID, owner)
	}
	if ts := bundleString(raw, "last_synced_at"); ts != "" {
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return migration.Row{}, "", fmt.Errorf("reconstruction: mirror row %s/%s carries an unparseable last_synced_at %q: %w", class, ext, ts, err)
		}
		row.LastSyncedAt = parsed
	}
	return row, class, nil
}

func bundleString(raw map[string]any, key string) string {
	s, ok := raw[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}
