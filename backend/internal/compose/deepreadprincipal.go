// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Who a deep read acts as. The worker runs on a job, but everything it writes
// belongs to the human who asked for the read — and the authority on which
// human that is, is the dossier row, not the job payload that travelled beside
// it. Provenance is written once and never re-derived, so getting this wrong
// is not caught downstream.

import (
	"context"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// run is the whole deep read, River-agnostic so tests drive it directly.
// Retry semantics rest on BeginSiteRead's CAS: any terminal outcome
// (done, partial, failed) leaves the dossier past "queued", so a River
// retry — including one after a recorded failure — CAS-misses and
// no-ops. One honest outcome per dossier, no zombie re-crawls; reading
// the site again is a human's next start, never an automatic retry.
// deepReadWorkerCtx attaches the worker's principal, workspace and correlation
// onto the job context — the values every store write (and terminalCtx) needs.
//
// The requester it carries comes from the job payload, because the claim that
// yields the authoritative one needs a context to be made at all. It is
// replaced by withClaimedRequester the moment the row is in hand, so nothing
// the worker writes is attributed on the payload's word.
func deepReadWorkerCtx(ctx context.Context, args SiteDeepReadArgs) context.Context {
	return withClaimedRequester(
		principal.WithWorkspaceID(ctx, args.WorkspaceID), args.RequestedBy, args.SiteReadID)
}

// withClaimedRequester stamps the principal every store write is attributed to.
// The human named here owns what this read creates, so it must be the one the
// DOSSIER ROW records: a payload that disagreed would hang another person's
// name on the rows, which no later gate would catch — provenance is written
// once and never re-derived.
func withClaimedRequester(ctx context.Context, requestedBy string, readID ids.UUID) context.Context {
	requester := requestedByUserID(requestedBy)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalSystem,
		ID:         "agent:deepread",
		UserID:     requester,
		OnBehalfOf: requester,
	})
	if readID.IsZero() {
		return principal.WithCorrelationID(ctx, ids.NewV7())
	}
	return principal.WithCorrelationID(ctx, readID)
}

// requestedByUserID recovers the human uuid behind a "human:<uuid>"
// requested_by so the staged proposal carries OnBehalfOf. A requester
// without a recoverable uuid yields the zero uuid — the approval's
// on_behalf_of is then honestly NULL rather than the read failing over
// provenance.
func requestedByUserID(requestedBy string) ids.UUID {
	_, raw, found := strings.Cut(requestedBy, ":")
	if !found {
		return ids.UUID{}
	}
	id, err := ids.Parse(raw)
	if err != nil {
		return ids.UUID{}
	}
	return id
}
