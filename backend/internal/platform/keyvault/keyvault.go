// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package keyvault owns secret-material storage behind an opaque,
// workspace-scoped Ref. It is a peer of platform/events and platform/jobs:
// technical plumbing that owns no domain. The DB row stays the system of
// record and the tenant anchor; the vault is custodian of the secret bytes
// only, addressed by a Ref that a domain row (e.g.
// capture_connection.credential_ref) carries in place of the raw
// credential. Isolation is a property of the Ref: a Ref minted for one
// workspace does not resolve under another, so a stolen Ref is inert across
// the tenant edge without also defeating RLS on the row that names it.
//
// Secret hygiene is the load-bearing rule here: the plaintext and the root
// key never reach a log line, an error message, or a model-bound payload.
// Every error names the Ref, never the secret. This composes with — it does
// not replace — model.SecretStripper, which keeps secrets out of model-bound
// payloads at egress; the vault is where secrets live at rest.
package keyvault

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ErrNotFound reports that no secret resolves for the given (workspace, ref)
// pair — either nothing was ever stored, it was deleted, or the ref belongs
// to a different workspace. Callers errors.Is against it. Cross-workspace
// resolution is deliberately indistinguishable from absence (existence
// hiding): a ref presented under the wrong workspace answers ErrNotFound,
// never "wrong tenant".
var ErrNotFound = errors.New("keyvault: secret not found")

// Vault is the secret-material seam. Every call carries the workspace so the
// provider scopes the ref to the tenant; a ref cannot be resolved under the
// wrong workspace. The one substitution axis is a real backend vs the memory
// fake — there is no cross-module provider registry — which is why this lives
// in platform, not shared/ports.
type Vault interface {
	// Put seals secret for ws and returns the opaque Ref addressing it. A
	// zero workspace id is refused: an unscoped secret has no tenant to
	// bind to.
	Put(ctx context.Context, ws ids.WorkspaceID, secret []byte) (Ref, error)

	// Get resolves ref under ws to the stored secret. It returns ErrNotFound
	// if nothing is stored, it was deleted, or ref belongs to another
	// workspace.
	Get(ctx context.Context, ws ids.WorkspaceID, ref Ref) ([]byte, error)

	// Delete removes the secret ref addresses under ws. It is idempotent:
	// deleting an absent ref (or a ref from another workspace) is not an
	// error, so an erasure crash-retry is safe.
	Delete(ctx context.Context, ws ids.WorkspaceID, ref Ref) error

	// Health reports whether the backing store is reachable, feeding the
	// /readyz probe. A nil error means ready.
	Health(ctx context.Context) error
}

// Ref is the opaque handle to one stored secret. It is safe to persist in a
// domain row and safe to log — it is NOT the secret. Its wire form encodes, in
// this order, a fixed scheme tag, the root-key version (carried so a later
// rotation can select the key by version — it cannot be retrofitted onto refs
// already minted), the owning workspace (so a ref presented under the wrong
// workspace is rejected structurally, before any storage read or decryption),
// and an unguessable random token that names the secret.
type Ref string

// refScheme tags every ref this package mints; a ref lacking it is not ours.
const refScheme = "mgv"

// refDelimiter separates ref components. A period cannot occur inside a
// canonical UUID (hyphens only) or a base64url token (RFC 4648 alphabet is
// A–Z a–z 0–9 - _), so splitting on it recovers exactly the four components.
const refDelimiter = "."

// refTokenBytes is the entropy of a ref's random token (128 bits): enough
// that a ref cannot be guessed, so a valid ref for another workspace cannot
// be forged even before the workspace and crypto binding reject it.
const refTokenBytes = 16

// currentKeyVersion is the root-key version every provider stamps into new
// refs today. It is 1 because there is one key; the version travels in the
// ref so a later rotation can add a keyring and pick the key by version
// without changing the ref format or the stored ciphertext.
const currentKeyVersion = 1

// mintRef builds a fresh ref for ws, stamping the current key version. The
// random token is drawn from crypto/rand; a failure there means the process
// cannot mint secret handles and is surfaced, never masked with a predictable
// value.
func mintRef(ws ids.WorkspaceID) (Ref, error) {
	tok := make([]byte, refTokenBytes)
	if _, err := rand.Read(tok); err != nil {
		return "", fmt.Errorf("keyvault: minting a ref token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tok)
	parts := []string{refScheme, strconv.Itoa(currentKeyVersion), ws.String(), token}
	return Ref(strings.Join(parts, refDelimiter)), nil
}

// parsedRef is a ref decomposed into its components.
type parsedRef struct {
	keyVersion int
	workspace  ids.WorkspaceID
	token      string
}

// parse decomposes a ref, validating the scheme and structure. A malformed
// ref is not distinguished from a missing one at the seam boundary (both are
// ErrNotFound to a caller), but parse returns a descriptive error so a
// provider can log the shape problem server-side without echoing the ref's
// token to a client.
func (r Ref) parse() (parsedRef, error) {
	parts := strings.Split(string(r), refDelimiter)
	if len(parts) != 4 || parts[0] != refScheme {
		return parsedRef{}, fmt.Errorf("keyvault: %w", errMalformedRef)
	}
	version, err := strconv.Atoi(parts[1])
	if err != nil || version < 1 {
		return parsedRef{}, fmt.Errorf("keyvault: %w", errMalformedRef)
	}
	ws, err := ids.ParseAs[ids.WorkspaceKind](parts[2])
	if err != nil {
		return parsedRef{}, fmt.Errorf("keyvault: %w", errMalformedRef)
	}
	if parts[3] == "" {
		return parsedRef{}, fmt.Errorf("keyvault: %w", errMalformedRef)
	}
	return parsedRef{keyVersion: version, workspace: ws, token: parts[3]}, nil
}

// scopedTo reports whether ref was minted for ws. A malformed ref is not
// scoped to any workspace. This is the cheap structural gate every provider
// runs first, so a cross-workspace resolution fails before touching storage.
func (r Ref) scopedTo(ws ids.WorkspaceID) bool {
	p, err := r.parse()
	if err != nil {
		return false
	}
	return p.workspace == ws
}

// errMalformedRef marks a ref that does not parse; providers map it to
// ErrNotFound at the boundary (existence hiding) but may log the shape.
var errMalformedRef = errors.New("ref is malformed")
