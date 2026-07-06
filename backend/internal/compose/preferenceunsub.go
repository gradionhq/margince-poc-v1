// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// preferenceLinkAdapter satisfies activities.UnsubscribeLinker over the
// consent module — the cross-module edge of the send path's RFC 8058
// header, injected here so activities never imports its sibling. A locked
// (transactional) purpose carries no unsubscribe surface; every other
// address resolves to its lazily-minted preference token.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/modules/consent"
)

type preferenceLinkAdapter struct {
	store *consent.Store
}

func (a preferenceLinkAdapter) UnsubscribeToken(ctx context.Context, recipientEmail, purposeKey string) (string, bool, error) {
	if consent.LockedPurpose(purposeKey) {
		return "", false, nil
	}
	return a.store.PreferenceTokenForEmail(ctx, recipientEmail)
}
