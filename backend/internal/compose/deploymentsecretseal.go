// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Moving a deployment-wide credential out of the process environment and into
// the key vault, once, without asking the operator to do anything.
//
// Two credentials take this path: the outbound-relay password and the license
// token. An installation that declares either one today keeps working and needs
// no action — the value is sealed on the next boot and the declaration becomes
// how the credential ARRIVED rather than where it lives. Once the boot log says
// it is sealed, the operator may delete the declaration: the whole `license:`
// block, or the `password:` line under `email.smtp`. Deleting only what it
// points AT is not the same thing and fails the boot in deployconfig — a named
// source that yields nothing has always been an error there, not an absence.
// Nothing here can delete it for them: the process cannot edit its own
// deployment.
//
// The DECLARATION outranks the sealed copy, which is the opposite of the order
// the BYOK provider keys take, and the difference is not an oversight. A
// provider key has a human write path — the routing surface — so the vault has
// to win or an admin's change would lose to a stale variable. These two have no
// write path at all: no seeded role holds update on either entry, so the sealed
// copy can only ever be a mirror of what the deployment declared. Reading the
// declaration first is therefore what keeps in-place rotation working, and
// re-sealing when it changes is what keeps the mirror honest.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// deploymentSecret is one credential's move into the vault: where its ref is
// recorded, and what to call it when something goes wrong.
type deploymentSecret struct {
	// ref is the settings entry holding the vault ref.
	ref *settings.Entry[string]
	// name is the credential in an operator's words, for the boot log and for
	// the refusal. "the license token", not "license_token_ref".
	name string
	// declaredAt is where the deployment spells the credential, so a refusal
	// names the line to edit rather than the row to inspect.
	declaredAt string
}

// vaultBinding is what a seal needs from the process: somewhere to put a blob,
// somewhere to record the ref, and the workspace both are scoped to. Grouped
// because every function below needs all three and none of them means anything
// alone.
type vaultBinding struct {
	pool  *pgxpool.Pool
	vault keyvault.Vault
	ws    ids.WorkspaceID
	log   *slog.Logger
}

// resolveSecret returns the credential this process should use, and seals it if
// the deployment declared one that the vault does not already hold.
//
// A declared credential always wins, so a boot never fails on the vault while
// the operator is still supplying the value themselves. The vault only has to
// answer once the declaration is gone — which is exactly when a failure to open
// it must be reported AS a vault failure, because by then it is the only copy.
func resolveSecret(ctx context.Context, b vaultBinding, s deploymentSecret, declared string) (string, error) {
	stored, err := settings.Get(ctx, NewSettingsStore(b.pool), s.ref)
	if err != nil {
		return "", fmt.Errorf("compose: reading where %s is sealed: %w", s.name, err)
	}
	if declared != "" {
		b.mirror(ctx, s, stored, declared)
		return declared, nil
	}
	if stored == "" {
		return "", nil
	}
	return b.open(ctx, s, stored)
}

// open reads a sealed credential, and says so in the operator's terms when it
// cannot.
//
// This refusal is the whole reason the sealed copy is safe to depend on. Once
// the declaration is gone the vault holds the only copy, so a vault this process
// cannot reach is not "no license configured" or "no relay password" — it is a
// credential that exists and is out of reach, which is a different problem with
// a different fix. Reporting it as absence would send an operator looking for a
// token they were already issued.
func (b vaultBinding) open(ctx context.Context, s deploymentSecret, stored string) (string, error) {
	secret, err := b.vault.Get(ctx, b.ws, keyvault.Ref(stored))
	if err != nil {
		return "", fmt.Errorf("compose: %s is sealed in the key vault and cannot be opened: %w — "+
			"check the vault root key is the one this installation sealed with", s.name, err)
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("compose: %s is sealed in the key vault but the sealed value is empty — "+
			"re-declare the credential at %s so it is sealed again", s.name, s.declaredAt)
	}
	return string(secret), nil
}

// mirror keeps the sealed copy equal to what the deployment declares.
//
// Best-effort on purpose: the declared value is already in hand and this process
// runs on it whatever happens here, so a vault that refuses must not cost the
// installation its boot. What it costs instead is a log line, which is the only
// thing that tells an operator the variable is not yet safe to remove.
func (b vaultBinding) mirror(ctx context.Context, s deploymentSecret, stored, declared string) {
	if stored != "" && b.opensTo(ctx, stored, declared) {
		return
	}
	// Either nothing is sealed, or the sealed copy has gone stale against a
	// rotated declaration, or it cannot be read at all. All three are repaired
	// the same way: seal the value in hand and repoint the row at it.
	ref, err := b.vault.Put(ctx, b.ws, []byte(declared))
	if err != nil {
		b.log.ErrorContext(ctx, "cannot seal a deployment credential into the key vault; it stays in the deployment configuration for now",
			"credential_name", s.name, "declared_at", s.declaredAt, "error", err)
		return
	}
	superseded, err := b.record(ctx, s, ref, declared)
	if err != nil {
		// The blob is sealed and nothing references it — inert, encrypted at
		// rest, collected by nobody. Loud, because a stranded secret is not a
		// non-event, and delete it rather than leave it for the next boot to
		// strand another.
		b.log.ErrorContext(ctx, "a sealed deployment credential could not be recorded; the deployment configuration is still the source",
			"credential_name", s.name, "declared_at", s.declaredAt, "error", err)
		keyvault.DeleteDetached(ctx, b.vault, b.log, b.ws.UUID, ref, "deployment credential seal failed")
		return
	}
	if superseded == ref {
		// Another role sealed the same value between this one's read and its
		// write, and won. Its blob is the one the row names; the blob sealed
		// here is the duplicate, and deleting it is the whole reason record()
		// reports which ref lost rather than just whether the write happened.
		keyvault.DeleteDetached(ctx, b.vault, b.log, b.ws.UUID, ref, "deployment credential sealed twice concurrently")
		return
	}
	if superseded != "" {
		// The superseded blob is deliberately NOT destroyed, which is the
		// opposite of what the two arms above do, and the difference is the
		// whole point: those delete a duplicate of a value still in hand, while
		// this one would delete a DIFFERENT credential — and once the operator
		// has deleted the declaration, the only copy of it.
		//
		// Three of this design's rules compose badly here. The declaration wins;
		// a re-seal supersedes; and after retirement the vault is the only copy.
		// So anything that puts a wrong value in the declaration for one boot —
		// a stale variable restored from git, a botched pipeline, a value set by
		// whoever can edit the deploy pipeline without touching the file the
		// operator reviews — would irreversibly destroy the real credential.
		// Unsetting the variable does not bring it back; nothing does.
		//
		// The cost of keeping it is an unreferenced blob per genuine rotation,
		// encrypted at rest and reachable by nobody, which is the benign default
		// keyvault.Put already documents for exactly this trade.
		//
		// Not "rotated" in the sentence either: this arm is also reached when the
		// sealed copy could not be READ, and telling an operator their rotation
		// landed when the vault merely hiccuped is the wrong half to be
		// confident about.
		b.log.InfoContext(ctx, "re-sealed a deployment credential into the key vault; the sealed copy did not match the declaration and has been left in place",
			"credential_name", s.name, "declared_at", s.declaredAt)
		return
	}
	// The one sentence that tells an operator they may now delete the
	// declaration — the whole `license:` block, or the `password:` line.
	b.log.InfoContext(ctx, "sealed a deployment credential into the key vault; the deployment configuration that declared it can be deleted",
		"credential_name", s.name, "declared_at", s.declaredAt)
}

// opensTo reports whether the recorded ref already holds this exact value, so a
// boot that has nothing to do does nothing at all — no second blob, no second
// audit row.
func (b vaultBinding) opensTo(ctx context.Context, stored, declared string) bool {
	sealed, err := b.vault.Get(ctx, b.ws, keyvault.Ref(stored))
	return err == nil && string(sealed) == declared
}

// record points the ref row at a freshly sealed blob and reports which ref is
// now unreferenced — the previous one on a rotation, the NEW one when another
// role won the race, and empty when this was the first seal.
//
// The read and the write are one transaction under the advisory lock the
// settings writer itself takes, which is what makes the answer trustworthy. Two
// serving roles boot together on a normal install — `make dev` starts both —
// and without the lock each reads "nothing sealed", each seals its own blob,
// and the loser's ciphertext is stranded in a table no sweeper walks. The lock
// is per-key and re-entrant within the transaction, so SetTx re-taking it below
// costs nothing.
func (b vaultBinding) record(ctx context.Context, s deploymentSecret, ref keyvault.Ref, declared string) (superseded keyvault.Ref, err error) {
	store := NewSettingsStore(b.pool)
	err = store.WriteTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, s.ref.Key()); err != nil {
			return fmt.Errorf("compose: serializing the seal of %s: %w", s.name, err)
		}
		current, err := settings.GetTx(ctx, tx, s.ref)
		if err != nil {
			return err
		}
		if current != "" && b.sealedOnTxEquals(ctx, tx, current, declared) {
			// Somebody else recorded this same value while this role was
			// sealing. Theirs stands; ours is the spare.
			superseded = ref
			return nil
		}
		if err := settings.SetTx(ctx, store, tx, s.ref, string(ref)); err != nil {
			return err
		}
		superseded = keyvault.Ref(current)
		return nil
	})
	if err != nil {
		return "", err
	}
	return superseded, nil
}

// sealedOnTxEquals opens a ref through the caller's own transaction, so the
// comparison sees the same snapshot the lock is protecting rather than a second
// one taken from the pool behind it.
func (b vaultBinding) sealedOnTxEquals(ctx context.Context, tx pgx.Tx, ref, want string) bool {
	sealed, err := b.vault.GetOn(ctx, tx, b.ws, keyvault.Ref(ref))
	return err == nil && string(sealed) == want
}

// secretSealActor names the boot in the audit trail when a credential's ref row
// is written. Distinct from the routing seed's actor: the two write different
// settings for different reasons, and an audit row that could not tell them
// apart would be the only record either one leaves.
const secretSealActor = "deployment-secret-seal"

// smtpPassword and licenseToken are the two credentials that take this path.
var (
	smtpPassword = deploymentSecret{
		ref:        identity.SMTPPasswordRef,
		name:       "the outbound-mail password",
		declaredAt: "email.smtp.password",
	}
	licenseToken = deploymentSecret{
		ref:        identity.LicenseTokenRef,
		name:       "the license token",
		declaredAt: "license.token",
	}
)

// SealedSMTPPassword resolves the relay credential the mailer authenticates
// with, sealing whatever the deployment declared.
//
// An empty result is an unauthenticated relay, which is a posture rather than a
// mistake: it is what an installation that names no password has always had.
func SealedSMTPPassword(ctx context.Context, pool *pgxpool.Pool, vault keyvault.Vault, cfg deployconfig.Config, lookup config.Lookup, log *slog.Logger) (string, error) {
	declared, err := cfg.Email.SMTPPassword(lookup)
	if err != nil {
		return "", err
	}
	return sealedSecret(ctx, pool, vault, smtpPassword, declared, log)
}

// SealedLicenseTokenSource is the license watcher's token source, reading the
// vault where the deployment no longer declares one.
//
// A SOURCE rather than a token, because the watcher re-reads it: a license the
// operator renews in place takes effect on the next re-check instead of waiting
// for a restart, and re-reading is also what keeps the sealed mirror current
// when they renew it that way.
func SealedLicenseTokenSource(ctx context.Context, pool *pgxpool.Pool, vault keyvault.Vault, cfg deployconfig.Config, lookup config.Lookup, log *slog.Logger) func() (string, error) {
	declared := cfg.License.TokenSource(lookup)
	return func() (string, error) {
		token, err := declared()
		if err != nil {
			return "", err
		}
		return sealedSecret(ctx, pool, vault, licenseToken, token, log)
	}
}

// sealedSecret binds the boot principal and hands off to resolveSecret.
//
// An unprovisioned installation (ADR-0105: claimed but not yet bootstrapped)
// has no workspace, so there is nowhere to seal a credential TO and nothing can
// have been sealed before. What the deployment declares is the whole answer,
// and that is not an error — every tenant route answers 503 until the claim
// runs, so a boot that refused here would refuse the very thing that fixes it.
//
// singletonWorkspace documents EnsureInstallation as having already refused a
// multi-workspace database, and on the WORKER that is the api's doing rather
// than this process's — the worker never calls it. ADR-0061 makes a second
// workspace unreachable, so the precondition holds in fact; it is worth saying
// that it holds for a reason outside this call rather than because of one.
func sealedSecret(ctx context.Context, pool *pgxpool.Pool, vault keyvault.Vault, s deploymentSecret, declared string, log *slog.Logger) (string, error) {
	if vault == nil {
		// No vault, which — because keyvault.FromEnv refuses a boot that has
		// sealed secrets and no root key — also means this installation has
		// sealed nothing. So there is no ref to find, the declaration is the
		// whole answer, and saying so costs no database read.
		//
		// That refusal is what makes this safe, rather than an assumption made
		// here: without it a root key dropped in a redeploy would arrive at this
		// line with a recorded ref nobody looked for, and a production boot
		// would call an installation that has a license unlicensed.
		return declared, nil
	}
	ws, err := singletonWorkspace(ctx, pool)
	if err != nil {
		return "", err
	}
	if ws == (ids.UUID{}) {
		return declared, nil
	}
	b := vaultBinding{pool: pool, vault: vault, ws: ids.From[ids.WorkspaceKind](ws), log: log}
	return resolveSecret(bootCtx(ctx, ws, secretSealActor), b, s, declared)
}
