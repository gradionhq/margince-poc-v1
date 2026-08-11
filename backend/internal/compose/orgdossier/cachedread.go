// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// Reading a dossier that already exists, and never writing one.
//
// Get is the surface a person opens: a cache miss there means assemble it, and
// assembling costs a model call. A DRAFTER wants the same facts and must never
// pay that price — a rep pressing "Write email" is not asking for a dossier,
// and a drafting screen that stalls behind one has spent the workspace's budget
// on something nobody requested.
//
// So this is the read half alone. Cold cache answers nothing, and the caller
// writes a draft without those facts, which is exactly what it did before the
// field was fed at all.

import (
	"context"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// CachedSentencesMax bounds what a draft may take from a dossier.
//
// A dossier is a page somebody reads; a draft is three short paragraphs. Handing
// the whole thing to a drafter buys prompt tokens and invites the model to write
// an essay about the company back to the company.
const CachedSentencesMax = 5

// CachedSections is what this account is known to BE, from a dossier already
// assembled for this reader — or nothing.
//
// It never generates. A cold cache, an unreadable payload and a stale
// fingerprint all answer empty, and the caller drafts without these facts. That
// is the guarantee stated as a dependency: this function reaches the cache and
// the row scope, and there is no lane here for it to call.
//
// The fingerprint is deliberately NOT checked. A dossier assembled from facts
// that have since moved is stale for the page that displays it as current
// knowledge, and still true enough for a sentence of background in a draft the
// rep reads before sending. Re-deriving it to find out would be the model call
// this function exists to avoid.
func (s *Service) CachedSections(ctx context.Context, orgID ids.OrganizationID) []string {
	// A nil *Service reaches here as a non-nil interface, which is how a
	// dependency wired before its provider exists gets past a nil check and
	// panics on the first call instead of at startup.
	if s == nil {
		return nil
	}
	// A dossier is a reading aid for a person, and its cache is keyed per
	// reader; an agent has the records themselves.
	if err := auth.RequireHuman(ctx); err != nil {
		return nil
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return nil
	}
	cached, found, err := s.cached(ctx, userID, orgID)
	if err != nil || !found {
		return nil
	}

	out := make([]string, 0, CachedSentencesMax)
	for _, section := range cached.Sections {
		for _, sentence := range section.Sentences {
			text := strings.TrimSpace(sentence.Text)
			if text == "" {
				continue
			}
			out = append(out, text)
			if len(out) == CachedSentencesMax {
				return out
			}
		}
	}
	return out
}
