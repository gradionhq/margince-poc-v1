// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// Field masks: the columns a principal reads as withheld. Object RBAC answers
// whether a role may read a KIND of record, the row scope which ROWS; a mask
// narrows one column of a readable row. It is applied where a store maps a
// row onto the wire — the field goes out null and the record names it in
// masked_fields, so a reader can tell "withheld" from "empty" — and it is
// refused as a sort or filter key, since ordering by a value is reading it.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// MaskedFields answers the columns of one object the principal reads as
// withheld on a row it may or may not change. An unbounded principal and the
// system principal read every column; a mask conditioned on write authority
// lifts on a row the caller could write.
func MaskedFields(p principal.Principal, object string, writable bool) []string {
	if Unbounded(p) {
		return nil
	}
	var out []string
	for _, m := range p.Permissions.FieldMasks {
		if m.Object != object {
			continue
		}
		if m.Condition == principal.MaskOutsideWriteAuthority && writable {
			continue
		}
		out = append(out, m.Field)
	}
	return out
}

// MasksAnyRowOf reports whether the principal carries a mask on the object at
// all — the test a list applies before accepting a sort or filter over a
// maskable column: ordering by a value the caller may not read on some rows
// would disclose it through the order.
func MasksAnyRowOf(ctx context.Context, object, field string) (bool, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return false, err
	}
	if Unbounded(p) {
		return false, nil
	}
	for _, m := range p.Permissions.FieldMasks {
		if m.Object == object && m.Field == field {
			return true, nil
		}
	}
	return false, nil
}

// WritableSubset answers, in ONE statement, which of the given rows of a
// shareable table the caller may SEE and could CHANGE — the pair EnsureWritable
// takes, asked of a page at once so a list can mask per row without a probe
// per row. A row the caller cannot see is absent from the answer whatever
// their write authority, capture privacy included; only a caller who reads the
// table whole with no predicate at all (UnboundedFor) writes every row named.
func WritableSubset(ctx context.Context, tx pgx.Tx, table string, rowIDs []ids.UUID) (map[ids.UUID]bool, error) {
	if !shareableTables[table] {
		return nil, fmt.Errorf("auth: %q is not a shareable table", table)
	}
	p, err := rbacActor(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[ids.UUID]bool, len(rowIDs))
	if UnboundedFor(p, table) && Unbounded(p) {
		for _, id := range rowIDs {
			out[id] = true
		}
		return out, nil
	}
	if len(rowIDs) == 0 {
		return out, nil
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idsPos := arg(rowIDs)
	// Visibility first, the write arm second — the same two questions
	// EnsureWritable asks, in one statement. An unbounded caller's write arm
	// is TRUE and capture privacy is all that can still withhold a row.
	clause := VisiblePredicate(p, table, arg)("") + " AND " + writeAuthorityPredicate(p, table, arg)
	rows, err := tx.Query(ctx,
		fmt.Sprintf(`SELECT id FROM %[1]s WHERE id = ANY($%[2]d) AND %[3]s`, table, idsPos, clause), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
