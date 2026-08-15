// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The claim path (ADR-0105). What is worth proving here is not that a valid
// claim works — it is that every way of getting it wrong is refused, and that a
// refusal never leaves the installation unclaimable.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// newSetupService builds the service on the package's shared pools, which
// setupIdentityDB resets per test — so each case below starts from a database
// that holds only what it seeds.
func newSetupService(t *testing.T) *Service {
	t.Helper()
	_, pool := setupIdentityDB(t)
	return NewService(pool)
}

func claimInput(org string) InstallationBootstrap {
	return InstallationBootstrap{
		OrganizationName: org,
		BaseCurrency:     "EUR",
		Timezone:         "Europe/Berlin",
		AdminEmail:       "admin@" + org + ".test",
		AdminName:        "Admin",
		AdminPassword:    "a bootstrap password!",
	}
}

func TestClaimCreatesTheOrganizationAndSpendsTheToken(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	token, err := svc.MintSetupToken(ctx)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	outstanding, err := svc.SetupTokenOutstanding(ctx)
	if err != nil || !outstanding {
		t.Fatalf("outstanding = %v, %v — a freshly minted token must read as claimable", outstanding, err)
	}

	if _, err := svc.ClaimInstallation(ctx, token, claimInput("claimed"), nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Spent, so the same token cannot claim twice even if the organization
	// were somehow removed.
	outstanding, err = svc.SetupTokenOutstanding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if outstanding {
		t.Error("the token is still outstanding after a successful claim — a second claim could reuse it")
	}
}

func TestClaimIsAttributedToTheAdminItCreates(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	token, err := svc.MintSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wsID, err := svc.ClaimInstallation(ctx, token, claimInput("attributed"), nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	var actorType, actorID, adminID string
	if err := database.WithInfraTx(ctx, svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT actor_type, actor_id, detail->>'admin_user_id'
			   FROM system_log
			  WHERE workspace_id = $1 AND action = 'installation_bootstrap'`, wsID).
			Scan(&actorType, &actorID, &adminID)
	}); err != nil {
		t.Fatalf("reading the bootstrap record: %v", err)
	}
	// ADR-0105 §4: the first provisioning event with a real human behind it.
	if actorType != "human" {
		t.Errorf("a claim was recorded as actor_type %q, want \"human\" — someone presented the token and chose their own credential", actorType)
	}
	if actorID != adminID {
		t.Errorf("actor_id %q is not the admin the claim created (%q)", actorID, adminID)
	}
}

func TestConfiguredBootstrapStaysASystemEvent(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	wsID, created, err := svc.BootstrapInstallation(ctx, func() (InstallationBootstrap, error) {
		return claimInput("configured"), nil
	}, nil)
	if err != nil || !created {
		t.Fatalf("bootstrap: created=%v err=%v", created, err)
	}
	var actorType string
	if err := database.WithInfraTx(ctx, svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT actor_type FROM system_log WHERE workspace_id = $1 AND action = 'installation_bootstrap'`, wsID).Scan(&actorType)
	}); err != nil {
		t.Fatal(err)
	}
	// No human signed in, so naming one would make the record assert something
	// false. The claim path is the exception, not the new default.
	if actorType != "system" {
		t.Errorf("a configured bootstrap was recorded as actor_type %q, want \"system\"", actorType)
	}
}

func TestAWrongTokenIsRefusedAndLeavesTheInstallationClaimable(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	if _, err := svc.MintSetupToken(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := svc.ClaimInstallation(ctx, "not-the-token", claimInput("wrongtoken"), nil)
	if !errors.Is(err, ErrSetupTokenMismatch) {
		t.Fatalf("claim with a wrong token returned %v, want ErrSetupTokenMismatch", err)
	}
	// The real credential must still work: a failed guess that burned the token
	// would let anyone lock an operator out of their own installation.
	outstanding, err := svc.SetupTokenOutstanding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !outstanding {
		t.Error("a rejected claim consumed the outstanding token — a wrong guess must not spend the operator's credential")
	}
}

// A claim aimed at a live installation is refused, and refused as ITSELF — a
// caller holding what was once a valid token gets the true reason rather than a
// token failure, because that an installation is provisioned is already visible
// from every other request.
//
// The token is minted BEFORE provisioning because that is now the only order
// the service permits: minting against a live installation is refused outright,
// and provisioning retires whatever was outstanding. Both are the point.
func TestClaimingAProvisionedInstallationIsRefused(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	token, err := svc.MintSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.BootstrapInstallation(ctx, func() (InstallationBootstrap, error) {
		return claimInput("incumbent"), nil
	}, nil); err != nil {
		t.Fatalf("seeding the provisioned installation: %v", err)
	}

	if _, err := svc.ClaimInstallation(ctx, token, claimInput("second"), nil); !errors.Is(err, ErrAlreadyProvisioned) {
		t.Fatalf("claiming a provisioned installation returned %v, want ErrAlreadyProvisioned", err)
	}
	// And minting a fresh one is refused too, so nothing can re-open the claim
	// surface on a live installation.
	if _, err := svc.MintSetupToken(ctx); !errors.Is(err, ErrAlreadyProvisioned) {
		t.Errorf("minting on a provisioned installation returned %v, want ErrAlreadyProvisioned — /setup/status would advertise it as claimable", err)
	}
}

func TestASecondTokenIsNotMintedWhileOneIsOutstanding(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	first, err := svc.MintSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MintSetupToken(ctx); !errors.Is(err, ErrSetupTokenExists) {
		t.Fatalf("second mint returned %v, want ErrSetupTokenExists", err)
	}
	// The first token must still be the credential: a boot that replaced it
	// would invalidate what an operator had already read out of the log.
	if _, err := svc.ClaimInstallation(ctx, first, claimInput("firsttoken"), nil); err != nil {
		t.Fatalf("the originally minted token no longer claims: %v", err)
	}
}

// The two claims 0252 makes in prose, asserted against the database rather than
// trusted. Both are one query in a lane that already has Postgres.

func TestOnlyTheHashOfTheSetupTokenIsStored(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	raw, err := svc.MintSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := database.WithInfraTx(ctx, svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT token_hash FROM setup_token WHERE consumed_at IS NULL`).Scan(&stored)
	}); err != nil {
		t.Fatal(err)
	}
	if stored == raw {
		t.Fatal("the plaintext setup token is stored in the database — a backup would be replayable into a claim")
	}
	sum := sha256.Sum256([]byte(raw))
	if stored != hex.EncodeToString(sum[:]) {
		t.Errorf("stored value is neither the plaintext nor its sha256; the claim path compares hashes, so this row could never match")
	}
}

func TestTheDatabaseRefusesASecondOutstandingToken(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	if _, err := svc.MintSetupToken(ctx); err != nil {
		t.Fatal(err)
	}
	// Straight past the Go-level EXISTS check, which is the arm that loses a
	// race between two booting replicas. The partial unique index is the
	// guarantee; this proves the guarantee, not the check in front of it.
	err := database.WithInfraTx(ctx, svc.db.Pool(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO setup_token (token_hash) VALUES ('a-second-outstanding-token')`)
		return err
	})
	if !storekit.IsUniqueViolation(err) {
		t.Fatalf("a second unconsumed setup token was accepted (err = %v) — two live claim credentials, and revoking one would not revoke the other", err)
	}
}

func TestAConfiguredBootstrapRetiresAnOutstandingToken(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()

	// An unprovisioned boot minted a token; the operator then chose the
	// configured path instead and restarted.
	if _, err := svc.MintSetupToken(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.BootstrapInstallation(ctx, func() (InstallationBootstrap, error) {
		return claimInput("configuredafter"), nil
	}, nil); err != nil {
		t.Fatalf("configured bootstrap: %v", err)
	}
	outstanding, err := svc.SetupTokenOutstanding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if outstanding {
		t.Error("a token minted before a configured bootstrap is still outstanding — /setup/status would advertise a provisioned installation as claimable, and the credential goes live again the moment the organization is archived")
	}
}

// The claim body is untrusted and the route is unauthenticated, so the password
// floor the configured path and the reset endpoint both apply has to hold here
// too. Without it `"admin_password": ""` mints a loginable root account — an
// absent JSON key decodes to exactly that.
func TestAClaimCannotSetAnEmptyOrShortAdminPassword(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string
	}{
		{"absent or empty", ""},
		{"under the floor", "short"},
		{"one below the floor", "elevenchars"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newSetupService(t)
			ctx := context.Background()
			token, err := svc.MintSetupToken(ctx)
			if err != nil {
				t.Fatal(err)
			}
			in := claimInput("weakpw")
			in.AdminPassword = tc.password
			if _, err := svc.ClaimInstallation(ctx, token, in, nil); err == nil {
				t.Fatal("the claim created a root account with a password below the floor")
			}
			// And the refusal must not have spent the credential: an operator
			// mistyping a password cannot be how they lose the installation.
			outstanding, err := svc.SetupTokenOutstanding(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !outstanding {
				t.Error("a rejected claim consumed the setup token")
			}
		})
	}
}
