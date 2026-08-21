// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The join between the two halves of the agent seat: identity's bootstrap
// writes it, and the extension-job dispatcher resolves it as the initiator of
// every actor-less tick.
//
// Both ends here are production writers, which is the whole point. Every other
// suite in this package hand-inserts a seat, so each of them proves only that
// the dispatcher accepts the row THAT TEST wrote — a shape bootstrap could
// stop producing without a single failure. The two are only pinned together by
// running the real one against the real other.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
)

func TestTheDispatcherResolvesTheSeatBootstrapMinted(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()

	// The harness seeds its own workspace, and the ADR-0061 state machine binds
	// to a single live one instead of creating anything. Archiving it presents
	// the empty installation bootstrap is written for — archived is exactly how
	// that machine reads "not there".
	if _, err := integration.OwnerConn(t).Exec(ctx,
		`UPDATE workspace SET archived_at = now() WHERE id = $1`, e.WS); err != nil {
		t.Fatalf("archiving the harness workspace: %v", err)
	}

	wsID, created, _, err := identity.NewService(e.Pool).BootstrapInstallation(ctx,
		func() (identity.InstallationBootstrap, error) {
			return identity.InstallationBootstrap{
				OrganizationName: "seatjoin",
				AdminEmail:       "admin@seatjoin.test",
				AdminName:        "Admin",
				AdminPassword:    "a bootstrap password!",
			}, nil
		}, nil)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !created {
		t.Fatal("bootstrap bound to an existing organization instead of creating one, so nothing " +
			"under test ran — the installation was not empty")
	}

	actor, err := extensionJobActor(ctx, e.Pool, wsID.UUID)
	if err != nil {
		t.Fatalf("resolving the extension job actor: %v", err)
	}
	if actor.IsZero() {
		t.Fatal("the dispatcher found no initiator in a freshly bootstrapped installation, so every " +
			"extension tick would be skipped and every scheduled job is dead on arrival — visible only " +
			"as a gauge nobody has a reason to read yet")
	}

	// It is the seat, and not merely some user the query happened to reach
	// first: the admin bootstrap also writes is a live app_user of this
	// workspace, and a predicate that lost its is_agent arm would return it.
	var isAgent bool
	var displayName string
	if err := integration.OwnerConn(t).QueryRow(ctx,
		`SELECT is_agent, display_name FROM app_user WHERE id = $1`, actor).Scan(&isAgent, &displayName); err != nil {
		t.Fatalf("reading the resolved actor: %v", err)
	}
	if !isAgent {
		t.Errorf("the dispatcher resolved %q, a human seat. A tick would then run as a person who "+
			"did not ask for it, with that person's authority", displayName)
	}
}
