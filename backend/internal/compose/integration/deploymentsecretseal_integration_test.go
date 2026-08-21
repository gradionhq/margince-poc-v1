// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The two deployment credentials leaving the process environment, against a
// real database.
//
// Four things need one and none can be shown with a unit test: that the ref is
// actually recorded, so the boot after the operator drops the declaration finds
// the credential rather than nothing; that sealing is idempotent, or every
// restart strands another copy of the same secret; that a rotated declaration
// re-seals rather than leaving the vault holding a stale credential nobody will
// notice until the declaration is gone; and that a vault which cannot open what
// it holds says exactly that, instead of reporting an installation with a
// license as having none.

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// mailConfig is a deployment that declares a relay password as a reference,
// parsed through the real decoder rather than assembled field by field: Secret
// refuses a literal at decode time, so a hand-built one would be a shape the
// product never produces.
func mailConfig(t *testing.T, passwordVar string) deployconfig.Config {
	t.Helper()
	body := "version: 1\nemail:\n  enabled: true\n  from_address: ops@example.test\n  smtp:\n    host: relay.example.test\n    port: 587\n    username: margince\n"
	if passwordVar != "" {
		body += "    password: ${env:" + passwordVar + "}\n"
	}
	cfg, err := deployconfig.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parsing the deployment file: %v", err)
	}
	return cfg
}

// licenseConfig is a deployment that names its license token in a variable.
func licenseConfig(t *testing.T, tokenVar string) deployconfig.Config {
	t.Helper()
	body := "version: 1\n"
	if tokenVar != "" {
		body += "license:\n  token: ${env:" + tokenVar + "}\n"
	}
	cfg, err := deployconfig.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parsing the deployment file: %v", err)
	}
	return cfg
}

// sealCtx is the boot's own context: no principal at all, because the seal path
// binds its own system actor. A test that supplied an admin would be proving
// something about a caller the product does not have.
func sealCtx() context.Context { return context.Background() }

// readCtx reads the ref rows back as an admin. Only the test does this — no
// product surface returns either row — so it exists to assert the ref was
// RECORDED, which a ref held in memory would not be.
func readCtx(ws ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"installation_settings": {Read: true, Update: true},
				"license":               {Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestTheRelayPasswordIsSealedAndAnsweredFromTheVaultOnceTheDeclarationIsGone(t *testing.T) {
	e := Setup(t)
	vault := keyvault.NewMemory()
	log := slog.New(slog.DiscardHandler)
	env := config.Static(map[string]string{"SMTP_PASSWORD": "a-relay-password"})

	got, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, mailConfig(t, "SMTP_PASSWORD"), env, log)
	if err != nil {
		t.Fatalf("sealing the relay password: %v", err)
	}
	if got != "a-relay-password" {
		t.Fatalf("resolved %q while the deployment still declares the password", got)
	}

	// Recorded, not merely sealed.
	ref, err := settings.Get(readCtx(e.WS), compose.NewSettingsStore(e.Pool), identity.SMTPPasswordRef)
	if err != nil {
		t.Fatalf("reading the recorded ref: %v", err)
	}
	if ref == "" {
		t.Fatal("the relay password was resolved but no vault ref was recorded; the next boot would seal a second copy")
	}

	// The boot after the operator drops the variable: nothing is declared, and
	// the sealed copy is the only one left.
	after, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, mailConfig(t, ""), config.Static(nil), log)
	if err != nil {
		t.Fatalf("reading the sealed relay password: %v", err)
	}
	if after != "a-relay-password" {
		t.Errorf("the vault answered %q, want the sealed password", after)
	}
}

// Idempotent across boots. Without this every restart seals another copy of the
// same secret and strands the previous one.
func TestASecondBootRepointsNothing(t *testing.T) {
	e := Setup(t)
	vault := keyvault.NewMemory()
	log := slog.New(slog.DiscardHandler)
	env := config.Static(map[string]string{"SMTP_PASSWORD": "a-relay-password"})
	cfg := mailConfig(t, "SMTP_PASSWORD")

	if _, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, cfg, env, log); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	first, err := settings.Get(readCtx(e.WS), compose.NewSettingsStore(e.Pool), identity.SMTPPasswordRef)
	if err != nil {
		t.Fatalf("reading the recorded ref: %v", err)
	}
	if _, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, cfg, env, log); err != nil {
		t.Fatalf("second boot: %v", err)
	}
	second, err := settings.Get(readCtx(e.WS), compose.NewSettingsStore(e.Pool), identity.SMTPPasswordRef)
	if err != nil {
		t.Fatalf("reading the recorded ref: %v", err)
	}
	if first != second {
		t.Errorf("a boot that sealed nothing new repointed the ref from %q to %q; the old blob is now stranded", first, second)
	}
}

// A credential rotated in the deployment must reach the vault, or the mirror
// goes stale silently and the operator only discovers it on the boot after they
// drop the declaration — which is the boot that has no other copy.
func TestARotatedDeclarationIsResealed(t *testing.T) {
	e := Setup(t)
	vault := keyvault.NewMemory()
	log := slog.New(slog.DiscardHandler)
	cfg := mailConfig(t, "SMTP_PASSWORD")

	if _, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, cfg,
		config.Static(map[string]string{"SMTP_PASSWORD": "the-old-password"}), log); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if _, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, cfg,
		config.Static(map[string]string{"SMTP_PASSWORD": "the-new-password"}), log); err != nil {
		t.Fatalf("boot after rotation: %v", err)
	}

	after, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, mailConfig(t, ""), config.Static(nil), log)
	if err != nil {
		t.Fatalf("reading the sealed relay password: %v", err)
	}
	if after != "the-new-password" {
		t.Errorf("the vault still answers %q after the deployment rotated the password", after)
	}
}

// The refusal this whole move turns on. Once the declaration is gone the vault
// holds the only copy of the license, so a vault that cannot open it must say
// THAT — an installation told it has no license goes looking for a token it was
// already issued, and in production that refusal blocks the boot.
func TestAnUnopenableVaultNamesItselfRatherThanReportingNoLicense(t *testing.T) {
	e := Setup(t)
	log := slog.New(slog.DiscardHandler)
	cfg := licenseConfig(t, "LICENSE_TOKEN")

	sealed := keyvault.NewMemory()
	source := compose.SealedLicenseTokenSource(sealCtx(), e.Pool, sealed, cfg,
		config.Static(map[string]string{"LICENSE_TOKEN": "a-license-token"}), log)
	if _, err := source(); err != nil {
		t.Fatalf("sealing the license token: %v", err)
	}

	// A different vault is what a rotated or dropped root key looks like from
	// here: the ref is recorded and refers to nothing this process can open.
	lost := compose.SealedLicenseTokenSource(sealCtx(), e.Pool, keyvault.NewMemory(), licenseConfig(t, ""), config.Static(nil), log)
	_, err := lost()
	if err == nil {
		t.Fatal("a vault that cannot open the sealed license reported no license at all")
	}
	for _, want := range []string{"license token", "key vault", "root key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
}
