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
	"errors"
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
	// The backfill walks the whole mailbox, Sent Items included — unlike the
	// incremental delta, which is inbox-only — so this is where the T1
	// correspondence evidence (ADR-0072 §1) is available to collect. Resolved
	// BEFORE anything is captured: a page that cannot tell sent mail from
	// received would stamp its whole window un-attested, and the natural key
	// makes that permanent. Treated like any other provider fault — the page
	// stops without advancing and the engine retries from its committed token.
	sentFolder, err := c.api.SentFolderID(ctx, access)
	if err != nil {
		return connector.BackfillPageResult{}, err
	}
	msgs, next, err := c.api.ListAfter(ctx, access, after, pageToken, backfillPageSize)
	if err != nil {
		return connector.BackfillPageResult{}, err
	}
	res := connector.BackfillPageResult{NextToken: next, Scanned: len(msgs)}
	// One page is a hundred messages of provider I/O and capture work, so the
	// engine hears the tally per message rather than once at the end. Scanned
	// counts messages WALKED so far, not the page's listing: a page that dies
	// mid-walk is retried from the committed token, and reporting the listing
	// up front would show progress the retry then repeats.
	progress := connector.BackfillProgressFrom(ctx)
	for walked, msg := range msgs {
		captured, err := c.backfillOne(ctx, access, msg, sink, st.Owner, sentFolder)
		if err != nil {
			return connector.BackfillPageResult{}, err
		}
		if captured {
			res.Captured++
		} else {
			res.Skipped++
		}
		progress.Observed(ctx, walked+1, res.Captured, res.Skipped)
	}
	return res, nil
}

// backfillOne walks ONE listed message: fetch its MIME, then capture it. Its
// bool is the page's tally decision — captured, or skipped for any of the
// ordinary reasons a message yields no row.
func (c *Connector) backfillOne(ctx context.Context, access string, msg MessageRef, sink connector.Sink, owner, sentFolder string) (bool, error) {
	raw, err := c.api.GetMIME(ctx, access, msg.ID)
	if errors.Is(err, connector.ErrSkip) {
		// A message the provider refuses to hand over: deleted between the
		// listing and the fetch, or an oversized MIME blob that truncated
		// would not be honest evidence. Both are per-message drops, counted
		// and stepped over the same way the incremental pull does —
		// returning either here would fail the page, and the engine would
		// retry from its committed token straight back onto the same
		// message, forever.
		return false, nil
	}
	if err != nil {
		// Stop the page without advancing, so a retry resumes from the
		// committed token. Whether there IS a retry is the engine's call on
		// the class this error carries: a rate limit or an unreachable
		// provider is waited out, anything else ends the run.
		return false, err
	}
	return captureOne(ctx, raw, sink, owner, msg.ParentFolderID == sentFolder)
}
