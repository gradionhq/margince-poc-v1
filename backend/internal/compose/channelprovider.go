// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The boot-time reconcile for the derived channel vocabulary: the ONE place
// channel_provider is written, and the ONE place
// activities.SetChannelProviders and comms.SetChannelProviders are called — so
// the DB registry and both packages' in-memory snapshots cannot be set two
// different ways.
//
// It does NOT write activity_kind. A provider names a transport; an activity
// kind names what sort of interaction happened. Those are different axes, and
// the vocabulary of interaction kinds is fixed by the contract and seeded by
// the core migration, so boot has nothing to add to it.
//
// Called from NewCaptureRegistry, which this codebase already constructs more
// than once per process (a role-specific alternate wiring path, the worker's
// one-shot backfill helper) — so every write here is an idempotent upsert, and
// every in-memory set is last-write-wins, never a once-only registration.

import (
	"context"
	"slices"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// reconcileChannelProviders upserts a channel_provider row (transport='core')
// for every composed provider, carrying the display facts the discovery
// endpoint publishes, then sets both packages' in-memory snapshots to exactly
// the composed set passed in.
//
// It runs over database.WithInfraTx, not the workspace-bound database.DB.Tx:
// activity_kind and channel_provider carry no workspace_id, so binding a
// tenant GUC would ask a question these tables have no answer to — and
// database.DB.Tx's workspace resolution fails outright on a fresh install
// with no organization bootstrapped yet, which is exactly when a process
// first constructs this registry.
//
// A provider name has to satisfy channel_provider's own grammar constraint,
// which is where an unusable name is refused — not here. The alternative, a
// check in Go beside the insert, would be a second spelling of the rule that
// could disagree with the column's.
//
// It never DELETEs. A provider whose supplier is gone on a later boot keeps
// its row — activity and person_channel_identity rows still reference it, the
// FK would refuse the delete anyway, and ErrConnectorNotConfigured already
// parks a send against it rather than needing the row gone.
func reconcileChannelProviders(ctx context.Context, pool *pgxpool.Pool, providers []string) error {
	err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		for _, facts := range channelProviderFactsFor(providers, providers) {
			// The display facts are UPSERTED, not left alone on conflict: they
			// describe the composed connector, so the running binary is their
			// only source of truth and a row written by an older build must be
			// corrected rather than preserved. The provider itself still lands
			// once — the primary key sees to that.
			if _, err := tx.Exec(ctx, `
				INSERT INTO channel_provider (provider, transport, label, credential_model, supplies_transport)
				VALUES ($1, 'core', $2, $3, $4)
				ON CONFLICT (provider) DO UPDATE SET
					label             = EXCLUDED.label,
					credential_model  = EXCLUDED.credential_model,
					supplies_transport = EXCLUDED.supplies_transport`,
				facts.provider, facts.label, facts.credentialModel, facts.suppliesTransport); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// These two take the COMPOSED set: they answer "can a reply leave this
	// installation", which is a question about what was compiled in. What a
	// message may NAME is a different set, loaded from the registry itself by
	// LoadChannelProviderDirectory — see there for why it is not written here.
	activities.SetChannelProviders(providers)
	comms.SetChannelProviders(providers)
	return nil
}

// composedChannelProviders holds this boot's registered transports, written by
// the reconcile above and read by the discovery endpoint. Same shape and same
// reason as composedExtensions: the mutex guards the read/write ORDERING, since
// the HTTP surface is assembled after the registry is constructed — and this
// package's registry construction can legitimately run more than once, so the
// last write in a boot sequence is authoritative.
//
// The endpoint reads THIS rather than querying channel_provider, which is what
// the plan calls serving from the boot snapshot: the table has no workspace_id
// to scope by, and answering an HTTP request from an unscoped pool read is a
// door this package does not need to open for a value fixed at boot.
var composedChannelProviders struct {
	mu sync.RWMutex
	// registered is every transport in the registry — what a message MAY name.
	registered []string
	// sending is the subset this binary composed a sender for — what a reply
	// CAN leave on. Held separately because the two differ (whatsapp is
	// registered and unsendable) and collapsing them is the conflation this
	// decision removed.
	sending []string
}

// LoadChannelProviderDirectory fills the directory snapshot from the registry
// table, and it runs for EVERY role that serves /v1 rather than as a side
// effect of constructing the capture registry.
//
// That independence is the point, and it is a defect this arc shipped once
// already: NewCaptureRegistry is config-gated (the api role only builds it when
// a keyvault root key is configured), so a snapshot written there is empty on a
// vault-less install — and the endpoint would then answer `{"data":[]}` with a
// 200, telling every timeline it has no labels and telling an agent the provider
// vocabulary is empty while log_activity still demands a value from it. Silence
// that reads as an answer is worse than an error.
//
// Reading the TABLE also makes the three columns 0252 adds load-bearing rather
// than write-only, and it is correct with or without a reconcile: the migration
// seeds every row this installation ships with, and a reconcile only refreshes
// them.
//
// transport='core' is filtered deliberately. The column exists to separate a
// core connector from an extension unit's declared channel, and a unit's
// provider id is an operator's choice of what to install — the same operator
// information GET /v1/extensions is admin-only for. This endpoint is readable by
// every authenticated seat, so publishing a unit name here would be a
// disclosure decision made by inheritance. The slice that gives a unit a channel
// makes it deliberately.
func LoadChannelProviderDirectory(ctx context.Context, pool *pgxpool.Pool) error {
	var registered []string
	err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT provider FROM channel_provider WHERE transport = 'core' ORDER BY provider`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			registered = append(registered, p)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	setComposedChannelProviders(registered, activities.SendableChannelProviders())
	return nil
}

func setComposedChannelProviders(registered, sending []string) {
	composedChannelProviders.mu.Lock()
	defer composedChannelProviders.mu.Unlock()
	composedChannelProviders.registered = slices.Clone(registered)
	composedChannelProviders.sending = slices.Clone(sending)
}

// ComposedChannelProviders returns this boot's registered transports and the
// subset that can carry an outbound message.
func ComposedChannelProviders() (registered, sending []string) {
	composedChannelProviders.mu.RLock()
	defer composedChannelProviders.mu.RUnlock()
	return slices.Clone(composedChannelProviders.registered), slices.Clone(composedChannelProviders.sending)
}
