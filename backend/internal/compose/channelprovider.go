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
// Driven by ReconcileChannelProviders, the boot step every process role runs.
// A role may run it more than once per process (a role-specific alternate
// wiring path, the worker's one-shot backfill helper), so every write here is
// an idempotent upsert, and every in-memory set is last-write-wins, never a
// once-only registration.

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// ReconcileChannelProviders writes this binary's channel vocabulary into the
// registry: the core connectors it compiled in, plus every channel its composed
// units declare. It runs after RegisterExtensions, because the unit half reads
// ComposedExtensions().
//
// It is a boot step in its own right, and it cannot ride the construction of
// the capture registry. That construction is gated on a configured keyvault
// root key; what a message may name is not. Where the write rides it, a
// vault-less installation registers none of its units' transports — a captured
// message on one then violates activity.channel_provider's foreign key, the
// core-vs-unit collision check never runs, and both in-memory snapshots keep
// their pre-registry default, so a reply on a unit channel is refused before it
// is staged. LoadChannelProviderDirectory answers the same question for the
// read side, and for the same reason.
//
// It returns its error rather than halting: a unit colliding with a core
// transport is a refusal an operator has to act on, and the caller that owns
// the process's exit is where that decision belongs.
func ReconcileChannelProviders(ctx context.Context, pool *pgxpool.Pool) error {
	return reconcileChannelProviders(ctx, pool, CoreChannelProviders())
}

// CoreChannelProviders is every transport this binary compiled a CORE connector
// for — the reconcile's core half.
//
// Derived from the registry rather than restated beside it. A hand-kept list
// would be a second answer to "what did this binary compile in", and the two
// would disagree the first time a connector was added; deriving it means the
// registry stays the only place that knows. It enumerates rather than captures,
// so it needs neither pool nor vault — both of which NewCaptureRegistry
// documents as optional for exactly that.
func CoreChannelProviders() []string {
	return NewCaptureRegistry(nil, nil, CaptureConfig{}).ChannelProviders()
}

// reconcileChannelProviders upserts a channel_provider row for every transport
// this binary composed — the core connectors passed in, plus every channel the
// composed UNITS declare — carrying the display facts the discovery endpoint
// publishes, then sets both packages' in-memory snapshots to the subset a reply
// can actually leave on.
//
// It is also where a unit shadowing a core connector is refused, because this is
// the first point at which both sets exist (unitChannelFacts).
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
	var units []channelProviderFacts
	if err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		reserved, err := reservedCoreProviders(ctx, tx, providers)
		if err != nil {
			return err
		}
		// Inside the transaction, and BEFORE any write: the reserved set is read
		// from the same snapshot the upsert runs against, so a unit cannot be
		// admitted against a registry that changed underneath the check. A
		// refused set writes nothing at all — the boot dies with the collision
		// uninstalled rather than half-installed.
		if units, err = unitChannelFacts(reserved); err != nil {
			return err
		}
		for _, facts := range append(channelProviderFactsFor(providers, providers), units...) {
			if err := upsertChannelProvider(ctx, tx, facts); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	// These two take the COMPOSED set: they answer "can a reply leave this
	// installation", which is a question about what was compiled in. What a
	// message may NAME is a different set, loaded from the registry itself by
	// LoadChannelProviderDirectory — see there for why it is not written here.
	//
	// A unit transport belongs in it exactly when the unit declared a Send. The
	// send path's own pre-flight reads this set, so a unit channel left out of
	// it would register, publish, capture — and park every reply a rep wrote
	// under "this installation cannot send on that", which is the one failure a
	// rep cannot tell from a broken provider.
	sendable := slices.Clone(providers)
	for _, unit := range units {
		if unit.suppliesTransport {
			sendable = append(sendable, unit.provider)
		}
	}
	activities.SetChannelProviders(sendable)
	comms.SetChannelProviders(sendable)
	return nil
}

// reservedCoreProviders is every transport this installation holds for the
// core: the registry's own `transport='core'` rows, plus the connectors this
// binary composed.
//
// BOTH halves, because neither is the whole answer. The registry knows names no
// binary has a connector for — `whatsapp` is registered by migration so a
// hand-logged WhatsApp message can say what carried it — and those are exactly
// the ones a composed-set check misses. The composed list covers the mirror
// case: a fresh database whose reconcile has not run yet, or a core connector
// registered after this one, where the row is not there to be read.
func reservedCoreProviders(ctx context.Context, tx pgx.Tx, composed []string) (map[string]bool, error) {
	reserved := make(map[string]bool, len(composed))
	for _, p := range composed {
		reserved[p] = true
	}
	rows, err := tx.Query(ctx, `SELECT provider FROM channel_provider WHERE transport = 'core'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		reserved[p] = true
	}
	return reserved, rows.Err()
}

// upsertChannelProvider writes one transport's row.
//
// The display facts are UPSERTED, not left alone on conflict: they describe the
// composed supplier, so the running binary is their only source of truth and a
// row written by an older build must be corrected rather than preserved. The
// provider itself still lands once — the primary key sees to that. `transport`
// is upserted with them because a provider that moves between suppliers is
// exactly the case where a stale value would tell the send path to resolve the
// wrong credential.
//
// The WHERE clause is the belt to reservedCoreProviders' braces, and it is here
// because the two failures are different: that check refuses a KNOWN collision
// with an explanation, and this refuses to overwrite a core row under any
// circumstance the check did not foresee. A unit taking over a core transport
// is silent by nature — the same conversation, transmitted by somebody else —
// so the write itself must not be able to do it.
func upsertChannelProvider(ctx context.Context, tx pgx.Tx, facts channelProviderFacts) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO channel_provider (provider, transport, label, credential_model, supplies_transport)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (provider) DO UPDATE SET
			transport          = EXCLUDED.transport,
			label              = EXCLUDED.label,
			credential_model   = EXCLUDED.credential_model,
			supplies_transport = EXCLUDED.supplies_transport
		WHERE channel_provider.transport = EXCLUDED.transport`,
		facts.provider, facts.transport, facts.label, facts.credentialModel, facts.suppliesTransport)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("compose: the transport %q is already registered by a different supplier than %q — a registered transport does not change hands, because every message and every identity binding already filed on it would start resolving a different credential",
			facts.provider, facts.transport)
	}
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
// Reading the TABLE also makes the three display columns load-bearing rather
// than write-only, and it is correct with or without a reconcile: the migration
// seeds every row this installation ships with, and a reconcile only refreshes
// them.
//
// EVERY registered transport is published, core and unit alike. The security
// review raised whether a unit's provider id — an operator's choice of what to
// install — should be visible to every authenticated seat, and the answer is
// yes, decided rather than inherited: a member whose timeline shows a message
// that arrived on a unit's channel needs to know what to call it, and hiding the
// name would leave that row rendering a raw id for exactly the transports the
// extension tier exists to add.
//
// What is gated is not the NAME but the ACT. Whether a member may send on a
// transport is an RBAC question, answered by the object permissions a unit
// registers as `ext_<unit>_<object>` (identity's policy) and by the send
// pre-flight — not by hiding the transport's existence. Disclosure and
// capability are separate axes, which is the same separation this whole arc is
// about.
func LoadChannelProviderDirectory(ctx context.Context, pool *pgxpool.Pool) error {
	var registered []string
	err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT provider FROM channel_provider ORDER BY provider`)
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

// loadChannelProviderDirectoryOrLog fills the directory snapshot at server
// assembly, for every role that serves /v1 and independent of whether this role
// composed a capture registry — see LoadChannelProviderDirectory for why that
// independence is load-bearing.
//
// A failure is logged rather than fatal: an installation that cannot read its
// own transport labels still serves every other route, and the directory's own
// empty answer is then the honest one. It is the SILENT empty this arc had to
// fix, not an empty that was reported.
//
// A nil pool OR a nil logger is a unit-test wiring with no database and no
// observability, which several route-level tests build directly; every other
// dependency here already tolerates that shape the same way. Both are checked
// because the two arrive independently — a test that supplies a pool and no
// logger would otherwise panic on the reporting path rather than the reading
// one, which is a confusing way to learn about a wiring gap.
func loadChannelProviderDirectoryOrLog(pool *pgxpool.Pool, log *slog.Logger) {
	if pool == nil || log == nil {
		return
	}
	if err := LoadChannelProviderDirectory(context.Background(), pool); err != nil {
		log.Error("compose: loading the transport directory", "err", err)
	}
}
