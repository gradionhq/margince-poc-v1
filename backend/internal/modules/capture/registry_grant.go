// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Granting a connector: the one path by which a human lends a connector their
// authority over their own mailbox. Everything the grant has to establish
// before a credential is stored lives here — live authority, the scope
// intersection, what the provider says it granted — plus the single upsert
// that writes (or re-points) the connection row.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Connect grants one connector under the CALLING human's authority. Two
// guards run here, both at grant time rather than discovered at 3am
// mid-sync: the granting human must still resolve as live authority, and
// a connector demanding scopes they do not hold is refused.
//
// note: the returned id (and the connectionID threaded through SyncOnce and
// the sync-state recording) names a capture_connection row, which the kernel
// does not model as a first-class entity — no kind exists for it, so it stays
// ids.UUID rather than inventing one.
func (r *Registry) Connect(ctx context.Context, name string, auth connector.Auth) (ids.UUID, error) {
	c, err := r.connector(name)
	if err != nil {
		return ids.Nil, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return ids.Nil, errors.New("capture: only a human grants a connector")
	}
	scopes, err := grantedScopes(c, actor, name)
	if err != nil {
		return ids.Nil, err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ids.Nil, errors.New("capture: connector grant outside workspace context")
	}
	if r.vault == nil {
		return ids.Nil, errors.New("capture: no keyvault configured — a connector credential cannot be sealed")
	}
	// A grant lends the granting human's authority to a connector, so that
	// authority has to be live at the moment it is lent — the principal
	// reaching here was built by a transport from a credential minted earlier,
	// and a human deactivated in between must not still be able to lend it.
	// Checked in the registry, the one place every transport passes through, so
	// the invariant does not rest on each caller's own check. It runs before
	// the seal below, so a refusal leaves neither a row nor a stored secret.
	if err := r.requireLiveGrantor(ctx, ws, actor.UserID); err != nil {
		return ids.Nil, err
	}
	// Put-then-commit (like blobstore): seal the credential in the vault
	// first, then commit the row that names it. The row stores the opaque ref;
	// the bytes never touch it. A transaction that fails after the seal strands
	// the blob — nothing references it, so nothing will ever collect it. It is
	// inert (unreferenced and encrypted at rest) and removed by the operational
	// sweep, but it is a stranded secret, not a non-event.
	ref, err := r.vault.Put(ctx, ids.From[ids.WorkspaceKind](ws), []byte(auth))
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: sealing connector credential: %w", err)
	}
	row := connectionUpsert{
		userID:         actor.UserID,
		provider:       name,
		scopes:         scopes,
		ref:            ref,
		accountLabel:   accountLabelFor(ctx, c, auth, name),
		providerScopes: providerScopesFor(ctx, c, auth, name),
	}
	var id ids.UUID
	var priorRef *string
	if err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		id, priorRef, err = upsertConnection(ctx, tx, row)
		return err
	}); err != nil {
		return ids.Nil, fmt.Errorf("capture: storing connection: %w", err)
	}
	// The row now names the fresh ref; a prior ref (a genuine reconnect over
	// an existing row) is unreachable from any row from here on and must be
	// destroyed — the same invariant Disconnect enforces, on the overwrite
	// path rather than the withdraw path. A first-time connect has no prior
	// ref: nothing to delete. The delete runs AFTER commit (put-then-commit's
	// mirror: the row is already safely repointed at the new secret before the
	// old one is destroyed), so it must outlive the request and must not fail
	// it — the reconnect is committed and there is nothing left to undo.
	if priorRef != nil {
		keyvault.DeleteDetached(ctx, r.vault, slog.Default(), ws, keyvault.Ref(*priorRef), "reconnect")
	}
	return id, nil
}

// requireLiveGrantor resolves the granting human against identity's live
// authority. A human who is archived, suspended, or deactivated resolves to
// ErrNotFound, which is the refusal: the grant is denied before anything is
// sealed or written, so nothing has to be undone.
func (r *Registry) requireLiveGrantor(ctx context.Context, ws, userID ids.UUID) error {
	if r.authority == nil {
		return errors.New("capture: no authority resolver configured — a connector grant cannot be checked against live authority")
	}
	if _, err := r.authority.EffectiveRBAC(ctx, ws, userID); err != nil {
		return fmt.Errorf("capture: the granting human's authority does not resolve: %w", err)
	}
	if _, err := r.authority.SeatType(ctx, ws, userID); err != nil {
		return fmt.Errorf("capture: the granting human's seat does not resolve: %w", err)
	}
	return nil
}

// grantedScopes runs the grant-time scope intersection and returns the
// connector's declared scopes as the strings the row freezes. A connector
// demanding a scope the granting human does not hold is refused here rather
// than discovered at 3am mid-sync.
func grantedScopes(c connector.Connector, actor principal.Principal, name string) ([]string, error) {
	declared := c.Descriptor().Scopes
	out := make([]string, 0, len(declared))
	for _, scope := range declared {
		if !actor.Scopes.Has(scope) {
			return nil, fmt.Errorf("capture: connector %s needs scope %q the granting human does not hold: %w",
				name, scope, apperrors.ErrScopeExceeded)
		}
		out = append(out, string(scope))
	}
	return out, nil
}

// accountLabelFor asks the connector to name the account it just authorized.
// Display-only; a connector that cannot name its account simply does not
// implement the seam. This must not fail the connect — a missing label is a
// blank line in the UI, not a lost connection.
func accountLabelFor(ctx context.Context, c connector.Connector, auth connector.Auth, name string) *string {
	labeler, ok := c.(connector.AccountLabeler)
	if !ok {
		return nil
	}
	label, err := labeler.AccountLabel(auth)
	if err != nil {
		slog.WarnContext(ctx, "capture: connector could not name its account", "provider", name, "err", err)
		return nil
	}
	if label == "" {
		return nil
	}
	return &label
}

// providerScopesFor asks the connector what the PROVIDER granted — the
// provider's own vocabulary, distinct from the internal scopes the row freezes
// alongside it. A connector with no consent step to read implements no such
// seam, and a connector that cannot parse its own bundle is a bug worth a log
// line, not a reason to refuse a grant the human already completed: both leave
// the column NULL, which reads as "not known" rather than "nothing granted".
func providerScopesFor(ctx context.Context, c connector.Connector, auth connector.Auth, name string) []string {
	scoper, ok := c.(connector.GrantedScoper)
	if !ok {
		return nil
	}
	granted, err := scoper.GrantedScopes(auth)
	if err != nil {
		slog.WarnContext(ctx, "capture: connector could not report its granted scopes", "provider", name, "err", err)
		return nil
	}
	return granted
}

// rebindsAccount reports whether this grant points the row at a DIFFERENT
// provider account than the one it held.
//
// Both labels must be known for the answer to be yes. A connector that cannot
// name its account — or one whose bundle carried no owner this time — reports
// nothing, and nothing is not evidence of a change: treating it as one would
// throw away the watermark of every routine reauth through such a connector,
// re-reading the whole mailbox each time.
func rebindsAccount(prior, next *string) bool {
	if prior == nil || *prior == "" || next == nil || *next == "" {
		return false
	}
	return *prior != *next
}

// connectionUpsert is one connect transaction's write: the granting human, the
// provider, the internal scopes frozen at grant time and the provider scopes
// actually granted, the ref naming the freshly sealed credential, and the
// display-only account label.
type connectionUpsert struct {
	userID   ids.UUID
	provider string
	scopes   []string
	// providerScopes is the provider's own vocabulary; nil when the connector
	// cannot report it, and stored as NULL rather than an empty list so
	// "unknown" never reads as "nothing".
	providerScopes []string
	ref            keyvault.Ref
	accountLabel   *string
}

// upsertConnection writes (or re-points) the caller's connection row and
// audits the change inside the connect transaction. It returns the row id and
// the credential ref this write superseded — nil for a first-time connect —
// which the caller destroys once the transaction has committed.
func upsertConnection(ctx context.Context, tx pgx.Tx, in connectionUpsert) (ids.UUID, *string, error) {
	// Capture what this (re)connect is about to overwrite, if a row for this
	// (workspace, user, provider) already exists. The FOR UPDATE lock holds the
	// row still for the span of this transaction, so a concurrent
	// disconnect/reconnect on the same row serializes behind this one rather
	// than racing it. Without the credential_ref read the upsert below would
	// silently orphan the previous secret in the vault — every reconnect
	// (including the reauth_required → Reconnect flow) would leak the prior
	// credential; status and account_label are the audit before-image, which
	// only this read can supply.
	var priorRef, priorStatus, priorLabel *string
	if err := tx.QueryRow(ctx, `
		SELECT credential_ref, status, account_label FROM capture_connection
		 WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		   AND user_id = $1 AND provider = $2
		   FOR UPDATE`,
		in.userID, in.provider).Scan(&priorRef, &priorStatus, &priorLabel); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, nil, err
	}
	// The generation bump is what fences a cycle that is still out at the
	// provider: a sync or backfill page holding the old generation commits
	// nothing onto a row this reconnect has re-pointed at a new credential.
	//
	// A rebind additionally throws the watermark away and stamps the new
	// binding: the row is keyed on (workspace, user, provider), so a second
	// account authorized over the first lands on the SAME row, and the previous
	// mailbox's cursor would tell the new one to resume from a point it has
	// never reached — silently skipping everything before it.
	rebound := rebindsAccount(priorLabel, in.accountLabel)
	var id ids.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO capture_connection (workspace_id, provider, user_id, scopes, credential_ref, status, account_label, provider_scopes)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, 'connected', $5, $6)
		ON CONFLICT (workspace_id, user_id, provider)
		DO UPDATE SET credential_ref = EXCLUDED.credential_ref, auth = NULL, status = 'connected', archived_at = NULL,
		              account_label = EXCLUDED.account_label, provider_scopes = EXCLUDED.provider_scopes,
		              generation = capture_connection.generation + 1,
		              sync_cursor = CASE WHEN $7 THEN NULL ELSE capture_connection.sync_cursor END,
		              account_bound_at = CASE WHEN $7 THEN now() ELSE capture_connection.account_bound_at END
		RETURNING id`,
		in.provider, in.userID, in.scopes, string(in.ref), in.accountLabel, in.providerScopes, rebound).Scan(&id); err != nil {
		return ids.Nil, nil, err
	}
	// A grant is a human's deliberate act over their own mailbox, so it is
	// attributed like any other record mutation. A row that already existed is
	// a reconnect (an update over the same connection), never a second create.
	// The images carry the connection, never the credential ref or auth bytes.
	verb, before := "create", map[string]any(nil)
	if priorStatus != nil {
		verb = "update"
		before = connectionAuditImage(in.provider, *priorStatus, priorLabel)
	}
	if err := auditLifecycle(ctx, tx, verb, captureConnectionObject, id, before,
		connectionAuditImage(in.provider, "connected", in.accountLabel)); err != nil {
		return ids.Nil, nil, err
	}
	// A (re)connect starts the scheduling ladder clean: a row parked by
	// reauth_required or degraded by backoff is due immediately with a fresh
	// credential (ADR-0063).
	if _, err := tx.Exec(ctx, `
		UPDATE capture_sync_state
		SET next_sync_at = now(), consecutive_failures = 0, last_error_class = NULL
		WHERE connection_id = $1`, id); err != nil {
		return ids.Nil, nil, err
	}
	return id, priorRef, nil
}
