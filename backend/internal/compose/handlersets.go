// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/accountdraft"
	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/quotas"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
)

// Aliases give the embedded handler sets distinct field names; each
// alias carries its module's full method set.
type (
	authHandlers         = identity.Handlers
	channelHandlers      = capture.ChannelHandlers
	peopleHandlers       = people.Handlers
	dealsHandlers        = deals.Handlers
	activitiesHandlers   = activities.Handlers
	approvalsHandlers    = approvals.Handlers
	searchHandlers       = search.Handlers
	consentHandlers      = consent.Handlers
	collectionsHandlers  = collections.Handlers
	signalsHandlers      = signals.Handlers
	privacyHandlers      = privacy.Handlers
	automationHandlers   = automation.Handlers
	voiceHandlers        = ai.Handlers
	customfieldsHandlers = customfields.Handlers
	quotasHandlers       = quotas.Handlers
	overlayHandlers      = overlay.Handlers
	webhooksHandlers     = webhooks.Handlers
	org360Handlers       = org360.Handlers
	person360Handlers    = person360.Handlers
	orgBriefHandlers     = orgbrief.Handlers
	orgDossierHandlers   = orgdossier.Handlers
	accountDraftHandlers = accountdraft.Handlers
)

// wirePerson360 binds the person record page — the organization page's
// sibling: same one-transaction assembly, same omitted-and-named sections,
// same overlay refusal. Its own function so the composition root reads as a
// list of what is wired rather than how each piece is built.
func (srv *Server) wirePerson360(pool *pgxpool.Pool) {
	srv.person360Handlers = person360.NewHandlers(
		person360.NewService(pool, srv.peopleStore, consent.NewStore(pool), ai.NewFeedbackStore(pool), time.Now),
		srv.sorDispatch.isOverlay,
	)
}
