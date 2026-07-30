// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The reconstruction bundle parser, unit-tested on crafted archives:
// it is the one place the rebuild trusts operator-supplied bytes, so
// what it accepts — and what it refuses — is the whole boundary.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// bundleZip builds an in-memory export bundle from its two JSON members.
func bundleZip(t *testing.T, manifest, dump any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, v any) {
		t.Helper()
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		if err := json.NewEncoder(f).Encode(v); err != nil {
			t.Fatalf("encoding %s: %v", name, err)
		}
	}
	write("data.json", dump)
	write("manifest.json", manifest)
	if err := zw.Close(); err != nil {
		t.Fatalf("closing the bundle: %v", err)
	}
	return buf.Bytes()
}

func TestParseBundleReadsTheEstateAndItsOwnerMap(t *testing.T) {
	appUser := ids.NewV7()
	raw := bundleZip(t,
		map[string]any{"canonical_data_resides_in": "hubspot"},
		map[string]any{
			"format": exportFormat,
			"objects": map[string]any{
				"overlay_mirror": []any{
					map[string]any{
						"object_class": "person", "external_id": "p-2",
						"fields": map[string]any{"full_name": "Second"}, "owner_external_id": "owner-1",
					},
					map[string]any{
						"object_class": "person", "external_id": "p-1",
						"fields": map[string]any{"full_name": "First"},
					},
					map[string]any{
						"object_class": "organization", "external_id": "org-1",
						"fields": map[string]any{"display_name": "Acme"},
					},
				},
				"overlay_association": []any{
					map[string]any{
						"from_type": "person", "from_id": "p-1",
						"to_type": "organization", "to_id": "org-1", "category": "employment",
					},
				},
				"mirror_user_map": []any{
					map[string]any{"incumbent_user_id": "owner-1", "app_user_id": appUser.String()},
				},
			},
		})

	contents, err := parseBundle(raw)
	if err != nil {
		t.Fatalf("parseBundle: %v", err)
	}
	if contents.incumbent != "hubspot" {
		t.Errorf("incumbent = %q, want hubspot — the provenance stamp the rebuild re-applies", contents.incumbent)
	}
	if got, ok := contents.owners["owner-1"]; !ok || got != appUser {
		t.Errorf("owner map = %v, want owner-1 bound to the exported app_user (without it every row rebuilds ownerless)", contents.owners)
	}

	// Rows page in a stable external_id order — the engine's checkpoint
	// is positional, so a shuffled source would resume onto a different row.
	people, err := contents.source.Rows(t.Context(), "person", 0, 10)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(people) != 2 || people[0].ExternalID != "p-1" || people[1].ExternalID != "p-2" {
		t.Fatalf("person rows = %+v, want p-1 then p-2", people)
	}
	// The owner rides in-band, where the writer's resolver reads it.
	if people[1].Fields[flipFieldOwnerExternalID] != "owner-1" {
		t.Errorf("p-2 lost its incumbent owner: %v", people[1].Fields)
	}

	counts, err := contents.source.Counts(t.Context())
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts["person"] != 2 || counts["organization"] != 1 {
		t.Errorf("counts = %v, want 2 persons and 1 organization", counts)
	}
	assocs, err := contents.source.Associations(t.Context())
	if err != nil || len(assocs) != 1 || assocs[0].FromID != "p-1" {
		t.Errorf("associations = %+v (err %v), want the seeded employment edge", assocs, err)
	}
}

func TestParseBundleRefusesWhatItCannotRebuildFrom(t *testing.T) {
	t.Run("not a zip", func(t *testing.T) {
		if _, err := parseBundle([]byte("this is not an archive")); err == nil {
			t.Fatal("a non-archive must be refused")
		}
	})

	t.Run("a foreign bundle format", func(t *testing.T) {
		raw := bundleZip(t, map[string]any{"canonical_data_resides_in": "hubspot"},
			map[string]any{"format": "someone-elses-export/9", "objects": map[string]any{}})
		_, err := parseBundle(raw)
		if err == nil || !strings.Contains(err.Error(), "format") {
			t.Fatalf("err = %v, want a refusal naming the format", err)
		}
	})

	t.Run("a native bundle carries no mirror snapshot", func(t *testing.T) {
		// No canonical_data_resides_in: exported outside overlay mode, so
		// there is no estate to rebuild and saying so beats importing zero.
		raw := bundleZip(t, map[string]any{},
			map[string]any{"format": exportFormat, "objects": map[string]any{}})
		_, err := parseBundle(raw)
		if err == nil || !strings.Contains(err.Error(), "PRE-FLIP") {
			t.Fatalf("err = %v, want a refusal that says only a pre-flip bundle rebuilds", err)
		}
	})

	t.Run("a mirror row missing its identity", func(t *testing.T) {
		raw := bundleZip(t, map[string]any{"canonical_data_resides_in": "hubspot"},
			map[string]any{"format": exportFormat, "objects": map[string]any{
				"overlay_mirror": []any{map[string]any{"fields": map[string]any{"full_name": "Nameless"}}},
			}})
		if _, err := parseBundle(raw); err == nil {
			t.Fatal("a row with no object_class/external_id must be refused, not imported under an empty key")
		}
	})

	t.Run("an unparseable owner id", func(t *testing.T) {
		raw := bundleZip(t, map[string]any{"canonical_data_resides_in": "hubspot"},
			map[string]any{"format": exportFormat, "objects": map[string]any{
				"mirror_user_map": []any{map[string]any{"incumbent_user_id": "owner-1", "app_user_id": "not-a-uuid"}},
			}})
		if _, err := parseBundle(raw); err == nil {
			t.Fatal("a malformed app_user_id must be refused rather than silently dropping the owner")
		}
	})
}

func TestBundleRowsPageAndRunOut(t *testing.T) {
	raw := bundleZip(t, map[string]any{"canonical_data_resides_in": "hubspot"},
		map[string]any{"format": exportFormat, "objects": map[string]any{
			"overlay_mirror": []any{
				map[string]any{"object_class": "person", "external_id": "p-1", "fields": map[string]any{"full_name": "A"}},
				map[string]any{"object_class": "person", "external_id": "p-2", "fields": map[string]any{"full_name": "B"}},
			},
		}})
	contents, err := parseBundle(raw)
	if err != nil {
		t.Fatalf("parseBundle: %v", err)
	}
	// limit truncates: a source that ignored it would break the engine's
	// positional checkpoint without failing any other assertion here.
	if capped, err := contents.source.Rows(t.Context(), "person", 0, 1); err != nil || len(capped) != 1 || capped[0].ExternalID != "p-1" {
		t.Fatalf("limited page = %+v (err %v), want exactly p-1", capped, err)
	}
	page, err := contents.source.Rows(t.Context(), "person", 1, 10)
	if err != nil || len(page) != 1 || page[0].ExternalID != "p-2" {
		t.Fatalf("offset page = %+v (err %v), want just p-2", page, err)
	}
	if page, err := contents.source.Rows(t.Context(), "person", 5, 10); err != nil || len(page) != 0 {
		t.Fatalf("past-the-end page = %+v (err %v), want empty", page, err)
	}
	if page, err := contents.source.Rows(t.Context(), "deal", 0, 10); err != nil || len(page) != 0 {
		t.Fatalf("absent class = %+v (err %v), want empty", page, err)
	}
}
