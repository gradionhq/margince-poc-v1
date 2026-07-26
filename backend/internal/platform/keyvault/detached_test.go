// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package keyvault

// DeleteDetached is the post-commit credential delete every lifecycle change
// shares. Two properties are load-bearing and proven here: it reaches the
// vault even when the caller's context is already dead, and a vault failure
// never escapes to the caller.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ctxHonouringVault is the memory fake made context-sensitive: the memory
// provider ignores ctx entirely, so on its own it cannot tell a detached
// delete from one that simply inherited a cancelled request. This wrapper
// refuses a delete on a dead context exactly as the local (Postgres-backed)
// provider does, which is what makes the cancellation case meaningful.
type ctxHonouringVault struct {
	Vault
	failWith error
}

func (v *ctxHonouringVault) Delete(ctx context.Context, ws ids.WorkspaceID, ref Ref) error {
	if v.failWith != nil {
		return v.failWith
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.Vault.Delete(ctx, ws, ref)
}

// errorLogger returns a logger writing ERROR records into buf, so a test can
// prove the failure was reported rather than swallowed.
func errorLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestDeleteDetachedOutlivesACancelledCaller(t *testing.T) {
	memory := NewMemory()
	vault := &ctxHonouringVault{Vault: memory}
	ws := ids.New[ids.WorkspaceKind]()
	ref, err := memory.Put(context.Background(), ws, []byte("superseded-credential"))
	if err != nil {
		t.Fatalf("sealing the fixture credential: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var logged bytes.Buffer
	DeleteDetached(ctx, vault, errorLogger(&logged), ws.UUID, ref, "reconnect")

	if _, err := memory.Get(context.Background(), ws, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the credential survived a delete on a cancelled caller: got %v, want ErrNotFound", err)
	}
	if logged.Len() != 0 {
		t.Fatalf("a successful detached delete logged at ERROR: %s", logged.String())
	}
}

func TestDeleteDetachedReportsAFailureWithoutRaisingIt(t *testing.T) {
	memory := NewMemory()
	vault := &ctxHonouringVault{Vault: memory, failWith: errors.New("vault unreachable")}
	ws := ids.New[ids.WorkspaceKind]()
	ref, err := memory.Put(context.Background(), ws, []byte("superseded-credential"))
	if err != nil {
		t.Fatalf("sealing the fixture credential: %v", err)
	}

	var logged bytes.Buffer
	DeleteDetached(context.Background(), vault, errorLogger(&logged), ws.UUID, ref, "disconnect")

	// The caller learns nothing (there is nothing it could undo), but the
	// orphaned blob is announced for operational cleanup.
	if _, err := memory.Get(context.Background(), ws, ref); err != nil {
		t.Fatalf("the fixture credential should still be present after a failed delete: %v", err)
	}
	if logged.Len() == 0 {
		t.Fatal("a failed detached delete was swallowed: nothing logged at ERROR")
	}
	if !bytes.Contains(logged.Bytes(), []byte(string(ref))) {
		t.Fatalf("the failure log does not name the orphaned ref: %s", logged.String())
	}
}

func TestDeleteDetachedRefusesAMissingVaultLoudly(t *testing.T) {
	ws := ids.New[ids.WorkspaceKind]()
	ref := Ref("mgv.1." + ws.String() + ".token")

	var logged bytes.Buffer
	DeleteDetached(context.Background(), nil, errorLogger(&logged), ws.UUID, ref, "disconnect")

	if logged.Len() == 0 {
		t.Fatal("a ref with no vault to delete it is a wiring fault and must be reported at ERROR")
	}
}

func TestDeleteDetachedIgnoresAnEmptyRef(t *testing.T) {
	var logged bytes.Buffer
	// No vault, no ref: nothing was ever sealed, so there is nothing to
	// report and nothing to reach for.
	DeleteDetached(context.Background(), nil, errorLogger(&logged), ids.New[ids.WorkspaceKind]().UUID, "", "reconnect")
	if logged.Len() != 0 {
		t.Fatalf("an empty ref is not a fault: %s", logged.String())
	}
}
