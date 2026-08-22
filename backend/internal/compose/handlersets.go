// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/accountdraft"
	"github.com/gradionhq/margince/backend/internal/compose/dealbrief"
	"github.com/gradionhq/margince/backend/internal/compose/meetingbrief"
	"github.com/gradionhq/margince/backend/internal/compose/nextaction"
	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	"github.com/gradionhq/margince/backend/internal/compose/personbrief"
	"github.com/gradionhq/margince/backend/internal/compose/persondraft"
	"github.com/gradionhq/margince/backend/internal/compose/personresearch"
	"github.com/gradionhq/margince/backend/internal/compose/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/compose/project360"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/commissions"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/dealrooms"
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
	traceHandlers          = capture.TraceHandlers
	pipelineTraceHandlers  = pipelinetrace.Handlers
	peopleHandlers         = people.Handlers
	dealsHandlers          = deals.Handlers
	contractsHandlers      = contracts.Handlers
	dealroomsHandlers      = dealrooms.Handlers
	commissionsHandlers    = commissions.Handlers
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
	project360Handlers     = project360.Handlers
	personBriefHandlers    = personbrief.Handlers
	personResearchHandlers = personresearch.Handlers
	meetingBriefHandlers   = meetingbrief.Handlers
	nextActionHandlers     = nextaction.Handlers
	dealBriefHandlers      = dealbrief.Handlers
	orgBriefHandlers       = orgbrief.Handlers
	orgDossierHandlers     = orgdossier.Handlers
	accountDraftHandlers   = accountdraft.Handlers
	personDraftHandlers    = persondraft.Handlers
	financeHandlers        = finance.Handlers
	aiActivityHandlers     = aiactivity.Handlers
)

// wirePerson360 binds the person record page — the organization page's
// sibling: same one-transaction assembly, same omitted-and-named sections,
// same overlay refusal. Its own function so the composition root reads as a
// list of what is wired rather than how each piece is built.
func (s *Server) wirePerson360(pool *pgxpool.Pool) {
	s.person360Svc = person360.NewService(pool, s.peopleStore, s.dealsStore, consent.NewStore(InstallationDB(pool)), ai.NewFeedbackStore(InstallationDB(pool)), time.Now)
	s.person360Handlers = person360.NewHandlers(s.person360Svc, s.sorDispatch.isOverlay)
	// The relationship brief is assembled from the SAME composite read the page
	// serves, so the two cannot disagree about what this caller may see. No
	// model lane is wired: the brief is the deterministic floor and says so in
	// generated_by, rather than 501-ing on a workspace without a model.
	s.personBriefHandlers = personbrief.NewHandlers(
		personbrief.NewService(pool, s.person360Svc, "", time.Now),
		s.sorDispatch.isOverlay,
	)
	// The pre-meeting brief shares that composite read and adds the claim
	// reader for the rest of the room. It caches NOTHING (ADR-0097 D5): it is
	// opened minutes before a meeting, so a stored artifact would be the one
	// thing it must not be. Same deterministic floor and the same
	// generated_by honesty as the brief above.
	s.meetingBriefHandlers = meetingbrief.NewHandlers(
		meetingbrief.NewService(pool, s.person360Svc, s.peopleStore, time.Now),
		s.sorDispatch.isOverlay,
	)
	// The deal's next best action reads the deal and its timeline through
	// their own gated stores and performs nothing; the click goes through the
	// verb the answer names.
	s.nextActionHandlers = nextaction.NewHandlers(
		nextaction.NewService(s.dealsStore, activities.NewStore(InstallationDB(pool)), time.Now),
	)
	// The deal brief reads the same deal, its health, timeline and tasks,
	// plus its Deal Room, all through their own gates, and writes nothing.
	s.dealBriefHandlers = dealbrief.NewHandlers(
		dealbrief.NewService(s.dealsStore, activities.NewStore(InstallationDB(pool)), dealrooms.NewStore(InstallationDB(pool)), time.Now),
	)
	// No provider is registered, which is the supported configuration rather
	// than a gap: the surface answers "not connected" and writes nothing
	// (ADR-0096 D4). Connecting one later is a provider implementation.
	s.personResearchHandlers = personresearch.NewHandlers(
		personresearch.NewService(s.peopleStore, s.person360Svc, persondata.NewRegistry(nil), time.Now),
		s.sorDispatch.isOverlay,
	)
	// The person-side draft reads through the same 360 and writes nothing, so
	// it needs no pool of its own. Nil lane here for the same reason as the
	// brief's: WithPersonDraft binds the api role's, and without it the endpoint
	// answers from its deterministic floor rather than 501-ing.
	s.personDraftHandlers = persondraft.NewHandlers(
		persondraft.NewService(s.person360Svc, nil).
			WithEnvelope(draftEnvelope(pool, s.log)), s.sorDispatch.isOverlay)
}

// wireProject360 assembles the project page from the module stores the
// handler sets already serve — the deals store with its field catalog, the
// shared people store, the contracts and activities stores — so the page and
// the per-record endpoints read the same columns under the same gates. It
// rides the same dispatch the company and person pages do: a workspace on
// the incumbent mirror refuses all three the same way.
func (s *Server) wireProject360(pool *pgxpool.Pool) {
	svc := project360.NewService(
		pool,
		deals.NewStore(InstallationDB(pool), DealsInstallation()).WithFieldCatalog(customfields.NewService(pool, nil)),
		s.peopleStore,
		contracts.NewStore(InstallationDB(pool)),
		activities.NewStore(InstallationDB(pool)),
		time.Now,
	)
	s.project360Handlers = project360.NewHandlers(svc, s.sorDispatch.isOverlay)
}
