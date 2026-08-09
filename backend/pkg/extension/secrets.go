// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Secrets and its errors are part of the published extension surface.
//
//margince:extension-surface

package extension

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrSecretNotFound reports that the unit's namespace holds no secret
// under that key, in the scope asked for. A workspace-scoped key and a
// user-scoped key of the same name are different secrets, so Get finding
// one says nothing about the other.
//
// It is also the answer when the mapping row exists but the custodian no
// longer holds the material it names — an absent secret is absent, and
// distinguishing "never stored" from "the vault lost it" would tell a
// caller something it cannot act on differently.
var ErrSecretNotFound = errors.New("extension: no secret is stored under that key")

// UserID names one member of the workspace the call is running in, in the
// canonical hyphenated UUID text form ("0195d3f2-...").
//
// The published surface is stdlib-only (ADR-0069 §3), so this cannot be the
// core's typed id — an extension would then bind the core's kernel as frozen
// published API. It is a distinct named type rather than a bare string so a
// key and a user cannot be swapped at a call site: PutUser(key, userID, ...)
// does not compile. A value that is not a canonical UUID is refused by the
// implementation, not by this type — validation belongs where the workspace
// membership is checked anyway.
type UserID string

// SecretScope is which of the two independent namespaces a secret lives in.
// The values are the ledger's vocabulary too — a system_log detail spells the
// scope with these exact strings.
type SecretScope string

const (
	// SecretScopeWorkspace is the installation's own credential: one value
	// for the whole workspace, typically the API key the unit calls a
	// provider with.
	SecretScopeWorkspace SecretScope = "workspace"

	// SecretScopeUser is one member's own credential at that provider: a
	// separate value per user, reached through the *User methods.
	SecretScopeUser SecretScope = "user"
)

// maxSecretKeyLength bounds a declared key name, and is the same bound the
// store applies to a key at run time — a declaration the store would refuse
// is worse than no declaration, because it reads as a promise.
const maxSecretKeyLength = 128

// SecretsRequest is one secret a unit DECLARES it will use: which key, in
// which scope.
//
// It is a REQUEST recorded in the manifest for operator resolution, exactly
// as a Tool's tier is (see Tool) — declaring a key grants nothing, mints
// nothing and reads nothing. Whether the installation actually holds that
// secret, and who put it there, is an operator's business.
//
// This is inert DATA and not a handle, so it does not contradict the package
// doc's "a declaration holds no handle into the core": the live port is
// Secrets, and a unit only ever gets one from the Runtime the core builds
// for a single invocation. What is declared here is a NAME and a SCOPE,
// which is what lets the generated manifest tell an operator "this unit
// expects a workspace-scoped `signing` key" before the unit ever runs.
type SecretsRequest struct {
	// Key is the unit's own bare key name, the same one it will pass to
	// Secrets.Get/Put — the declaration and the call must agree, or the
	// manifest describes a secret nothing reads.
	Key string

	// Scope is which namespace the key lives in. The zero value is invalid
	// rather than defaulting to workspace: "I did not think about it" and "I
	// want the installation-wide one" must not look the same to an operator
	// reading the manifest.
	Scope SecretScope
}

// Validate enforces what a declaration must state to be readable: a usable
// key name, and one of the two scopes. It is the published check both the
// manifest generator and the boot preflight run, so a declaration that
// reached the composed set outside the generator path is judged the same way.
func (r SecretsRequest) Validate() error {
	switch {
	case strings.TrimSpace(r.Key) == "":
		return errors.New("a declared secret has an empty key name")
	case len(r.Key) > maxSecretKeyLength:
		return fmt.Errorf("declared secret key %q is %d characters — the store bounds a key name at %d", r.Key, len(r.Key), maxSecretKeyLength)
	}
	// Before the rune loop, not after: ranging a Go string decodes an invalid
	// byte to U+FFFD, which is not a control character — so a malformed key
	// passes the loop below and is then written into the manifest and the audit
	// ledger as replacement characters the declaration never spelled. Same
	// ordering, and the same reason, as validateRenderedText in verb.go.
	if !utf8.ValidString(r.Key) {
		return fmt.Errorf("declared secret key %q is not valid UTF-8", r.Key)
	}
	for _, c := range r.Key {
		// The key is echoed into the operator-facing manifest and the audit
		// ledger; a name with an embedded newline has no honest rendering in
		// either.
		if unicode.IsControl(c) {
			return fmt.Errorf("declared secret key %q carries a control character", r.Key)
		}
	}
	if r.Scope != SecretScopeWorkspace && r.Scope != SecretScopeUser {
		return fmt.Errorf("declared secret %q requests scope %q — the scopes are %q and %q", r.Key, string(r.Scope), string(SecretScopeWorkspace), string(SecretScopeUser))
	}
	return nil
}

// Secrets is the extension's own secret namespace, handed to a unit through
// its Runtime. Keys are the unit's own bare names: the implementation closes
// over the invoking unit and scopes every statement by it, so two units may
// both use the key "signing" and neither can read the other's. There is
// deliberately no method taking a unit name — reaching another namespace is
// not something this port can express.
//
// Nor does anything here expose the custodian's handle for a secret. An
// extension addresses a secret by ITS OWN key name and never sees, stores or
// supplies a vault ref: a ref is a capability, and one that reached extension
// code could be persisted somewhere the core cannot revoke.
//
// The two scopes are independent namespaces. The workspace scope is the
// installation's own credential (an API key the unit calls a provider with);
// the user scope is one member's (their personal token at that provider). A
// unit that only ever needs one of them uses only those three methods.
//
// Every call runs against the workspace the invocation is pinned to. There is
// no cross-workspace read, and no parameter through which one could be asked
// for.
//
// ALL OF THE ABOVE IS A PROPERTY OF THIS PORT, and the port is not the only
// road to the rows. Under the tier's threat model (see Runtime) that is the
// right protection: it makes a unit reaching outside its namespace a thing that
// does not compile, which is what stops the mistake. It is not containment. A
// unit that goes around this port — Runtime.Tx reaches extension_secret on the
// shared application role, and in-process Go can read the keyvault root key
// from the environment and decrypt the ciphertext — is not stopped by anything
// here or anywhere else in the tree. Issue #628 is the first change that would
// narrow the first of those.
type Secrets interface {
	// Get returns the workspace-scoped secret stored under key, or
	// ErrSecretNotFound.
	Get(ctx context.Context, key string) ([]byte, error)

	// Put stores secret under key at workspace scope, replacing whatever was
	// there. Rotation is a Put: the superseded material is destroyed once the
	// replacement is durable, so a credential rotated on a schedule does not
	// accumulate.
	Put(ctx context.Context, key string, secret []byte) error

	// Delete removes the workspace-scoped secret under key and destroys its
	// material. Deleting a key that holds nothing is ErrSecretNotFound.
	Delete(ctx context.Context, key string) error

	// GetUser returns the secret stored under key for userID, or
	// ErrSecretNotFound. A userID that is not a member of the calling
	// workspace is an error, never someone else's secret.
	GetUser(ctx context.Context, userID UserID, key string) ([]byte, error)

	// PutUser stores secret under key for userID, with the same
	// replace-and-destroy rotation as Put.
	PutUser(ctx context.Context, userID UserID, key string, secret []byte) error

	// DeleteUser removes userID's secret under key and destroys its material.
	DeleteUser(ctx context.Context, userID UserID, key string) error
}
