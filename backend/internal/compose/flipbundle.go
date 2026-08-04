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
// This is the engine's `bundle` connector, exercised today by the
// OVA-AC-6(d) lane. It is deliberately package-internal: the /import/*
// wire that would expose it is IEM-GAP-2's contract extension, and
// exporting a rebuild entry point with no caller that ships would be
// surface without a consumer.

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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// bundleContents is one parsed export bundle: the estate source plus
// the two things the rebuild needs beside it — the incumbent name the
// manifest discloses (the provenance stamp a rebuilt row re-applies, so
// it carries the same source its flipped sibling would) and the
// bundle's OWN owner map, since a clean instance has no live
// mirror_user_map to resolve owners against.
type bundleContents struct {
	source    migration.Source
	incumbent string
	owners    map[string]ids.UUID
}

// maxBundleEntryBytes caps one decompressed bundle member. The bundle is
// operator-supplied input to a rebuild, so a crafted archive must not be
// able to exhaust memory before the parse even starts.
const maxBundleEntryBytes = 512 << 20 // 512 MiB

// parseBundle reads an export bundle into its reconstruction contents.
func parseBundle(bundle []byte) (bundleContents, error) {
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		return bundleContents{}, fmt.Errorf("reconstruction: opening the export bundle: %w", err)
	}
	var dump struct {
		Format  string                      `json:"format"`
		Objects map[string][]map[string]any `json:"objects"`
	}
	if err := readBundleJSON(zr, "data.json", &dump); err != nil {
		return bundleContents{}, err
	}
	if dump.Format != exportFormat {
		return bundleContents{}, fmt.Errorf("reconstruction: bundle format %q is not %q: %w", dump.Format, exportFormat, apperrors.ErrConflict)
	}
	var manifest struct {
		CanonicalDataResidesIn string `json:"canonical_data_resides_in"`
	}
	if err := readBundleJSON(zr, "manifest.json", &manifest); err != nil {
		return bundleContents{}, err
	}
	if manifest.CanonicalDataResidesIn == "" {
		return bundleContents{}, fmt.Errorf(
			"reconstruction: this bundle carries no mirror snapshot — only a PRE-FLIP overlay bundle rebuilds an estate: %w", apperrors.ErrConflict)
	}

	src := bundleFlipSource{rows: map[string][]migration.Row{}}
	for _, raw := range dump.Objects["overlay_mirror"] {
		row, class, err := bundleMirrorRow(raw)
		if err != nil {
			return bundleContents{}, err
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

	// The bundle's own owner map: the flipped estate's records carry
	// their incumbent owner, and only this map turns that back into an
	// app_user — without it every rebuilt row would land ownerless.
	owners := map[string]ids.UUID{}
	for _, raw := range dump.Objects["mirror_user_map"] {
		incumbentUser := bundleString(raw, "incumbent_user_id")
		appUser := bundleString(raw, "app_user_id")
		if incumbentUser == "" || appUser == "" {
			continue
		}
		id, err := ids.Parse(appUser)
		if err != nil {
			return bundleContents{}, fmt.Errorf("reconstruction: the bundle's user map carries an unparseable app_user_id %q: %w", appUser, err)
		}
		owners[incumbentUser] = id
	}
	return bundleContents{source: src, incumbent: manifest.CanonicalDataResidesIn, owners: owners}, nil
}

// reconstructFromBundle rebuilds a clean native instance from a pre-flip
// export bundle: a `bundle`-connector migration run through the same
// engine and native writers the flip used. The target workspace is the
// ctx's — reconstruction assumes a clean instance and is idempotent on
// the rows' provenance if re-run.
func reconstructFromBundle(ctx context.Context, pool *pgxpool.Pool, bundle []byte) (migration.Report, error) {
	// Gated here as well as at the run store: a rebuild writes the whole
	// estate into this workspace, so the caller must hold the import
	// grant before the bundle is even parsed.
	if err := auth.Require(ctx, "import_run", principal.ActionCreate); err != nil {
		return migration.Report{}, err
	}
	contents, err := parseBundle(bundle)
	if err != nil {
		return migration.Report{}, err
	}
	runs := migration.NewRunStore(pool)
	// Everything that can still fail happens BEFORE the run row exists.
	// Only the engine needs a run id, so an early return here cannot
	// strand a row at `running` with an empty error column — a state
	// nothing resumes and nothing clears, and one indistinguishable from
	// a reconstruction still in progress.
	//
	// The bundle's owner map names app_users of the workspace it was
	// exported FROM. Reconstruction may land in a different tenant whose
	// user set differs, and an owner_id pointing at a stranger would be
	// rejected by the FK — so the map is filtered to users that actually
	// exist here, and a record whose owner did not travel imports
	// ownerless with a disclosure rather than failing the rebuild.
	owners, err := presentOwners(ctx, pool, contents.owners)
	if err != nil {
		return migration.Report{}, err
	}
	operator, err := flipOperator(ctx)
	if err != nil {
		return migration.Report{}, err
	}
	assocs, err := contents.source.Associations(ctx)
	if err != nil {
		return migration.Report{}, err
	}

	run, err := runs.Create(ctx, migration.CreateRunInput{
		Connector: migration.ConnectorBundle,
		SourceRef: exportFormat,
		Source:    "bundle:reconstruction",
	})
	if err != nil {
		return migration.Report{}, err
	}
	// unresolvedOwnerEmails, not nil: this path never resolves an owner
	// email (owners come from the bundle's own map), and a fail-loud
	// placeholder beats a nil-interface panic if that stops being true.
	writers := newFlipWriters(pool, overlay.NewMirrorStore(pool, unresolvedOwnerEmails{}), contents.incumbent).
		forRun(run.ID, operator).
		WithOwnerMap(owners)
	writers.SetAssociations(assocs)
	return migration.NewEngine(runs, writers).Run(ctx, run.ID, contents.source)
}

// presentOwners narrows a bundle's incumbent-user -> app-user map to the
// app_users that exist in the target workspace.
func presentOwners(ctx context.Context, pool *pgxpool.Pool, owners map[string]ids.UUID) (map[string]ids.UUID, error) {
	if len(owners) == 0 {
		return owners, nil
	}
	candidates := make([]ids.UUID, 0, len(owners))
	for _, appUser := range owners {
		candidates = append(candidates, appUser)
	}
	live := make(map[ids.UUID]bool, len(candidates))
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM app_user WHERE id = ANY($1)`, candidates)
		if err != nil {
			return fmt.Errorf("reconstruction: checking which bundle owners exist here: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UUID
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("reconstruction: scanning a surviving owner: %w", err)
			}
			live[id] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	present := make(map[string]ids.UUID, len(owners))
	for incumbentUser, appUser := range owners {
		if live[appUser] {
			present[incumbentUser] = appUser
		}
	}
	return present, nil
}

func readBundleJSON[T any](zr *zip.Reader, name string, out *T) error {
	f, err := zr.Open(name)
	if err != nil {
		return fmt.Errorf("reconstruction: the bundle has no %s: %w", name, err)
	}
	// LimitReader + 1: a member that fills the cap is over it, and a
	// zip bomb hits the ceiling instead of the machine's memory.
	raw, readErr := io.ReadAll(io.LimitReader(f, maxBundleEntryBytes+1))
	if closeErr := f.Close(); closeErr != nil && readErr == nil {
		readErr = closeErr
	}
	if readErr != nil {
		return fmt.Errorf("reconstruction: reading %s: %w", name, readErr)
	}
	if len(raw) > maxBundleEntryBytes {
		return fmt.Errorf("reconstruction: %s exceeds the %d-byte bundle-member cap: %w", name, maxBundleEntryBytes, apperrors.ErrConflict)
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
		// Never echo any of the row: it is a customer record, and even
		// object_class is untrusted bundle content. Which key is absent
		// is the whole diagnosis, so that is all this says.
		return migration.Row{}, "", fmt.Errorf(
			"reconstruction: a mirror row is missing its identity (object_class present: %t, external_id present: %t)",
			class != "", ext != "")
	}
	row := migration.Row{ExternalID: ext}
	if fields, ok := raw["fields"].(map[string]any); ok {
		row.Fields = fields
	}
	row.OwnerExternalID = bundleString(raw, "owner_external_id")
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

// ReconstructForTest is the OVA-AC-6(d) lane's entry into the
// unexported rebuild — its only caller until the /import/* wire lands
// (IEM-GAP-2's contract extension), which is why the rebuild itself
// stays unexported and this seam is named for what it is.
//
// Unlike ClaimFlipForTest (flipclaim_integration.go) this is NOT behind
// the integration tag, deliberately: claimFlip has product callers, so
// gating its seam costs nothing, while reconstructFromBundle's only
// caller is this function — gating it would leave the whole rebuild
// path unreferenced, reading as dead code, in every untagged build.
func ReconstructForTest(ctx context.Context, pool *pgxpool.Pool, bundle []byte) (migration.Report, error) {
	return reconstructFromBundle(ctx, pool, bundle)
}
