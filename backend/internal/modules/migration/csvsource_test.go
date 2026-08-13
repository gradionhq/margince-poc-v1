// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
)

const testCSVKey = "ws/import/file"

func seedCSV(t *testing.T, body string) blobstore.Store {
	t.Helper()
	bs := blobstore.NewMemory()
	if err := bs.Put(context.Background(), testCSVKey, strings.NewReader(body), int64(len(body)), "text/csv"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return bs
}

func leadMapping() (map[string]string, string) {
	return map[string]string{"Email": "email", "First Name": "first_name"}, "Email"
}

// The engine's checkpoint contract requires Rows to page a stable, deterministic
// ordering — a resumed run continues at an absolute offset and must not re-read
// or skip a row. An uploaded file has no cursor, so this is the property the
// whole resume guarantee rests on, and it is asserted rather than assumed.
func TestCSVSourceRowsPageDeterministicallyByOffset(t *testing.T) {
	var b strings.Builder
	b.WriteString("Email,First Name\n")
	for i := range 500 {
		fmt.Fprintf(&b, "person%03d@x.test,Name%03d\n", i, i)
	}
	mapping, sourceKey := leadMapping()
	src := NewCSVSource(seedCSV(t, b.String()), testCSVKey, ObjectLead, mapping, sourceKey)
	ctx := context.Background()

	var paged []string
	for offset := 0; offset < 500; offset += 200 {
		rows, err := src.Rows(ctx, ObjectLead, offset, 200)
		if err != nil {
			t.Fatalf("Rows(%d): %v", offset, err)
		}
		for _, r := range rows {
			paged = append(paged, r.ExternalID)
		}
	}

	whole, err := src.Rows(ctx, ObjectLead, 0, 500)
	if err != nil {
		t.Fatalf("Rows(whole): %v", err)
	}
	if len(paged) != len(whole) {
		t.Fatalf("paged %d rows, whole read %d", len(paged), len(whole))
	}
	seen := make(map[string]bool, len(paged))
	for i, id := range paged {
		if id != whole[i].ExternalID {
			t.Fatalf("row %d: paged %q, whole read %q — the pages are not the file's own order", i, id, whole[i].ExternalID)
		}
		if seen[id] {
			t.Fatalf("row %d (%q) was delivered twice", i, id)
		}
		seen[id] = true
	}
}

func TestCSVSourceReadsPastTheEndWithoutError(t *testing.T) {
	mapping, sourceKey := leadMapping()
	src := NewCSVSource(seedCSV(t, "Email,First Name\na@x.test,A\n"), testCSVKey, ObjectLead, mapping, sourceKey)

	rows, err := src.Rows(context.Background(), ObjectLead, 50, 200)
	if err != nil {
		t.Fatalf("Rows past the end: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want none — a resume that overshoots is finished, not broken", len(rows))
	}
}

// An unmapped column is not data. Carrying it into Fields would hand the writer
// a key no target has, and defaulting it would invent a value the file never
// contained.
func TestCSVSourceCarriesOnlyMappedColumns(t *testing.T) {
	mapping, sourceKey := leadMapping()
	body := "Email,First Name,Internal Notes\na@x.test,Ada,do not import\n"
	src := NewCSVSource(seedCSV(t, body), testCSVKey, ObjectLead, mapping, sourceKey)

	rows, err := src.Rows(context.Background(), ObjectLead, 0, 10)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := rows[0].Fields["email"]; got != "a@x.test" {
		t.Errorf("email = %v, want the mapped value", got)
	}
	if _, ok := rows[0].Fields["Internal Notes"]; ok {
		t.Error("an unmapped column reached Fields")
	}
	if len(rows[0].Fields) != 2 {
		t.Errorf("fields = %v, want exactly the two mapped ones", rows[0].Fields)
	}
	if rows[0].ExternalID != "a@x.test" {
		t.Errorf("external id = %q, want the source-key column's value", rows[0].ExternalID)
	}
}

// A row with no value in the source-key column cannot be identified, so it can
// neither be deduplicated on a re-run nor undone. It is skipped with a reason
// rather than landed under an invented key.
func TestCSVSourceSkipsARowWithNoSourceKey(t *testing.T) {
	mapping, sourceKey := leadMapping()
	body := "Email,First Name\n,Nameless\nb@x.test,B\n"
	src := NewCSVSource(seedCSV(t, body), testCSVKey, ObjectLead, mapping, sourceKey)

	rows, err := src.Rows(context.Background(), ObjectLead, 0, 10)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 1 || rows[0].ExternalID != "b@x.test" {
		t.Fatalf("rows = %+v, want only the identifiable one", rows)
	}
	if got := src.Skipped(); len(got) != 1 || got[0].Line != 2 {
		t.Fatalf("skipped = %+v, want line 2 named", got)
	}
}

func TestCSVSourceCountsRowsAndNamesItsObject(t *testing.T) {
	mapping, sourceKey := leadMapping()
	body := "Email,First Name\na@x.test,A\nb@x.test,B\nc@x.test,C\n"
	src := NewCSVSource(seedCSV(t, body), testCSVKey, ObjectLead, mapping, sourceKey)

	if got := src.Objects(); len(got) != 1 || got[0] != ObjectLead {
		t.Fatalf("Objects() = %v, want just the run's own object", got)
	}
	counts, err := src.Counts(context.Background())
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts[ObjectLead] != 3 {
		t.Fatalf("count = %d, want 3", counts[ObjectLead])
	}
	assoc, err := src.Associations(context.Background())
	if err != nil {
		t.Fatalf("Associations: %v", err)
	}
	if len(assoc) != 0 {
		t.Fatalf("associations = %v, want none: a flat file carries no edges", assoc)
	}
}

func TestCSVSourceReportsAMissingObject(t *testing.T) {
	mapping, sourceKey := leadMapping()
	src := NewCSVSource(seedCSV(t, "Email,First Name\na@x.test,A\n"), testCSVKey, ObjectLead, mapping, sourceKey)

	if _, err := src.Rows(context.Background(), ObjectOrganization, 0, 10); !errors.Is(err, ErrObjectNotInSource) {
		t.Fatalf("err = %v, want ErrObjectNotInSource", err)
	}
}

func TestCSVSourceReportsAVanishedUpload(t *testing.T) {
	mapping, sourceKey := leadMapping()
	src := NewCSVSource(blobstore.NewMemory(), testCSVKey, ObjectLead, mapping, sourceKey)

	if _, err := src.Rows(context.Background(), ObjectLead, 0, 10); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("err = %v, want the blobstore's own not-found", err)
	}
}
