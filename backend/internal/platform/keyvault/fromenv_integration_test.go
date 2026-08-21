// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package keyvault_test

// A root key that goes missing while the installation is holding sealed
// ciphertext.
//
// This needs a real database because the whole question is what is in
// vault_secret, and it is the one failure the vault has that nothing else can
// report honestly: every reader downstream discovers it separately and
// describes it in its own dialect, the worst of which calls an installation
// that has a license unlicensed. Refusing here is what lets those readers keep
// treating a nil vault as "nothing was ever sealed".

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// An installation that has sealed nothing is the ordinary case — every
// development and CI process in this repository — and it must keep booting with
// no vault at all. Asserted first, because a refusal that fired here would stop
// every one of them.
func TestNoRootKeyAndNothingSealedIsNotAnError(t *testing.T) {
	pool := singleConnPool(t)

	vault, configured, err := keyvault.FromEnv(t.Context(), pool, config.Static(nil))
	if err != nil {
		t.Fatalf("an installation with no vault and nothing sealed was refused: %v", err)
	}
	if configured || vault != nil {
		t.Errorf("configured=%v vault!=nil=%v, want an absent vault", configured, vault != nil)
	}
}

func TestSealedSecretsWithNoRootKeyRefuseTheBoot(t *testing.T) {
	pool := singleConnPool(t)
	ctx := t.Context()

	sealing, err := keyvault.New(keyvault.Config{RootKey: rootKey(t), Pool: pool})
	if err != nil {
		t.Fatalf("building the sealing vault: %v", err)
	}
	if _, err := sealing.Put(ctx, ids.From[ids.WorkspaceKind](ids.NewV7()), []byte("a credential")); err != nil {
		t.Fatalf("sealing the credential under test: %v", err)
	}

	// The redeploy that dropped MARGINCE_KEYVAULT_ROOT_KEY: same database, same
	// ciphertext, no key.
	_, _, err = keyvault.FromEnv(ctx, pool, config.Static(nil))
	if err == nil {
		t.Fatal("a boot with sealed secrets and no root key was allowed; every credential it holds is unreachable and nothing said so")
	}
	// The variable is named because it is the only thing that fixes this, and
	// "sealed" is named because an operator has to understand that the data is
	// still there and still theirs — the key is what is missing, and it is not
	// recoverable from the ciphertext.
	for _, want := range []string{keyvault.EnvRootKey, "sealed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
}
