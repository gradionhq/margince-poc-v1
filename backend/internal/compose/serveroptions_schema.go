// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The options that hand a role authority over the SCHEMA rather than over the
// rows in it: the owner-DSN pool a custom-field DDL statement needs, and the
// non-production data reset built on top of it.
//
// They live apart from the rest of the option surface because they are the group
// where getting the wiring wrong is not a missing feature but a destructive one.
// Every other option adds a capability whose absence is an honest 501; these
// decide whether a process can drop and rebuild what an installation holds, so
// they carry their own file and their own reasoning about who may be granted
// them.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// WithSchemaPool wires the owner-privileged schema-change pool the
// customfields engine's two runtime-DDL paths (Create, SetOptions) need
// (--schema-dsn / MARGINCE_SCHEMA_DSN). It feeds the
// /readyz probe and rebuilds the customfields handlers over the real
// pool; without it those two operations stay their generated 501
// (ErrSchemaChangesUnavailable) rather than nil-derefing a pool that was
// never mounted — a role that runs no runtime DDL declares that by
// omission, the same posture as WithBlobstore/WithKeyvault.
//
// WHICH posture this option should have under the role-separation invariant is
// still open: issue #651 records the decision. The two candidates are keeping
// the 501 above as the honest answer for a role that legitimately holds no owner
// DSN, or relocating the two DDL operations behind a process that does. It is a
// product call about a CORE module — the extension tier depends on neither
// outcome — so it is filed rather than settled here, and the pointer sits at the
// function a reader arrives at with the question rather than in an issue tracker
// they would first have to think to search.
func WithSchemaPool(schemaPool *pgxpool.Pool) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.customfieldsHandlers = customfields.NewHandlers(pool, schemaPool)
		s.schemaPoolReady = schemaPool.Ping
	}
}

// WithDataReset wires the admin data-reset endpoint (POST /v1/admin/reset-data):
// the sweep runs through the composed app-role pool (the one every option
// receives, as WithSchemaPool takes it), while schemaPool (may be nil) is the
// owner-privileged pool that finalizes cf_* column drops — nil skips that
// finalize step, the reset itself still succeeds. Absent this option, or with
// allowed=false, the endpoint answers 404 (dataResetHandlers' zero value has a
// nil pool and a false flag, the same closed default).
//
// allowed is operations.allow_data_reset, stated by the deployment. It used to
// be inferred from MARGINCE_ENV, which meant an installation labelled `staging`
// — real internal users — could have its tenant data purged because a label
// said it was not production.
func WithDataReset(schemaPool *pgxpool.Pool, seeds deployconfig.Seeds, allowed bool) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.dataResetHandlers = dataResetHandlers{
			pool: pool, schemaPool: schemaPool, seeds: seeds, dataResetAllowed: allowed, log: s.log,
			// A pointer into the Server, so WithResetRuntime may be applied
			// before or after this option (see Server.resetRuntime). The meter
			// is likewise the ONE shared instance WithOverlayMeter rebinds; the
			// object store is backfilled by WithBlobstore when it runs later,
			// and the flush is a method value that reads the Server's caches at
			// reset time, not now.
			//
			// flushAfterOwnReset, NOT FlushResetCaches: this handler is the
			// gated path, so it is the one allowed to clear the auth lockout
			// buckets as well as the caches.
			runtime: &s.resetRuntime,
			budget:  s.overlayMeter,
			blob:    s.blob,
			vault:   s.vault,
			flush:   s.flushAfterOwnReset,
		}
	}
}

// WithResetRuntime wires the non-Postgres purges POST /admin/reset-data
// performs: the job queue, the event bus, and the reset announcement every
// process drops its caches on. The api role passes it under a non-production
// posture; a role without it resets Postgres only and reports zeros for the
// other surfaces.
//
// It takes funcs, not clients: cmd owns the Redis and River handles and
// compose names neither (see ResetRuntime).
func WithResetRuntime(rt ResetRuntime) Option {
	return func(s *Server, _ *pgxpool.Pool) { s.resetRuntime = rt }
}

// WithNonProduction surfaces the deployment posture onto /me's non_production
// field. Absent this option /me reports production, the fail-closed default.
func WithNonProduction(env runtimeenv.Environment) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.authHandlers = s.WithNonProduction(env.IsNonProduction())
	}
}

// WithDataResetAvailable surfaces onto /me the same switch WithDataReset gates
// the endpoint on, so the action a client renders and the route it would call
// cannot disagree.
//
// Its own option rather than an argument to the posture above, because the two
// are independent facts and this whole capability exists to stop them
// travelling together. Absent it /me reports unavailable, which hides the
// action rather than offering one the server would refuse.
func WithDataResetAvailable(allowed bool) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.authHandlers = s.WithDataResetAvailable(allowed)
	}
}

// Every send option below records onto s.send and NOTHING else. The HTTP
// handlers' own store is reconciled from that one value once every option has
// run (New's applySendPath), and the tool surface is rebuilt over it here, so
// no option can configure one transport and leave the others silently
// without.
