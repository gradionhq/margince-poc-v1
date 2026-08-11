// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/accountdraft"
	"github.com/gradionhq/margince/backend/internal/compose/meetingbrief"
	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	"github.com/gradionhq/margince/backend/internal/compose/personbrief"
	"github.com/gradionhq/margince/backend/internal/compose/persondraft"
	"github.com/gradionhq/margince/backend/internal/compose/personresearch"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/finance"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/quotas"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
	"github.com/gradionhq/margince/backend/internal/shared/ports/persondata"
)

// Aliases give the embedded handler sets distinct field names; each
// alias carries its module's full method set.
type (
	authHandlers           = identity.Handlers
	channelHandlers        = capture.ChannelHandlers
	peopleHandlers         = people.Handlers
	dealsHandlers          = deals.Handlers
	activitiesHandlers     = activities.Handlers
	approvalsHandlers      = approvals.Handlers
	searchHandlers         = search.Handlers
	consentHandlers        = consent.Handlers
	collectionsHandlers    = collections.Handlers
	signalsHandlers        = signals.Handlers
	privacyHandlers        = privacy.Handlers
	automationHandlers     = automation.Handlers
	voiceHandlers          = ai.Handlers
	customfieldsHandlers   = customfields.Handlers
	quotasHandlers         = quotas.Handlers
	overlayHandlers        = overlay.Handlers
	webhooksHandlers       = webhooks.Handlers
	org360Handlers         = org360.Handlers
	person360Handlers      = person360.Handlers
	personBriefHandlers    = personbrief.Handlers
	personResearchHandlers = personresearch.Handlers
	meetingBriefHandlers   = meetingbrief.Handlers
	orgBriefHandlers       = orgbrief.Handlers
	orgDossierHandlers     = orgdossier.Handlers
	accountDraftHandlers   = accountdraft.Handlers
	personDraftHandlers    = persondraft.Handlers
	financeHandlers        = finance.Handlers
)

// wirePerson360 binds the person record page — the organization page's
// sibling: same one-transaction assembly, same omitted-and-named sections,
// same overlay refusal. Its own function so the composition root reads as a
// list of what is wired rather than how each piece is built.
func (srv *Server) wirePerson360(pool *pgxpool.Pool) {
	srv.person360Svc = person360.NewService(pool, srv.peopleStore, consent.NewStore(pool), ai.NewFeedbackStore(pool), time.Now)
	srv.person360Handlers = person360.NewHandlers(srv.person360Svc, srv.sorDispatch.isOverlay)
	// The relationship brief is assembled from the SAME composite read the page
	// serves, so the two cannot disagree about what this caller may see. No
	// model lane is wired: the brief is the deterministic floor and says so in
	// generated_by, rather than 501-ing on a workspace without a model.
	srv.personBriefHandlers = personbrief.NewHandlers(
		personbrief.NewService(pool, srv.person360Svc, "", time.Now),
		srv.sorDispatch.isOverlay,
	)
	// The pre-meeting brief shares that composite read and adds the claim
	// reader for the rest of the room. It caches NOTHING (ADR-0097 D5): it is
	// opened minutes before a meeting, so a stored artifact would be the one
	// thing it must not be. Same deterministic floor and the same
	// generated_by honesty as the brief above.
	srv.meetingBriefHandlers = meetingbrief.NewHandlers(
		meetingbrief.NewService(pool, srv.person360Svc, srv.peopleStore, time.Now),
		srv.sorDispatch.isOverlay,
	)
	// No provider is registered, which is the supported configuration rather
	// than a gap: the surface answers "not connected" and writes nothing
	// (ADR-0096 D4). Connecting one later is a provider implementation.
	srv.personResearchHandlers = personresearch.NewHandlers(
		personresearch.NewService(srv.peopleStore, srv.person360Svc, persondata.NewRegistry(nil), time.Now),
		srv.sorDispatch.isOverlay,
	)
	// The person-side draft reads through the same 360 and writes nothing, so
	// it needs no pool of its own. Nil lane here for the same reason as the
	// brief's: WithPersonDraft binds the api role's, and without it the endpoint
	// answers from its deterministic floor rather than 501-ing.
	srv.personDraftHandlers = persondraft.NewHandlers(
		persondraft.NewService(srv.person360Svc, nil), srv.sorDispatch.isOverlay)
}
