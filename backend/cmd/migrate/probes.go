// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The probe verbs: each answers one yes/no question by PRINTING the answer
// rather than by exit code, so a shell caller can branch on it without
// conflating "the answer is no" with "the command failed". They live together
// because that output contract is the thing they share and the thing a caller
// depends on -- scripts/lib-testdb.sh string-compares db-exists, and the deploy
// entrypoint string-compares org-exists.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
)

// dbExists prints "true" or "false" — output, not exit code, so callers can
// tell "absent" apart from "could not ask" (a connection failure still exits
// non-zero).
func dbExists(ctx context.Context, conn *pgx.Conn, name string, stdout io.Writer) error {
	if name == "" {
		return errors.New("migrate db-exists: --name is required")
	}
	if err := fitsIdentifier(ctx, conn, "migrate db-exists: --name", name); err != nil {
		return err
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return fmt.Errorf("migrate db-exists: probing %q: %w", name, err)
	}
	if _, err := fmt.Fprintf(stdout, "%t\n", exists); err != nil {
		return fmt.Errorf("migrate db-exists: writing the answer: %w", err)
	}
	return nil
}

// orgExists reports whether this installation has been bootstrapped — whether an
// active organization exists — as "true" or "false" on stdout, in the shape
// dbExists above already answers a yes/no question.
//
// It exists so a deployment can tell the two states apart BEFORE the api boots,
// which is what lets an entrypoint stop materializing a bootstrap credential an
// already-provisioned installation will never read (ADR-0061 §2: bootstrap
// values are consumed exactly once, and the section may be deleted once the
// organization exists).
//
// The predicate is the one the api itself applies when it counts organizations
// at boot — archived_at IS NULL — rather than a second spelling of "active" that
// could drift from it.
func orgExists(ctx context.Context, conn *pgx.Conn, stdout io.Writer) error {
	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM workspace WHERE archived_at IS NULL)`).Scan(&exists); err != nil {
		return fmt.Errorf("migrate org-exists: probing for an organization: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "%t\n", exists); err != nil {
		return fmt.Errorf("migrate org-exists: writing the answer: %w", err)
	}
	return nil
}
