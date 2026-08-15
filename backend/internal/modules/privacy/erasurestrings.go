// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The two string helpers the erasure cascade's steps share.
//
// Split from erasure.go because they belong to no step: every stage that hunts
// for the subject by address normalizes with one and collects with the other,
// so keeping them beside any single stage suggests they are that stage's, and
// keeping them in the cascade file made it the longest file in the package
// while saying nothing about erasure.

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// lowercased normalizes identifiers for SQL ANY matching.
func lowercased(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.ToLower(strings.TrimSpace(v))
	}
	return out
}

func collectStrings(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
