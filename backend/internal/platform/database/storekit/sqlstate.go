// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// The SQLSTATEs the stores branch on, named once.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
	pgExclusionViolation  = "23P01"
)

// pgViolation names the violated constraint when err is the given
// SQLSTATE class — the single spelling of "which constraint fired".
func pgViolation(err error, code string) (constraint string, ok bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == code {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// IsUniqueViolation detects the 23505 dedupe path (409 + existing id).
func IsUniqueViolation(err error) bool {
	_, ok := UniqueViolation(err)
	return ok
}

// UniqueViolation names the violated constraint of a 23505, so callers
// can tell an email/domain dedupe hit from an unrelated uniqueness rule
// (e.g. the one-primary-email index) instead of mislabeling both as
// duplicates.
func UniqueViolation(err error) (constraint string, ok bool) {
	return pgViolation(err, pgUniqueViolation)
}

func IsForeignKeyViolation(err error) bool {
	_, ok := pgViolation(err, pgForeignKeyViolation)
	return ok
}

// ExclusionViolation names a fired EXCLUDE constraint — the overlap
// guards (double-booking) map it to their domain conflict.
func ExclusionViolation(err error) (constraint string, ok bool) {
	return pgViolation(err, pgExclusionViolation)
}

// CheckViolation exposes a fired CHECK constraint's name so the transport
// can answer a typed 422 instead of an opaque 500 — the defense-in-depth
// net under the per-path validations: a CHECK is a business rule, and a
// business-rule breach is never a server fault.
func CheckViolation(err error) (constraint string, ok bool) {
	return pgViolation(err, pgCheckViolation)
}
