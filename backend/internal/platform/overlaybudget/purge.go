// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlaybudget

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// purgeScanBatch bounds one SCAN round trip; SCAN is incremental by contract,
// so this is throughput, never completeness.
const purgeScanBatch = 500

// PurgeWorkspace drops every budget counter for ws, returning the number of
// keys unlinked. A reset install must not report a spent budget.
//
// The scan is prefix-bound to this workspace (keyPrefix + the workspace id is
// how ops.go builds every key), so a co-tenant's counters are out of reach by
// key shape rather than by care. A meter with no Redis client — the fail-closed
// value compose constructs before cmd rebinds it — has nothing to purge and
// reports zero.
func (m *Meter) PurgeWorkspace(ctx context.Context, ws ids.UUID) (int, error) {
	if m.rdb == nil {
		return 0, nil
	}
	var deleted int
	iter := m.rdb.Scan(ctx, 0, keyPrefix+ws.String()+":*", purgeScanBatch).Iterator()
	batch := make([]string, 0, purgeScanBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := m.rdb.Unlink(ctx, batch...).Result()
		if err != nil {
			return fmt.Errorf("overlaybudget: unlinking %d counters: %w", len(batch), err)
		}
		deleted += int(n)
		batch = batch[:0]
		return nil
	}
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) == purgeScanBatch {
			if err := flush(); err != nil {
				return deleted, err
			}
		}
	}
	if err := iter.Err(); err != nil {
		return deleted, fmt.Errorf("overlaybudget: scanning counters for workspace %s: %w", ws, err)
	}
	if err := flush(); err != nil {
		return deleted, err
	}
	return deleted, nil
}
