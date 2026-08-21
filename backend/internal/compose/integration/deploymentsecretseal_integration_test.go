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
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
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

// realVault is the LOCAL provider over the harness pool, not keyvault.NewMemory.
//
// The memory provider's GetOn ignores the querier it is handed, and the seal
// path's one in-transaction read — sealedOnTxEquals, the whole mechanism the
// concurrency fix rests on — is exactly a GetOn through a borrowed transaction.
// Against the fake, swapping it back to a pool read would pass every case here
// and deadlock a one-connection pool in production. The root key is fixed and
// meaningless; these tests assert reachability, never key management.
func realVault(t *testing.T, e *Env) keyvault.Vault {
	t.Helper()
	vault, err := keyvault.New(keyvault.Config{RootKey: bytes.Repeat([]byte{9}, 32), Pool: e.Pool})
	if err != nil {
		t.Fatalf("building the test vault: %v", err)
	}
	return vault
}

// differentVault is the same table under a DIFFERENT root key — which is what a
// key rotated or lost in a redeploy looks like from the reading process: the
// ciphertext is all still there and none of it opens.
func differentVault(t *testing.T, e *Env) keyvault.Vault {
	t.Helper()
	vault, err := keyvault.New(keyvault.Config{RootKey: bytes.Repeat([]byte{4}, 32), Pool: e.Pool})
	if err != nil {
		t.Fatalf("building the test vault: %v", err)
	}
	return vault
}

// sealedBlobCount is the evidence for "exactly one copy survives". vault_secret
// carries no workspace_id, and the harness gives each test its own database, so
// a plain count is the honest question.
func sealedBlobCount(t *testing.T, e *Env) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM vault_secret`).Scan(&n); err != nil {
		t.Fatalf("counting sealed blobs: %v", err)
	}
	return n
}

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
	vault := realVault(t, e)
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
	vault := realVault(t, e)
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
	vault := realVault(t, e)
	log := slog.New(slog.DiscardHandler)
	cfg := mailConfig(t, "SMTP_PASSWORD")

	if _, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, cfg,
		config.Static(map[string]string{"SMTP_PASSWORD": "the-old-password"}), log); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	superseded, err := settings.Get(readCtx(e.WS), compose.NewSettingsStore(e.Pool), identity.SMTPPasswordRef)
	if err != nil {
		t.Fatalf("reading the recorded ref: %v", err)
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
	// The superseded blob SURVIVES, and that is the deliberate answer rather
	// than an omission. A re-seal is triggered by the declaration alone, and the
	// declaration is the thing a stale variable or a botched pipeline gets
	// wrong; destroying what it supersedes would let one bad boot irreversibly
	// destroy the installation's only copy of a credential it never meant to
	// replace. An unreferenced blob is encrypted at rest and reachable by
	// nobody, which is the cheaper of the two failures by a wide margin.
	if _, err := vault.Get(sealCtx(), ids.From[ids.WorkspaceKind](e.WS), keyvault.Ref(superseded)); err != nil {
		t.Errorf("the superseded password is gone from the vault (%v); a re-seal must not destroy a credential the declaration merely disagreed with", err)
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

	sealed := realVault(t, e)
	source := compose.SealedLicenseTokenSource(sealCtx(), e.Pool, sealed, cfg,
		config.Static(map[string]string{"LICENSE_TOKEN": "a-license-token"}), log)
	if _, err := source(); err != nil {
		t.Fatalf("sealing the license token: %v", err)
	}

	// A different vault is what a rotated or dropped root key looks like from
	// here: the ref is recorded and refers to nothing this process can open.
	lost := compose.SealedLicenseTokenSource(sealCtx(), e.Pool, differentVault(t, e), licenseConfig(t, ""), config.Static(nil), log)
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

// Two roles booting at once. `make dev` starts the api and the worker together,
// so the first boot of a keyvault-enabled installation runs this race for real:
// both see nothing sealed, both seal, and without the lock the loser's
// ciphertext is stranded in a table no sweeper walks.
//
// The assertion is the BLOB COUNT, not the ref. A fix that recorded one ref
// while leaving two blobs behind looks identical from the setting row alone,
// and it is the stranded ciphertext that is the defect.
func TestTwoRolesSealingAtOnceLeaveExactlyOneCopy(t *testing.T) {
	e := Setup(t)
	vault := realVault(t, e)
	log := slog.New(slog.DiscardHandler)
	cfg := mailConfig(t, "SMTP_PASSWORD")
	env := config.Static(map[string]string{"SMTP_PASSWORD": "a-relay-password"})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, cfg, env, log)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("role %d: %v", i, err)
		}
	}

	// One, not two: the losing role deletes the duplicate it just sealed. This
	// is the case where deleting IS right — the value is still in hand and
	// another copy of the very same bytes is the one the row names — and it is
	// the opposite of the rotation case above, which keeps what it supersedes.
	if got := sealedBlobCount(t, e); got != 1 {
		t.Errorf("%d sealed blobs after two roles sealed one credential, want 1 — the loser's copy is stranded", got)
	}
	// And the one that survived is the one the row names.
	ref, err := settings.Get(readCtx(e.WS), compose.NewSettingsStore(e.Pool), identity.SMTPPasswordRef)
	if err != nil {
		t.Fatalf("reading the recorded ref: %v", err)
	}
	opened, err := vault.Get(sealCtx(), ids.From[ids.WorkspaceKind](e.WS), keyvault.Ref(ref))
	if err != nil {
		t.Fatalf("the recorded ref does not open: %v", err)
	}
	if string(opened) != "a-relay-password" {
		t.Errorf("the recorded ref opens to %q, want the sealed password", opened)
	}
}

// The ledger records that a credential was sealed, never where it went.
//
// A ref is not the secret, but it is the unguessable capability half of the
// handle — which is why the vault strips it from every error it raises and why
// the data reset refuses to name one in a log. audit_log is a log sink like any
// other: admin-readable over /audit-log and exportable. Careful everywhere else
// and verbatim here is careful nowhere.
func TestTheLedgerRecordsTheSealWithoutTheAddress(t *testing.T) {
	e := Setup(t)
	vault := realVault(t, e)
	log := slog.New(slog.DiscardHandler)

	if _, err := compose.SealedSMTPPassword(sealCtx(), e.Pool, vault, mailConfig(t, "SMTP_PASSWORD"),
		config.Static(map[string]string{"SMTP_PASSWORD": "a-relay-password"}), log); err != nil {
		t.Fatalf("sealing the relay password: %v", err)
	}

	ref, err := settings.Get(readCtx(e.WS), compose.NewSettingsStore(e.Pool), identity.SMTPPasswordRef)
	if err != nil {
		t.Fatalf("reading the recorded ref: %v", err)
	}
	if ref == "" {
		t.Fatal("nothing was sealed; the assertion below would pass vacuously")
	}

	var rows int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE after::text LIKE '%' || $1 || '%' OR before::text LIKE '%' || $1 || '%'`,
		ref).Scan(&rows); err != nil {
		t.Fatalf("scanning the ledger: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d audit row(s) carry the vault ref verbatim; the ledger records that the seal changed, not its address", rows)
	}
	// And the event itself is still on the record — a redaction that audited
	// nothing would pass the assertion above while losing the row that matters.
	if got := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE action = $1`, identity.SMTPPasswordRef.AuditVerb()); got == 0 {
		t.Error("the seal left no audit row at all; redacting the address must not cost the event")
	}
}
