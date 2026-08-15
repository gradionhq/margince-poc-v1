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

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// dbExists answers whether a database of that name is present on the cluster.
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

// orgExists answers whether this installation holds an active organization —
// whether it is provisioned. A deployment asks before the api boots, to know
// whether a bootstrap credential is still needed at all (ADR-0061 §2: bootstrap
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

// rotateSetupToken issues a fresh claim credential for an unprovisioned
// installation, retiring whatever was outstanding. It is the operator's way
// back from a setup token lost before first use — without it the
// single-outstanding rule makes that loss permanent, and only hand-written SQL
// against production recovers it.
//
// A CLI and not an HTTP route, for the reason ADR-0061 §4 gives: rotating
// invalidates a live claim credential, which is precisely what an attacker
// wants while the operator still holds one. Reaching it requires the owner DSN.
//
// It opens a pool rather than reusing this command's single connection because
// the identity service owns the rule — the advisory lock, the provisioned
// refusal, the retire-then-issue order — and a second spelling here would drift
// from it.
func rotateSetupToken(ctx context.Context, dsn string, stdout io.Writer) error {
	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		return fmt.Errorf("migrate setup-token: opening a pool: %w", err)
	}
	defer pool.Close()

	raw, err := identity.NewService(pool).RotateSetupToken(ctx)
	if errors.Is(err, identity.ErrAlreadyProvisioned) {
		return errors.New("migrate setup-token: this installation already has an organization — there is nothing left to claim; use `migrate reset-password` to recover an account")
	}
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", raw); err != nil {
		return fmt.Errorf("migrate setup-token: writing the token: %w", err)
	}
	return nil
}
