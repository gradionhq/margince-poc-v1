// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The workspace consumer-mail list over a real Postgres: that an operator can
// correct the shipped baseline in both directions, and that the correction is
// what the capture path actually reads.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// listAdminCtx binds a human with the capture_settings grant this surface rides
// — the same grant that gates the auto-enrich toggle, because an admin who may
// switch enrichment on may certainly say that a domain is a mailbox provider.
// The user is a REAL seeded human, not a synthetic id: an entry is attributed
// to whoever added it, and a fabricated author would fail the foreign key that
// keeps that attribution honest.
func listAdminCtx(ws, user ids.UUID, grant principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"capture_settings": grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestWorkspaceConsumerMailListCorrectsTheBaselineBothWays(t *testing.T) {
	e := Setup(t)
	ctx := listAdminCtx(e.WS, e.Rep1, principal.ObjectGrant{Read: true, Update: true})
	store := capture.NewFreemailDomains(e.Pool)

	// A regional provider the shipped dataset missed, and a domain it wrongly
	// claims — the operator's real customers mail from gmx.de.
	added, err := store.Add(ctx, "Regional-Mail.Example", capture.FreemailKindExtra)
	if err != nil {
		t.Fatalf("adding a missed provider: %v", err)
	}
	if _, err := store.Add(ctx, "mail.gmx.de", capture.FreemailKindNever); err != nil {
		t.Fatalf("carving out a wrong entry: %v", err)
	}

	// The subdomain is stored as the registrable domain, because that is what
	// the matcher keys on — storing mail.gmx.de would never match anything.
	entries, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range entries {
		got[entry.Domain] = entry.Kind
	}
	if got["regional-mail.example"] != capture.FreemailKindExtra {
		t.Errorf("entries = %v, want the added provider lower-cased", got)
	}
	if got["gmx.de"] != capture.FreemailKindNever {
		t.Errorf("entries = %v, want the carve-out stored as its registrable domain", got)
	}

	// The matcher the capture path reads must obey both corrections.
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		matcher, err := capture.MatcherTx(ctx, tx)
		if err != nil {
			return err
		}
		if !matcher.IsConsumer("regional-mail.example") {
			t.Error("the added provider is not treated as consumer mail")
		}
		if matcher.IsConsumer("gmx.de") {
			t.Error("the carve-out did not beat the shipped baseline")
		}
		if !matcher.IsConsumer("gmail.com") {
			t.Error("correcting two domains disarmed the rest of the baseline")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Withdrawing returns the workspace to the baseline's own answer.
	if err := store.Remove(ctx, added.ID); err != nil {
		t.Fatalf("withdrawing an entry: %v", err)
	}
	if err := store.Remove(ctx, added.ID); err != nil {
		t.Fatalf("withdrawing what is already gone must be a no-op, got: %v", err)
	}
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		matcher, err := capture.MatcherTx(ctx, tx)
		if err != nil {
			return err
		}
		if matcher.IsConsumer("regional-mail.example") {
			t.Error("a withdrawn entry still matches")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConsumerMailListRefusesWhatTheMatcherCouldNeverRead(t *testing.T) {
	e := Setup(t)
	ctx := listAdminCtx(e.WS, e.Rep1, principal.ObjectGrant{Read: true, Update: true})
	store := capture.NewFreemailDomains(e.Pool)

	if _, err := store.Add(ctx, "localhost", capture.FreemailKindExtra); err == nil {
		t.Error("a label with no dot is not a mail domain and must be refused")
	}
	if _, err := store.Add(ctx, "acme.example", "maybe"); err == nil {
		t.Error("a kind outside extra|never must be refused")
	}
}
