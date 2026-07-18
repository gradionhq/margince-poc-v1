// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The bounded backfill (ADR-0063): Gmail enumerates a mailbox backward from
// a date boundary via messages.list q=after:. The estimate is the provider's
// resultSizeEstimate — an estimate by name and by nature; the page walk uses
// the same GetRaw + capture discipline as incremental sync, so a message the
// two paths both see lands once (the capture key dedupes).

package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// backfillPageSize bounds one BackfillPage call; the engine commits cursor
// and counters between pages, so this is also the resume granularity.
const backfillPageSize = 100

var _ connector.Backfiller = (*Connector)(nil)

// afterQuery renders Gmail's after: operator. Gmail treats the date in the
// mailbox's own timezone and the operator is inclusive-day; the window
// boundary is a product parameter measured in months, so day-grain slack is
// noise.
func afterQuery(after time.Time) string {
	return "after:" + after.Format("2006/01/02")
}

// EstimateBackfill asks the provider how many messages the window holds.
func (c *Connector) EstimateBackfill(ctx context.Context, auth connector.Auth, after time.Time) (int, error) {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return 0, fmt.Errorf("gmail: malformed auth state: %w", err)
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return 0, err
	}
	return c.api.EstimateAfter(ctx, access, afterQuery(after))
}

// BackfillPage pulls one page of the window, oldest-boundary inclusive,
// through the same capture path as incremental sync.
func (c *Connector) BackfillPage(ctx context.Context, auth connector.Auth, after time.Time, pageToken string, sink connector.Sink) (connector.BackfillPageResult, error) {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return connector.BackfillPageResult{}, fmt.Errorf("gmail: malformed auth state: %w", err)
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return connector.BackfillPageResult{}, err
	}
	ids, next, err := c.api.ListAfter(ctx, access, afterQuery(after), pageToken, backfillPageSize)
	if err != nil {
		return connector.BackfillPageResult{}, err
	}
	res := connector.BackfillPageResult{NextToken: next, Scanned: len(ids)}
	for _, id := range ids {
		raw, err := c.api.GetRaw(ctx, access, id)
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

// paramMaxResults is Gmail's page-size query parameter.
const paramMaxResults = "maxResults"

// EstimateAfter returns messages.list's resultSizeEstimate for a query.
func (a *httpAPI) EstimateAfter(ctx context.Context, accessToken, query string) (int, error) {
	var out struct {
		ResultSizeEstimate int `json:"resultSizeEstimate"` //nolint:tagliatelle // Google names this field
	}
	q := url.Values{"q": {query}, paramMaxResults: {"1"}}
	if _, err := a.get(ctx, accessToken, "/messages", q, &out); err != nil {
		return 0, err
	}
	return out.ResultSizeEstimate, nil
}

// ListAfter returns one page of message ids matching the query.
func (a *httpAPI) ListAfter(ctx context.Context, accessToken, query, pageToken string, pageSize int) ([]string, string, error) {
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		NextPageToken string `json:"nextPageToken"` //nolint:tagliatelle // Google names this field
	}
	q := url.Values{"q": {query}, paramMaxResults: {strconv.Itoa(pageSize)}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	if _, err := a.get(ctx, accessToken, "/messages", q, &out); err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, len(out.Messages))
	for _, m := range out.Messages {
		ids = append(ids, m.ID)
	}
	return ids, out.NextPageToken, nil
}
