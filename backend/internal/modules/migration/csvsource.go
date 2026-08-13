// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
)

// The objects a delimited file may carry. `lead` — not `person` — is what a
// bulk prospect file creates: ADR-0008's anti-pollution rule is that
// machine-sourced rows land as leads and are promoted by a human, and IEM-AC-7
// asserts it by number. A file of people already known to the business is
// imported as leads and promoted, not smuggled past qualification by the choice
// of an enum value.
const (
	ObjectLead         = "lead"
	ObjectOrganization = "organization"
	// ConnectorCSV is the direct migrate-in connector this source serves
	// (UC-E11-03). Its sibling constants name the flip's own sources.
	ConnectorCSV = "csv"
)

// ErrObjectNotInSource reports a read for an object this file does not carry.
// One upload is one object; the engine asks per object because a Source may
// span several, and answering rows from the wrong one would import a file into
// a table nobody chose.
var ErrObjectNotInSource = errors.New("object is not in this import source")

// errPageFull stops a walk once the requested page is complete. Internal, and
// never returned to a caller: a page that filled is a success, and reading the
// remaining rows to discover that would make every page cost a whole file.
var errPageFull = errors.New("page full")

// SkippedLine is one row the source could not deliver, named by the line a
// human would open the file to.
type SkippedLine struct {
	Line   int
	Reason string
}

// CSVSource reads an uploaded delimited file out of the blobstore as a
// migration Source.
//
// Why the bytes are durable rather than streamed: Source.Rows pages by absolute
// offset and the engine's checkpoint resumes at one, so the file must be
// re-readable after the request that uploaded it is long gone. Re-opening and
// skipping to the offset is O(rows) per page, which the 10 MB body cap bounds —
// and the file's own order IS the deterministic ordering the resume contract
// requires, with no cursor to store or invalidate.
type CSVSource struct {
	blobs     blobstore.Store
	key       string
	object    string
	mapping   map[string]string
	sourceKey string

	// skipped accumulates rows no page could deliver. Guarded because the
	// engine may page concurrently with a count, and a disclosed skip that
	// races into a torn slice would be a skip nobody can see.
	mu      sync.Mutex
	skipped []SkippedLine
}

// NewCSVSource binds one uploaded file to one object and one mapping.
// sourceKeyColumn names the column that identifies a row for idempotent upsert
// and for undo; the caller decides which column that is, so exactly one place
// makes that choice.
func NewCSVSource(blobs blobstore.Store, key, object string, mapping map[string]string, sourceKeyColumn string) *CSVSource {
	return &CSVSource{blobs: blobs, key: key, object: object, mapping: mapping, sourceKey: sourceKeyColumn}
}

// Objects fixes the import order. One upload is one object, so there is one.
func (s *CSVSource) Objects() []string { return []string{s.object} }

// Counts reports how many rows the file holds, for the run's progress figure.
func (s *CSVSource) Counts(ctx context.Context) (map[string]int, error) {
	rows := 0
	err := s.walk(ctx, func(int, []string, map[string]int) error {
		rows++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]int{s.object: rows}, nil
}

// Associations answers empty, and that is the shape of the source rather than
// an unfinished implementation: a flat file has no edges to detangle. A source
// that carries them (HubSpot, Salesforce) implements this seam with content.
func (s *CSVSource) Associations(context.Context) ([]Assoc, error) { return nil, nil }

// Skipped reports the rows no page could deliver, with the line to open and the
// reason. Never silent: a file half-ignored under a success message is worse
// than a refusal.
func (s *CSVSource) Skipped() []SkippedLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SkippedLine(nil), s.skipped...)
}

// Rows pages the file in its own order. offset is absolute over DELIVERABLE
// rows, which is what the engine checkpoints against.
func (s *CSVSource) Rows(ctx context.Context, object string, offset, limit int) ([]Row, error) {
	if object != s.object {
		return nil, fmt.Errorf("%w: this source carries %q, not %q", ErrObjectNotInSource, s.object, object)
	}
	out := make([]Row, 0, limit)
	delivered := 0
	err := s.walk(ctx, func(line int, record []string, index map[string]int) error {
		row, ok := s.rowFrom(line, record, index)
		if !ok {
			return nil
		}
		position := delivered
		delivered++
		if position < offset {
			return nil
		}
		out = append(out, row)
		if len(out) == limit {
			return errPageFull
		}
		return nil
	})
	if err != nil && !errors.Is(err, errPageFull) {
		return nil, err
	}
	return out, nil
}

// rowFrom builds one Row from one record, or reports why it cannot. A row with
// no value in the source-key column can neither be recognized on a re-run nor
// found by an undo, so it is disclosed rather than landed under an invented id.
func (s *CSVSource) rowFrom(line int, record []string, index map[string]int) (Row, bool) {
	fields := make(map[string]any, len(s.mapping))
	for column, target := range s.mapping {
		at, ok := index[column]
		if !ok || at >= len(record) {
			continue
		}
		if value := strings.TrimSpace(record[at]); value != "" {
			fields[target] = value
		}
	}

	at, ok := index[s.sourceKey]
	external := ""
	if ok && at < len(record) {
		external = strings.TrimSpace(record[at])
	}
	if external == "" {
		s.skip(line, fmt.Sprintf("the %q column is empty, so this row cannot be identified for re-import or undo", s.sourceKey))
		return Row{}, false
	}
	return Row{ExternalID: external, Fields: fields, LastSyncedAt: time.Time{}}, true
}

func (s *CSVSource) skip(line int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, already := range s.skipped {
		if already.Line == line {
			// A page re-read after a resume walks the same rows again; the
			// disclosure is about the FILE, so it is recorded once.
			return
		}
	}
	s.skipped = append(s.skipped, SkippedLine{Line: line, Reason: reason})
}

// walk opens the object and calls visit for each data record, handing it the
// file line (the header is line 1) and the header index. One reader, one place
// that knows the file's framing.
func (s *CSVSource) walk(ctx context.Context, visit func(line int, record []string, index map[string]int) error) error {
	body, _, err := s.blobs.Get(ctx, s.key)
	if err != nil {
		return fmt.Errorf("import source %q: %w", s.key, err)
	}
	defer func() {
		// A close error cannot change what was already parsed, and shadowing
		// the parse result with it would report the wrong failure — so it is
		// logged rather than returned, and never dropped.
		if cerr := body.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing the import source", "key", s.key, "err", cerr)
		}
	}()

	cr := csv.NewReader(body)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: the file has no header row", ErrHeaderInvalid)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSourceUnreadable, err)
	}
	index := make(map[string]int, len(header))
	for i, name := range header {
		index[strings.TrimSpace(name)] = i
	}

	for line := 2; ; line++ {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: line %d: %v", ErrSourceUnreadable, line, err)
		}
		if err := visit(line, record, index); err != nil {
			return err
		}
	}
}
