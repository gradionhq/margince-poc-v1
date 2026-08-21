// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The first-party Agent Runner identity bootstrap writes (seed-and-fixtures
// §1.5) — the seat that work nobody requested answers as.
//
// What the row must NOT carry matters as much as what it must, so both halves
// are asserted: a seat that acquired a password would be a login with no person
// to administer it, and one that acquired a role would be a standing grant no
// passport ever bounded — which is precisely the ambient authority "agent ≤
// human" exists to deny. The second test holds the first of those closed
// against the one endpoint that can still open it.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const agentSeatAdminPassword = "a bootstrap password!"

// agentSeatRow is the seat as the schema holds it. The password is asked about
// rather than read out because absence is the assertion — a hash must not be
// selected into a test's memory to be tested for.
type agentSeatRow struct {
	id          ids.UUID
	email       string
	displayName string
	status      string
	seatType    string
	hasPassword bool
	archived    bool
}

// bootstrapForAgentSeat creates one installation through the real writer and
// returns it with the slug the seat's address derives from.
func bootstrapForAgentSeat(t *testing.T, pool *pgxpool.Pool) (ids.WorkspaceID, string) {
	t.Helper()
	ctx := context.Background()
	// The test database persists across binary runs, so the slug — and the seat
	// address derived from it — carries the id's random tail to stay unique.
	slug := "agentseat-" + ids.NewV7().String()[24:]
	var wsID ids.WorkspaceID
	err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		wsID, err = createInstallation(ctx, tx, InstallationBootstrap{
			OrganizationName: slug,
			AdminEmail:       "admin@" + slug + ".test",
			AdminName:        "Admin",
			AdminPassword:    agentSeatAdminPassword,
		}, originConfigured, nil, &[]string{})
		return err
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return wsID, slug
}

// readAgentSeats reads one workspace's agent seats, naming the workspace
// explicitly: the owner connection is a superuser, which row-level security
// does not filter, so an unqualified read answers with every workspace's rows.
func readAgentSeats(t *testing.T, owner *pgx.Conn, wsID ids.WorkspaceID) []agentSeatRow {
	t.Helper()
	rows, err := owner.Query(context.Background(),
		`SELECT id, email, display_name, status, seat_type,
		        password_hash IS NOT NULL, archived_at IS NOT NULL
		   FROM app_user WHERE is_agent`)
	if err != nil {
		t.Fatalf("reading the agent seats: %v", err)
	}
	seats, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (agentSeatRow, error) {
		var s agentSeatRow
		return s, r.Scan(&s.id, &s.email, &s.displayName, &s.status,
			&s.seatType, &s.hasPassword, &s.archived)
	})
	if err != nil {
		t.Fatalf("collecting the agent seats: %v", err)
	}
	return seats
}

// theAgentSeat asserts the workspace holds exactly one and returns it.
func theAgentSeat(t *testing.T, owner *pgx.Conn, wsID ids.WorkspaceID) agentSeatRow {
	t.Helper()
	seats := readAgentSeats(t, owner, wsID)
	if len(seats) != 1 {
		t.Fatalf("the workspace holds %d agent seat(s), want exactly 1 — with none, every actor-less "+
			"job has no initiator to name and is skipped; with more, which one initiates them is "+
			"arbitrary", len(seats))
	}
	return seats[0]
}

func TestBootstrapMintsAnAgentSeatThatCarriesNoAuthorityOfItsOwn(t *testing.T) {
	owner, pool := setupIdentityDB(t)
	wsID, slug := bootstrapForAgentSeat(t, pool)
	seat := theAgentSeat(t, owner, wsID)

	if want := agentSeatEmail(slug); seat.email != want {
		t.Errorf("seat address = %q, want %q (seed-and-fixtures §1.5)", seat.email, want)
	}
	if seat.displayName != "Gradion Agent" {
		t.Errorf("seat display name = %q, want %q — it is what a human reads beside a record the "+
			"runner owns", seat.displayName, "Gradion Agent")
	}
	if seat.status != "active" || seat.archived {
		t.Errorf("seat status = %q, archived = %v; the dispatcher resolves an initiator by "+
			"`is_agent AND status = 'active' AND archived_at IS NULL`, so anything else leaves the "+
			"installation seatless with a seat sitting in the table", seat.status, seat.archived)
	}
	if seat.seatType != "full" {
		t.Errorf("seat type = %q, want \"full\" — the schema admits no other value for an agent "+
			"(app_user_agent_is_full); a read ceiling reaches an agent through the human it acts "+
			"for, never through its own row", seat.seatType)
	}
	if seat.hasPassword {
		t.Error("the agent seat carries a password hash, which makes an identity with no person " +
			"behind it a set of credentials somebody must administer and rotate")
	}

	var grants int
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM role_assignment WHERE user_id = $1`,
		seat.id).Scan(&grants); err != nil {
		t.Fatalf("counting the seat's role assignments: %v", err)
	}
	if grants != 0 {
		t.Errorf("the agent seat holds %d role assignment(s). The seat is an identity, not an "+
			"authority: what an agent may do is the passport granting it intersected with the human "+
			"that passport names, so a role here is a standing grant nobody asked for and nothing "+
			"revokes", grants)
	}
}

// The seat is listed in the roster, so an admin can reach it with every member
// action — and one of them mints a credential. Login already refuses a seat with
// no hash and forgot-password cannot even find it (that lookup requires an
// existing hash), which leaves the admin-issued link as the only way a machine
// identity could come to hold a password.
func TestNoSetPasswordLinkCanBeIssuedForTheAgentSeat(t *testing.T) {
	owner, pool := setupIdentityDB(t)
	wsID, slug := bootstrapForAgentSeat(t, pool)
	// Bound to the workspace this test just bootstrapped: the suite seeds one
	// per test, so there is no installation singleton to resolve.
	svc := NewServiceFor(database.BindTo(pool, wsID))
	// Workspace AND correlation id, as the HTTP middleware binds both. Without
	// the correlation id the write shape refuses at the audit row, so a missing
	// guard would fail this test on the wrong error and the assertions below
	// would never be reached — the refusal has to be what stops it.
	wsCtx := principal.WithCorrelationID(
		principal.WithWorkspaceID(context.Background(), wsID.UUID), ids.NewV7())

	// The admin's real Identity, resolved the way the HTTP surface resolves it:
	// the refusal has to hold for a caller who passes every other gate.
	admin, _, err := svc.Login(wsCtx, "admin@"+slug+".test", agentSeatAdminPassword)
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	seat := theAgentSeat(t, owner, wsID)

	_, _, err = svc.IssuePasswordLink(wsCtx, admin, ids.From[ids.UserKind](seat.id))
	if !errors.Is(err, errAgentSeatHasNoPassword) {
		t.Fatalf("issuing a set-password link for the agent seat returned %v, want the agent-seat "+
			"refusal. Redeeming that link would give an identity with no person behind it a working "+
			"credential, and every session opened with it would read as the agent", err)
	}

	// And nothing was written on the way to the refusal: no token to redeem
	// later, and the seat still holds no password.
	var tokens int
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM auth_token WHERE user_id = $1 AND used_at IS NULL`, seat.id).Scan(&tokens); err != nil {
		t.Fatalf("counting the seat's live tokens: %v", err)
	}
	if tokens != 0 {
		t.Errorf("the refused issue left %d live token(s) for the agent seat; a refusal that still "+
			"mints the credential refuses nothing", tokens)
	}
	if theAgentSeat(t, owner, wsID).hasPassword {
		t.Error("the agent seat acquired a password hash during a refused issue")
	}
}
