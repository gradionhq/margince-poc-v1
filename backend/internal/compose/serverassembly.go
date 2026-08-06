// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The assembly steps newServer runs. Each one binds ONE surface group
// together with the cross-module edges it needs — a module never imports a
// sibling (ADR-0054), so compose is where those edges are made. They live
// beside the Server inventory rather than inside it so the literal in
// server.go reads as what a process serves, not as how each set is built.
// serveroptions.go is the other half of the wiring: what a process ROLE
// layers on top of these defaults.

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// newPeopleHandlers builds the person/organization/lead transport with the
// seams compose owns for it.
//
// The fieldcatalog seam: customfields' catalog read makes the
// workspace's active cf_* columns ride person/organization
// payloads (values only — the schema-change engine stays behind
// WithSchemaPool; ActiveColumns needs none of it).
// The match stager is injected here because approvals is a sibling of
// people and a module never imports one: compose is where that edge is
// made, as it is for every other cross-module dependency.
func newPeopleHandlers(pool *pgxpool.Pool) peopleHandlers {
	return people.NewHandlers(pool).
		WithFieldCatalog(customfields.NewService(pool, nil)).
		WithMatchStager(linkedInMatchStager(pool))
}

// newActivitiesHandlers builds the timeline transport over the sibling
// modules its inbound and outbound edges need.
func newActivitiesHandlers(pool *pgxpool.Pool) activitiesHandlers {
	return activities.NewHandlers(pool).
		WithConsent(consent.NewGate(consent.NewStore(pool))).
		// The public booking capture seams (feedback/14): people is the
		// idempotent-on-email person path, consent records the
		// passthrough — both injected here, never sibling imports.
		WithPublicBooking(people.NewStore(pool), bookingConsentAdapter{store: consent.NewStore(pool)}).
		// The RFC 8058 unsubscribe linker (B-E11.32): consent mints the
		// preference token behind the List-Unsubscribe URL.
		WithUnsubscribe(preferenceLinkAdapter{store: consent.NewStore(pool)})
}

// wireCaptureSettingsSurface binds the workspace's own capture posture
// controls.
func (s *Server) wireCaptureSettingsSurface(pool *pgxpool.Pool) {
	// The workspace capture-settings surface (CAP-WIRE-7, ADR-0072):
	// read the auto-enrich posture (all roles), toggle it (admin/ops).
	s.captureSettingsHandlers = captureSettingsHandlers{store: capture.NewSettings(NewSettingsStore(pool))}
	// The installation's own identity and reporting basis (ADR-0090/A135):
	// name, reporting zone, base currency — the last of which locks once a
	// deal has converted against it (ADR-0085 §7).
	s.installationSettingsHandlers = installationSettingsHandlers{
		store: identity.NewInstallationSettings(pool, NewSettingsStore(pool)),
	}
	// The workspace's own consumer-mail list (CAP-PARAM-5): the surviving
	// domain control, and the only way an operator corrects a shipped
	// baseline that is wrong about one of their customers.
	s.consumerMailDomainHandlers = consumerMailDomainHandlers{store: capture.NewFreemailDomains(pool)}
}

// wireExportSurface binds the two export transports.
func (s *Server) wireExportSurface(pool *pgxpool.Pool, log *slog.Logger) {
	// First-class filtered export (B-E15.13): the writer reuses the ONE
	// predicate engine + the bundle writer's open-format rendering; the
	// collections store resolves a saved view / dynamic list source
	// behind its own visibility gate.
	s.filteredExportHandlers = filteredExportHandlers{writer: NewFilteredExportWriter(pool), collections: collections.NewStore(pool)}
	s.overlayExportHandlers = newOverlayExportHandlers(pool, log)
}

// wireOnboardingSurface binds the first-run group: the installation's own
// company, the site read that seeds it, and the onboarding state the two
// report progress through — all three gated by the same rollout.
func (s *Server) wireOnboardingSurface(pool *pgxpool.Pool) {
	// The installation's own company (the 0083 anchor). Its own store
	// instance, like every other people-backed shadow here: the company
	// form's write shape is people's, the transport is compose's.
	s.companyHandlers = companyHandlers{store: people.NewStore(pool), rollout: companyContextRolloutOnboarding}
	s.siteReadHandlers = siteReadHandlers{companyContextRollout: companyContextRolloutOnboarding}
	s.onboardingStateHandlers = onboardingStateHandlers{
		state: identity.NewOnboardingStore(pool), company: people.NewStore(pool),
		proposal: &onboardingProposalEngine{
			state: identity.NewOnboardingStore(pool), people: people.NewStore(pool),
			rollout: companyContextRolloutOnboarding,
		},
	}
}

// wireSystemOfRecordReads builds the per-workspace native/overlay dispatch
// and the reads that ride it — the company view and its grounded prose.
func (s *Server) wireSystemOfRecordReads(pool *pgxpool.Pool) {
	// The overlay read dispatch is built with a nil live-incumbent resolver
	// here (force-fresh degrades to the mirror). WithKeyvault injects the
	// vault-backed resolver once the vault is known — the vault arrives via
	// an option applied AFTER newServer returns, and the dispatch/provider/
	// freshness reader are pointers shared across that return, so a
	// boot-time SetOverlayIncumbentResolver reaches the same instance this
	// field serves reads through.
	s.sorDispatch = NewDispatcher(NewProvider(pool), NewOverlayProvider(pool, s.overlayMeter, nil), pool)
	// The company view (org360) is assembled from THIS system of record;
	// it asks the same dispatch every other overlay-aware read asks, so a
	// workspace running on the incumbent mirror gets one honest refusal
	// instead of a page that quietly omits most of itself. Wired after the
	// dispatch because it needs it.
	// The people store carries the SAME fieldcatalog seam peopleHandlers
	// gets: the 360 serves the organization object, and without it the
	// company view would silently omit the cf_* columns GET
	// /organizations/{id} returns for the same record.
	// The brief reads THROUGH the 360 service, so it inherits every gate the
	// page itself applies and can only describe what this caller may see.
	// The model lane is nil here: WithAccountBrief binds the api role's
	// summarize lane, and without it the brief serves its deterministic
	// floor.
	s.peopleStore = people.NewStore(pool).WithFieldCatalog(customfields.NewService(pool, nil))
	s.org360Svc = org360.NewService(pool, s.peopleStore, approvals.NewService(pool), time.Now)
	s.orgBriefSvc = orgbrief.NewService(pool, s.org360Svc, s.peopleStore, nil, "", time.Now)
	s.orgBriefHandlers = orgbrief.NewHandlers(s.orgBriefSvc, s.sorDispatch.isOverlay)
	s.org360Handlers = org360.NewHandlers(
		s.org360Svc,
		s.sorDispatch.isOverlay,
	)
	// The person page is the company page's sibling and rides the same
	// dispatch, so it is wired here rather than beside the handler sets: a
	// workspace on the incumbent mirror refuses both the same way.
	s.wirePerson360(pool)
}
