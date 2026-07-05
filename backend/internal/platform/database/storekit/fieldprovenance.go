// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// Field-level provenance (B-E02.12): the ONE spelling of the shared
// field_provenance child table — modules stamp and read per-field
// origin through here, never with their own SQL, so the table's shape
// has a single owner. Row-level source/captured_by on the record stays
// the creation default; FieldOrigins falls back to it for fields with
// no field-level row (gate Q3→a: both layers coexist).

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// FieldStamp is one field's origin at capture/enrich time.
type FieldStamp struct {
	Field       string
	Confidence  *float32
	EvidenceRef *string
}

// StampFields records field-level provenance for fields a write set,
// under the SAME source/captured_by the caller just stamped on the row
// itself — the two provenance layers can never name different authors
// for one write.
func StampFields(ctx context.Context, tx pgx.Tx, objectType string, objectID ids.UUID, source, capturedBy string, stamps []FieldStamp) error {
	if len(stamps) == 0 {
		return nil
	}
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return fmt.Errorf("storekit: field provenance outside workspace context")
	}
	for _, s := range stamps {
		if _, err := tx.Exec(ctx,
			`INSERT INTO field_provenance (workspace_id, object_type, object_id, field_name, source, captured_by, confidence, evidence_ref)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			wsID, objectType, objectID, s.Field, source, capturedBy, s.Confidence, s.EvidenceRef); err != nil {
			return fmt.Errorf("storekit: stamp field provenance: %w", err)
		}
	}
	return nil
}

// FieldOrigin is what the provenance display shows for one field.
type FieldOrigin struct {
	Field       string
	Source      string
	CapturedBy  string
	CapturedAt  time.Time
	Confidence  *float32
	EvidenceRef *string
	// FieldLevel is false when the origin fell back to the record's
	// row-level source/captured_by (no field-provenance row exists).
	FieldLevel bool
}

// FieldOrigins answers per-field origin for one record: the latest
// field_provenance row per field, falling back to the record's
// row-level provenance for every requested field without one. The
// caller passes the row-level default it already read (under its own
// row-scope gate) — this helper never widens visibility.
func FieldOrigins(ctx context.Context, tx pgx.Tx, objectType string, objectID ids.UUID, fields []string, rowSource, rowCapturedBy string, rowCapturedAt time.Time) (map[string]FieldOrigin, error) {
	out := make(map[string]FieldOrigin, len(fields))
	for _, f := range fields {
		out[f] = FieldOrigin{
			Field: f, Source: rowSource, CapturedBy: rowCapturedBy, CapturedAt: rowCapturedAt,
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (field_name) field_name, source, captured_by, captured_at, confidence, evidence_ref
		FROM field_provenance
		WHERE object_type = $1 AND object_id = $2 AND field_name = ANY($3)
		ORDER BY field_name, captured_at DESC, id DESC`,
		objectType, objectID, fields)
	if err != nil {
		return nil, fmt.Errorf("storekit: read field provenance: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		o := FieldOrigin{FieldLevel: true}
		if err := rows.Scan(&o.Field, &o.Source, &o.CapturedBy, &o.CapturedAt, &o.Confidence, &o.EvidenceRef); err != nil {
			return nil, err
		}
		out[o.Field] = o
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
