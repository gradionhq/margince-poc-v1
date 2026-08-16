// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The sales org: teams, seats, and the role assignment that gives a seat any
// permission at all.
//
// This is the one phase that writes SQL rather than going through the API,
// for two reasons that are both product gaps rather than shortcuts:
//
//   - Teams are read-only over the contract (GET /teams and nothing else), so
//     there is no governed path to create one.
//   - A password can only be SET through a single-use link, and the contract
//     floors it at 12 characters. The demo password is "1234", which every
//     demo script and screenshot already assumes, and no API accepts it.
//
// Both are noted in the dataset's STATE.md. Everything else the seeder does
// goes through the API precisely so this stays the exception.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

// Argon2id parameters, mirroring backend/internal/modules/identity/internal/
// password/password.go. That package sits behind a nested `internal` and
// cannot be imported here, so the values are restated — if they change there,
// they must change here, and a demo user failing to log in is the signal.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonSalt    = 16
	argonKey     = 32
)

// hashPassword derives the PHC-formatted Argon2id hash the product verifies
// against.
func hashPassword(plaintext string) (string, error) {
	salt := make([]byte, argonSalt)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, argonTime, argonMemory, argonThreads, argonKey)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// seedOrg creates the teams and seats, and returns how many of each were new.
//
// Re-running never rewrites an existing seat's password: Argon2 salts are
// random, so re-hashing every run would churn every hash for no reason, and
// an operator who changed a demo password by hand would find it silently
// reverted.
func seedOrg(ctx context.Context, conn *pgx.Conn, cfg demoConfig, mode runMode) error {
	if mode == modeDryRun {
		fmt.Printf("\nwould create %d team(s) and %d seat(s)\n", len(cfg.Teams), len(cfg.Users))
		return nil
	}

	var workspace string
	if err := conn.QueryRow(ctx, `SELECT id FROM workspace ORDER BY created_at LIMIT 1`).Scan(&workspace); err != nil {
		return fmt.Errorf("resolving the installation's workspace: %w", err)
	}

	teamIDs, teamsNew, err := ensureTeams(ctx, conn, workspace, cfg.Teams)
	if err != nil {
		return err
	}
	seatsNew, err := ensureUsers(ctx, conn, workspace, cfg, teamIDs)
	if err != nil {
		return err
	}

	fmt.Printf("\nteams:         %d new, %d already present\n", teamsNew, len(cfg.Teams)-teamsNew)
	fmt.Printf("seats:         %d new, %d already present (password %q)\n",
		seatsNew, len(cfg.Users)-seatsNew, cfg.UserPassword)
	return nil
}

func ensureTeams(ctx context.Context, conn *pgx.Conn, workspace string, teams []demoTeam) (map[string]string, int, error) {
	ids := map[string]string{}
	created := 0
	for _, team := range teams {
		var id string
		err := conn.QueryRow(ctx,
			`SELECT id FROM team WHERE workspace_id = $1 AND name = $2`, workspace, team.Name).Scan(&id)
		switch {
		case err == nil:
			ids[team.Ref] = id
			continue
		case err != pgx.ErrNoRows:
			return nil, 0, fmt.Errorf("looking up team %q: %w", team.Name, err)
		}
		if err := conn.QueryRow(ctx,
			`INSERT INTO team (workspace_id, name) VALUES ($1, $2) RETURNING id`,
			workspace, team.Name).Scan(&id); err != nil {
			return nil, 0, fmt.Errorf("creating team %q: %w", team.Name, err)
		}
		ids[team.Ref] = id
		created++
	}
	return ids, created, nil
}

func ensureUsers(ctx context.Context, conn *pgx.Conn, workspace string, cfg demoConfig, teamIDs map[string]string) (int, error) {
	created := 0
	for _, user := range cfg.Users {
		id, isNew, err := ensureSeat(ctx, conn, workspace, user, cfg.UserPassword)
		if err != nil {
			return created, err
		}
		if isNew {
			created++
		}
		if err := assignRole(ctx, conn, workspace, id, user.RoleKey); err != nil {
			return created, err
		}
		if user.Team == "" {
			continue
		}
		teamID, ok := teamIDs[user.Team]
		if !ok {
			return created, fmt.Errorf("user %s names team %q, which demo.json does not define", user.Email, user.Team)
		}
		if _, err := conn.Exec(ctx,
			`INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($1, $2, $3)
			 ON CONFLICT (team_id, user_id) DO NOTHING`, workspace, teamID, id); err != nil {
			return created, fmt.Errorf("adding %s to team %q: %w", user.Email, user.Team, err)
		}
	}
	return created, nil
}

func ensureSeat(ctx context.Context, conn *pgx.Conn, workspace string, user demoUser, password string) (id string, isNew bool, err error) {
	switch err := conn.QueryRow(ctx,
		`SELECT id FROM app_user WHERE workspace_id = $1 AND lower(email) = lower($2)`,
		workspace, user.Email).Scan(&id); {
	case err == nil:
		return id, false, nil
	case err != pgx.ErrNoRows:
		return "", false, fmt.Errorf("looking up seat %s: %w", user.Email, err)
	}

	hash, err := hashPassword(password)
	if err != nil {
		return "", false, err
	}
	if err := conn.QueryRow(ctx,
		`INSERT INTO app_user (workspace_id, email, password_hash, display_name, seat_type, status)
		 VALUES ($1, $2, $3, $4, 'full', 'active') RETURNING id`,
		workspace, user.Email, hash, user.DisplayName).Scan(&id); err != nil {
		return "", false, fmt.Errorf("creating seat %s: %w", user.Email, err)
	}
	return id, true, nil
}

// assignRole gives a seat its permissions. A seat with no role_assignment has
// NONE — every object check fails closed, so it cannot even load a list.
func assignRole(ctx context.Context, conn *pgx.Conn, workspace, userID, roleKey string) error {
	tag, err := conn.Exec(ctx,
		`INSERT INTO role_assignment (workspace_id, role_id, user_id)
		 SELECT $1, r.id, $2 FROM role r
		  WHERE r.workspace_id = $1 AND r.key = $3
		    AND NOT EXISTS (
		          SELECT 1 FROM role_assignment ra
		           WHERE ra.workspace_id = $1 AND ra.user_id = $2 AND ra.role_id = r.id)`,
		workspace, userID, roleKey)
	if err != nil {
		return fmt.Errorf("assigning role %q: %w", roleKey, err)
	}
	// A role key the workspace does not carry inserts nothing and would leave
	// a seat that cannot do anything — worth failing on rather than shipping.
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM role_assignment ra JOIN role r ON r.id = ra.role_id
			  WHERE ra.workspace_id = $1 AND ra.user_id = $2 AND r.key = $3)`,
			workspace, userID, roleKey).Scan(&exists); err != nil {
			return fmt.Errorf("confirming role %q: %w", roleKey, err)
		}
		if !exists {
			return fmt.Errorf("role %q does not exist in this workspace — a seat without one has no permissions at all", roleKey)
		}
	}
	return nil
}

// seedOrgWithDSN opens the one database connection this tool needs and hands
// it to seedOrg. Kept apart from the seeding itself so the SQL exception has
// exactly one door.
func seedOrgWithDSN(dsn string, cfg demoConfig, mode runMode) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting for the org seed: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }() //craft:ignore swallowed-errors closing a read-only seed connection has no failure the caller can act on
	return seedOrg(ctx, conn, cfg, mode)
}
