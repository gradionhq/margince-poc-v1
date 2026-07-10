// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package keyvault

import (
	"context"
	"fmt"
	"sync"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// memoryVault is the in-memory Vault: the default for unit tests and the
// offline / zero-toolchain dev path. It copies bytes in and out so a caller
// can neither mutate a stored secret through a returned slice nor have a
// later write reach into an earlier read. Isolation matches the local
// provider's contract: the ref carries its workspace, and a ref presented
// under the wrong workspace answers ErrNotFound before any lookup.
//
// The memory fake mints refs at the current key version — it has no root key
// and does no encryption, but stamps the same version the local provider
// does so a ref round-trips through either provider's parse.
type memoryVault struct {
	mu      sync.RWMutex
	secrets map[Ref][]byte // keyed by the whole ref, which is globally unique
}

// NewMemory returns an in-memory Vault. It is safe for concurrent use.
//
//nolint:ireturn // the seam has two providers (memory + local) behind one Vault; returning the interface is the design.
func NewMemory() Vault {
	return &memoryVault{secrets: make(map[Ref][]byte)}
}

func (m *memoryVault) Put(_ context.Context, ws ids.WorkspaceID, secret []byte) (Ref, error) {
	if ws.IsZero() {
		return "", fmt.Errorf("keyvault: cannot store a secret for a zero workspace id")
	}
	ref, err := mintRef(ws, currentKeyVersion)
	if err != nil {
		return "", err
	}
	stored := make([]byte, len(secret))
	copy(stored, secret)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[ref] = stored
	return ref, nil
}

func (m *memoryVault) Get(_ context.Context, ws ids.WorkspaceID, ref Ref) ([]byte, error) {
	// The structural workspace gate first: a ref from another workspace is
	// ErrNotFound before any lookup, matching the local provider.
	if !ref.scopedTo(ws) {
		return nil, ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	stored, ok := m.secrets[ref]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(stored))
	copy(out, stored)
	return out, nil
}

func (m *memoryVault) Delete(_ context.Context, ws ids.WorkspaceID, ref Ref) error {
	if !ref.scopedTo(ws) {
		// A ref from another workspace addresses nothing here; deleting it is
		// a no-op, not an error (idempotent, like an absent ref).
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.secrets, ref)
	return nil
}

// Health always succeeds: the in-memory vault has no backend to reach.
func (m *memoryVault) Health(_ context.Context) error { return nil }
