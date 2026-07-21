// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The bounded backfill (ADR-0063): Graph enumerates a mailbox backward from a
// date boundary via /me/messages $filter=receivedDateTime ge. The estimate is
// the provider's @odata.count; the page walk uses the same GetMIME + capture
// discipline as incremental sync, so a message the two paths both see lands
// once (the capture key dedupes).

package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// backfillPageSize bounds one BackfillPage call ($top); the engine commits
// cursor and counters between pages, so this is also the resume granularity.
const backfillPageSize = 100

// EstimateBackfill asks the provider how many messages the window holds.
func (c *Connector) EstimateBackfill(ctx context.Context, auth connector.Auth, after time.Time) (int, error) {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return 0, fmt.Errorf("graph: malformed auth state: %w", err)
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return 0, err
	}
	return c.api.EstimateAfter(ctx, access, after)
}

// BackfillPage pulls one page of the window, oldest-boundary inclusive,
// through the same capture path as incremental sync. The page token is the
// provider's own @odata.nextLink (the client refuses one that points off the
// Graph API).
func (c *Connector) BackfillPage(ctx context.Context, auth connector.Auth, after time.Time, pageToken string, sink connector.Sink) (connector.BackfillPageResult, error) {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return connector.BackfillPageResult{}, fmt.Errorf("graph: malformed auth state: %w", err)
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return connector.BackfillPageResult{}, err
	}
	ids, next, err := c.api.ListAfter(ctx, access, after, pageToken, backfillPageSize)
	if err != nil {
		return connector.BackfillPageResult{}, err
	}
	res := connector.BackfillPageResult{NextToken: next, Scanned: len(ids)}
	for _, id := range ids {
		raw, err := c.api.GetMIME(ctx, access, id)
		if err != nil {
			// A fetch fault is transient — stop the page without advancing
			// so the engine retries this page from its committed token.
			return connector.BackfillPageResult{}, err
		}
		captured, err := captureOne(ctx, raw, sink, st.Owner)
		if err != nil {
			return connector.BackfillPageResult{}, err
		}
		if captured {
			res.Captured++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}
