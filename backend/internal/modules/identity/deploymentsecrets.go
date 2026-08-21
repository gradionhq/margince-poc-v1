// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Where the two deployment-wide credentials live once they stop living in the
// process environment: the outbound-mail password and the license token.
//
// Both used to be resolved out of margince.yaml or an environment variable on
// every boot, which means both sat in the process environment for the life of
// the process — readable by anything that can read /proc, by a crash dump, and
// by every child the server forks. Sealed in the key vault they are at rest
// under AEAD and reachable only by a process holding the vault's root key.
//
// The REF is what these entries store, never the credential. A ref is opaque
// and workspace-bound — the vault refuses one presented under another
// workspace — so a reader who somehow saw one of these rows learns that a
// credential exists and nothing whatever about it.
//
// Neither has a human write path, and that is deliberate rather than
// unfinished: the deployment still DECLARES these credentials, compose seals
// what it declared, and these rows only record where the sealed copy went. An
// operator ROTATES by changing the declaration, exactly as before.
//
// REMOVING one is the case that changed, and it changed in a direction nobody
// asked for: deleting `email.smtp.password` used to switch the relay back to
// unauthenticated, and now the sealed copy keeps answering, because "declared
// nothing" and "declared nothing AND meant it" are the same input here. There
// is no unseal — no role holds update, the reset spares both rows, and no
// surface deletes a setting. Tracked as issue #2162 rather than papered over;
// the license is unaffected in practice (an installation removing its license
// is one that has stopped paying, not one reconfiguring itself).
//
// Both are marked AsInstallationIdentity, and not because a relay password is
// identity in the sense the installation's name and timezone are. It is a
// pairing with the reset: `vault_secret` is in preservedResetTables, so the
// ciphertext survives a data reset — and an unmarked ref row would not, which
// would leave every sealed deployment credential orphaned and the next boot
// without a license. The two halves have to agree, and this is the half that
// is easy to drop.

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/settings"
)

// SMTPPasswordRefKey and LicenseTokenRefKey are the storage keys. Named
// constants because the seal path in compose logs them: an operator reading
// "sealed installation.license_token_ref" can grep for the entry that
// describes what that is.
const (
	SMTPPasswordRefKey = "installation.smtp_password_ref"
	LicenseTokenRefKey = "installation.license_token_ref"
)

// SMTPPasswordRef holds the vault ref for the outbound relay's password.
//
// Gated by installation_settings, which every seeded role may read. That is
// the right gate and not a compromise: the row holds an opaque ref, no surface
// returns it, and the fact an installation has configured outbound mail is
// already visible to anyone who can be sent a password-reset email.
var SMTPPasswordRef = settings.Define[string](
	SMTPPasswordRefKey,
	installationSettingsObject,
	"update",
	"",
	validateSecretRef,
).AsInstallationIdentity().AsSecretReference()

// LicenseTokenRef holds the vault ref for the installation's entitlement token.
//
// Gated by `license`, the object that already governs the entitlement surface.
// No seeded role holds update on it, so no principal but the boot machinery can
// ever write this row — which is the same posture the object's own declaration
// describes, and it survives this change: sealing is not a human write path.
var LicenseTokenRef = settings.Define[string](
	LicenseTokenRefKey,
	licenseObject,
	"update",
	"",
	validateSecretRef,
).AsInstallationIdentity().AsSecretReference()

// validateSecretRef refuses an empty ref.
//
// An empty string is how these entries read when nothing is sealed, so it is a
// legitimate DEFAULT and an illegitimate stored value: a row written with one
// says a credential was sealed while naming nowhere to find it, and the
// resolver would then fall through to the deployment file believing nothing was
// ever sealed. Blanking the row is therefore not how a credential would be
// unsealed even once something can — deleting it is (issue #2162).
func validateSecretRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("identity: a sealed credential reference cannot be empty; delete the row to unseal")
	}
	return nil
}
