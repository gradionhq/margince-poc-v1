// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Registry holds the compiled-in connector set and owns the two
// authority rules of the capture path: the grant-time scope
// intersection (a connector's declared scopes ⊆ the granting human's)
// and the run-time connector principal (built from the granting
// human's LIVE authority — a demoted human instantly narrows every
// connector they granted, exactly like passports).
type Registry struct {
	mu         sync.RWMutex
	connectors map[string]connector.Connector
	pool       *pgxpool.Pool
	sink       *Sink
	authority  authz.Resolver
	// vault seals and resolves a connection's credential bundle. The row
	// carries an opaque credential_ref, never the credential bytes; the vault
	// is the custodian. May be nil for a role composed before WithKeyvault
	// wires one: Connect then refuses loudly (it must seal), and SyncOnce
	// refuses only for a row whose credential lives in the vault — a
	// not-yet-backfilled legacy row still resolves from its auth column with
	// no vault.
	vault keyvault.Vault

	// The scheduling state machine's knobs (ADR-0063): now is injected so
	// the backoff/pacing arithmetic is testable; syncInterval paces a
	// healthy connection (next_sync_at = success + interval);
	// progressPacing paces the running page's live tally write.
	now            func() time.Time
	syncInterval   time.Duration
	progressPacing time.Duration
}

// defaultSyncInterval paces a healthy connection between syncs; the push
// webhook (when live) makes this the safety net, not the latency floor.
const defaultSyncInterval = 2 * time.Minute

// NewRegistry builds the connector registry over the pool, the capture Sink,
// the live-authority resolver, and the keyvault that seals/resolves each
// connection's credential. vault may be nil for a role composed before its
// custodian is wired (WithKeyvault rebuilds the registry once it is).
func NewRegistry(pool *pgxpool.Pool, sink *Sink, authority authz.Resolver, vault keyvault.Vault) *Registry {
	return &Registry{
		connectors:     map[string]connector.Connector{},
		pool:           pool,
		sink:           sink,
		authority:      authority,
		vault:          vault,
		now:            time.Now,
		syncInterval:   defaultSyncInterval,
		progressPacing: defaultProgressPacing,
	}
}

// WithSyncInterval overrides the healthy-connection pacing (the worker's
// --gmail-sync-interval flag lands here).
func (r *Registry) WithSyncInterval(d time.Duration) *Registry {
	if d > 0 {
		r.syncInterval = d
	}
	return r
}

// WithProgressPacing overrides how often a running backfill page writes its
// live tally. Zero means every report is written — the pacing exists to keep a
// long import from writing one row update per message, so removing it is only
// sensible when the pages are short enough that the volume is not the point.
func (r *Registry) WithProgressPacing(d time.Duration) *Registry {
	r.progressPacing = d
	return r
}

// Register adds one connector at composition time.
func (r *Registry) Register(c connector.Connector) {
	desc := c.Descriptor()
	if desc.Name == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("capture: registering a connector with no name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.connectors[desc.Name]; dup {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("capture: duplicate connector %s", desc.Name))
	}
	r.connectors[desc.Name] = c
}

// Connectors lists the registered surface, stably ordered.
func (r *Registry) Connectors() []connector.Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]connector.Descriptor, 0, len(r.connectors))
	for _, c := range r.connectors {
		out = append(out, c.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SyncOnce runs one incremental sync for a connection: builds the
// connector principal from the granting human's live authority, hands
// the connector the sink, and advances the stored cursor only when the
// sync succeeded end to end.
//
// The generation read here fences the commit. A sync spends real time at the
// provider, and its human can disconnect or reconnect in that window; the
// watermark and the health verdict this cycle produced belong to a connection
// that no longer exists, so neither is written. That is not a failure — nothing
// went wrong — and it is not a success either.
func (r *Registry) SyncOnce(ctx context.Context, connectionID ids.UUID) error {
	var (
		name          string
		grantedBy     ids.UserID
		credentialRef *string
		authBytes     []byte
		cursor        []byte
		generation    int
	)
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		// 'error' is syncable by design (ADR-0063): the daily probe of a
		// degraded connection runs through this same path, and its success
		// is what flips the row back to connected. Only 'disconnected' and
		// 'reauth_required' park a connection.
		return tx.QueryRow(ctx, `
			SELECT provider, user_id, credential_ref, auth, sync_cursor, generation FROM capture_connection
			WHERE id = $1 AND status IN ('connected','error')`, connectionID).
			Scan(&name, &grantedBy, &credentialRef, &authBytes, &cursor, &generation)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("capture: connection %s: %w", connectionID, apperrors.ErrNotFound)
	}
	if err != nil {
		return err
	}
	c, err := r.connector(name)
	if err != nil {
		return err
	}
	// The connector principal is built before credential resolution so every
	// failure past this point records into the scheduling state under an
	// actor-bearing context (the sidecar's system_log line needs one).
	runCtx, err := r.connectorContext(ctx, name, grantedBy)
	if err != nil {
		return err
	}
	auth, err := r.resolveCredential(ctx, credentialRef, authBytes)
	if err != nil {
		if recErr := r.recordSyncFailure(runCtx, connectionID, err); recErr != nil {
			return errors.Join(err, recErr)
		}
		return err
	}

	// The own-domain set is seeded BEFORE a single message is pulled, and this
	// ordering is the whole control rather than a detail of it. Seeding from
	// what the sync RETURNS means the first sync of a newly connected mailbox
	// — the one carrying the bounded backfill, the largest batch this system
	// ever ingests — runs against an empty set and captures every internal
	// message in it. A rule that only holds from the second sync onward is not
	// a confidentiality control (ADR-0082/A127 §2).
	if err := r.seedOwnDomainFromAccount(runCtx, c, auth); err != nil {
		return err
	}

	next, syncErr := c.Sync(runCtx, auth, connector.Cursor(cursor), r.sink)
	if syncErr != nil {
		// A transient failure never kills the connection (ADR-0063): the
		// state machine classifies, backs off, degrades to a daily probe at
		// worst — and auth parks the row for its human.
		if recErr := r.recordSyncFailure(runCtx, connectionID, syncErr); recErr != nil {
			return errors.Join(syncErr, recErr)
		}
		return syncErr
	}
	superseded, err := r.commitSyncCursor(ctx, connectionID, generation, next)
	if err != nil {
		return err
	}
	if superseded {
		// Everything this cycle learned belongs to the connection as it was, so
		// none of it lands — including the health verdict. Recording a success
		// here would tell a human their just-disconnected mailbox is syncing
		// fine, which is the same lie the fence exists to stop.
		slog.InfoContext(ctx, "capture: sync superseded by a lifecycle change — its cursor and health were not recorded",
			"connection_id", connectionID, "provider", name)
		return nil
	}
	return r.recordSyncSuccess(ctx, connectionID)
}

// commitSyncCursor advances a connection's watermark to what the sync just
// returned. The own-domain seed happens BEFORE the pull, not here — a set
// learned from a completed sync does not cover that sync.
//
// generation is the fence: it is the value SyncOnce read before the pull, and a
// lifecycle change since then has moved it. superseded=true says the write
// matched nothing for that reason — the caller records neither the watermark nor
// a health verdict, because the cycle belongs to a connection that is gone.
func (r *Registry) commitSyncCursor(ctx context.Context, connectionID ids.UUID, generation int, next connector.Cursor) (superseded bool, err error) {
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		// sync_cursor is jsonb; the connector's watermark is already JSON. A
		// connector that yields no cursor writes NULL, never an empty jsonb.
		var cur []byte
		if len(next) > 0 {
			cur = []byte(next)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE capture_connection SET sync_cursor = $2
			WHERE id = $1 AND generation = $3`, connectionID, cur, generation)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			superseded = true
			return nil
		}
		return nil
	})
	return superseded, err
}

// seedOwnDomainFromAccount registers the connected mailbox's own domain before
// any message is read, from the connector's account label.
//
// WHETHER that domain may govern storage depends on who vouches for the label.
// A provider-attested one (OAuth: Gmail, Graph, Calendar) is recorded verified
// and takes effect immediately. A self-declared one (IMAP, whose login is
// proved against a host the same caller supplied) is recorded UNVERIFIED and
// suppresses nothing: it is a candidate for an administrator to confirm, not a
// claim the system acts on. Without that split, anyone able to reach the
// connect endpoint could name a domain they do not own and silently stop the
// workspace storing correspondence with it.
//
// A connector that cannot name its account seeds nothing and syncs normally —
// the set stays admin-fed for it, which is a smaller failure than refusing to
// capture.
func (r *Registry) seedOwnDomainFromAccount(ctx context.Context, c connector.Connector, auth connector.Auth) error {
	labeler, ok := c.(connector.AccountLabeler)
	if !ok {
		return nil
	}
	label, err := labeler.AccountLabel(auth)
	if err != nil {
		// Not fatal — the label is display-grade and providers return it
		// inconsistently, so losing a whole mailbox to a missing string would be
		// worse than syncing without the seed. It is not silent either: this is
		// the confidentiality control's own input, and an operator seeing
		// internal mail captured needs this line to know why.
		slog.WarnContext(ctx, "capture: could not read the connected account's label — its domain was not registered as internal",
			"connector", c.Descriptor().Name, "error", err)
		return nil
	}
	if strings.TrimSpace(label) == "" {
		return nil
	}
	_, attested := c.(connector.AttestedAccountLabeler)
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return seeddomainOfAddressTx(ctx, tx, label, attested)
	})
}

// seeddomainOfAddressTx registers the domain of one mailbox address as internal.
// verified says whether the address was attested by the provider; an unverified
// row is recorded for an administrator to confirm and governs no drop.
func seeddomainOfAddressTx(ctx context.Context, tx pgx.Tx, address string, verified bool) error {
	domain := domainOfAddress(address)
	if domain == "" {
		return nil
	}
	// A gmail.com mailbox does not make gmail.com internal — every colleague at
	// every other company shares it.
	consumerMail, err := MatcherTx(ctx, tx)
	if err != nil {
		return err
	}
	if consumerMail.IsConsumer(domain) {
		return nil
	}
	// A row already confirmed by a human is never downgraded by a later sync.
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_email_domain (workspace_id, domain, source, verified)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'mailbox', $2)
		ON CONFLICT (workspace_id, domain) DO UPDATE SET verified = workspace_email_domain.verified OR EXCLUDED.verified`,
		domain, verified); err != nil {
		return fmt.Errorf("capture: seeding workspace email domain: %w", err)
	}
	return nil
}

// resolveCredential turns a stored connection's credential into the opaque
// Auth the connector expects. It PREFERS the vault ref; the legacy auth bytea
// column is read only for a row not yet backfilled onto the vault (during the
// additive transition, before that column is dropped).
func (r *Registry) resolveCredential(ctx context.Context, credentialRef *string, authBytes []byte) (connector.Auth, error) {
	if credentialRef != nil && *credentialRef != "" {
		if r.vault == nil {
			return nil, errors.New("capture: connection carries a credential ref but no keyvault is configured to resolve it")
		}
		ws, ok := principal.WorkspaceID(ctx)
		if !ok {
			return nil, errors.New("capture: credential resolution outside workspace context")
		}
		secret, err := r.vault.Get(ctx, ids.From[ids.WorkspaceKind](ws), keyvault.Ref(*credentialRef))
		if err != nil {
			return nil, fmt.Errorf("capture: resolving connector credential: %w", err)
		}
		return connector.Auth(secret), nil
	}
	// A row not yet backfilled: the credential still lives in the column.
	return connector.Auth(authBytes), nil
}

// connectorContext builds the acting principal: connector identity,
// the granting human's LIVE permissions and teams (connector ≤ human as
// a runtime property), full seat (capture is a write path by nature —
// the human's ability to grant it is what the scope check consumed).
func (r *Registry) connectorContext(ctx context.Context, name string, grantedBy ids.UserID) (context.Context, error) {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, errors.New("capture: sync outside workspace context")
	}
	// The authz resolver and the principal seam are untyped (ids.UUID);
	// widen the typed granting-human id at each of those edges.
	rbac, err := r.authority.EffectiveRBAC(ctx, wsID, grantedBy.UUID)
	if err != nil {
		return nil, fmt.Errorf("capture: granting human no longer resolves — the grant dies with them: %w", err)
	}
	seat, err := r.authority.SeatType(ctx, wsID, grantedBy.UUID)
	if err != nil {
		return nil, err
	}
	p := principal.Principal{
		Type:        principal.PrincipalConnector,
		ID:          connectorPrincipalID(name),
		UserID:      grantedBy.UUID,
		OnBehalfOf:  grantedBy.UUID,
		TeamIDs:     rbac.TeamIDs,
		SeatType:    seat,
		Permissions: rbac.Permissions,
	}
	runCtx := principal.WithActor(ctx, p)
	return principal.WithCorrelationID(runCtx, ids.NewV7()), nil
}

func (r *Registry) connector(name string) (connector.Connector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.connectors[name]
	if !ok {
		return nil, fmt.Errorf("capture: connector %q is not compiled in: %w", name, apperrors.ErrNotFound)
	}
	return c, nil
}
