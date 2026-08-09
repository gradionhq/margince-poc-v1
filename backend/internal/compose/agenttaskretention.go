// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Retention for the MCP task table.
//
// A completed task stores the VERBATIM tool result its execution produced, for
// the reason the row exists at all: a second poll must be answerable without
// re-running anything. For most of the confirm-first set that result is a whole
// record read-back — a person with their names, e-mails and phones; a deal with
// its amount — so the row holds subject data exactly as the idempotency claim
// body does, and for exactly as long as somebody remembers to remove it.
//
// The window is the task's OWN expires_at rather than a constant here. Past it
// the handle is already unanswerable: Load refuses an expired task, which the
// specification explicitly permits a server to discard. So a row past that
// point can never be read again through any surface, and keeping it stores
// personal data for no purpose at all — which is what Art. 5(1)(e) storage
// limitation forbids.
//
// This is a SEPARATE pass from the idempotency sweep on purpose, even though
// the two do the same shape of work an hour apart. The windows are independent
// and must stay so: folding them would make a change to one silently move the
// other, and the job's name would stop describing what it deletes.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// agentTaskRetentionActor is the principal the sweep runs as.
const agentTaskRetentionActor = "agent:agent_task_retention"

// AgentTaskRetentionSweeper drops MCP task rows past their own expiry.
type AgentTaskRetentionSweeper struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewAgentTaskRetentionSweeper builds the sweep over the pool.
func NewAgentTaskRetentionSweeper(pool *pgxpool.Pool, log *slog.Logger) *AgentTaskRetentionSweeper {
	return &AgentTaskRetentionSweeper{pool: pool, log: log}
}

// SweepWorkspace deletes every expired task in the workspace already bound in
// ctx. It reports how many rows went, because a retention pass that says
// nothing reads exactly like one that had nothing to do.
func (s *AgentTaskRetentionSweeper) SweepWorkspace(ctx context.Context) error {
	wsCtx := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem,
		ID:   agentTaskRetentionActor,
	})
	purged, err := s.purgeExpired(wsCtx)
	if err != nil {
		return err
	}
	if purged > 0 {
		s.log.InfoContext(wsCtx, "mcp task retention: expired tasks purged", "rows", purged)
	}
	return nil
}

func (s *AgentTaskRetentionSweeper) purgeExpired(ctx context.Context) (int64, error) {
	var purged int64
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// idx_agent_task_expiry (migration 0201) is what keeps this cheap.
		tag, err := tx.Exec(ctx, `DELETE FROM agent_task WHERE expires_at < now()`)
		if err != nil {
			return fmt.Errorf("compose: purging expired MCP tasks: %w", err)
		}
		purged = tag.RowsAffected()
		return nil
	})
	return purged, err
}
