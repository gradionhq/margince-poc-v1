// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"testing"
)

// resolveCredential's two credential sources are unit-tested here without a
// database: the vault-ref path and the legacy auth-column fallback. The
// full seal→resolve round-trip against real Postgres lives in the
// compose/integration lane.
func TestResolveCredential(t *testing.T) {
	ctx := context.Background()
	r := &Registry{} // no vault wired

	// A row not yet backfilled carries its credential in the auth column;
	// with no ref, that is the credential (no vault needed).
	auth, err := r.resolveCredential(ctx, nil, []byte("legacy-credential"))
	if err != nil {
		t.Fatalf("legacy resolve: %v", err)
	}
	if string(auth) != "legacy-credential" {
		t.Fatalf("legacy resolve returned %q, want the auth-column bytes", auth)
	}

	// A pointer to an empty ref is treated the same as no ref — the legacy
	// column still answers.
	empty := ""
	auth, err = r.resolveCredential(ctx, &empty, []byte("fallback"))
	if err != nil || string(auth) != "fallback" {
		t.Fatalf("empty-ref resolve returned %q, %v; want the fallback bytes", auth, err)
	}

	// A row carrying a credential_ref but no vault to resolve it is a loud
	// refusal, never a silent nil-deref or an empty credential.
	ref := "mgv.1.workspace.token"
	if _, err := r.resolveCredential(ctx, &ref, nil); err == nil {
		t.Fatal("a credential_ref with no keyvault configured must error")
	}
}
