// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The claim path (ADR-0105). What is worth proving here is not that a valid
// claim works — it is that every way of getting it wrong is refused, and that a
// refusal never leaves the installation unclaimable.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
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

// archiveWorkspaces clears the harness's own organization so the service under
// test sees the unprovisioned state a real first boot sees.
func archiveWorkspaces(t *testing.T, svc *Service) {
	t.Helper()
	if err := database.WithInfraTx(context.Background(), svc.db.Pool(), func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE workspace SET archived_at = now() WHERE archived_at IS NULL`)
		return err
	}); err != nil {
		t.Fatalf("clearing the harness workspace: %v", err)
	}
}

func TestClaimCreatesTheOrganizationAndSpendsTheToken(t *testing.T) {
	svc := newSetupService(t)
	archiveWorkspaces(t, svc)
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
	archiveWorkspaces(t, svc)
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
	archiveWorkspaces(t, svc)
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
	archiveWorkspaces(t, svc)
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

func TestClaimingAProvisionedInstallationIsRefusedWithoutSpendingTheToken(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()
	// Provision it the configured way first, so the claim below meets a real
	// organization rather than an empty database.
	if _, _, err := svc.BootstrapInstallation(ctx, func() (InstallationBootstrap, error) {
		return claimInput("incumbent"), nil
	}, nil); err != nil {
		t.Fatalf("seeding the provisioned installation: %v", err)
	}

	token, err := svc.MintSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ClaimInstallation(ctx, token, claimInput("second"), nil)
	if !errors.Is(err, ErrAlreadyProvisioned) {
		t.Fatalf("claiming a provisioned installation returned %v, want ErrAlreadyProvisioned — a valid token deserves the true reason", err)
	}
	outstanding, err := svc.SetupTokenOutstanding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !outstanding {
		t.Error("the token was spent by a claim that created nothing")
	}
}

func TestASecondTokenIsNotMintedWhileOneIsOutstanding(t *testing.T) {
	svc := newSetupService(t)
	archiveWorkspaces(t, svc)
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
