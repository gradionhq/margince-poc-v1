// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The company view's three grounded-prose lanes, and only those.
//
// They are together because they share one decision: what a reader sees when a
// model is not available. All three degrade to a deterministic floor rather
// than failing, and `generated_by` names which wrote what — but the floors are
// not equivalent. The brief and the dossier still describe something from the
// same records; growth fit's can only abstain, because grading is not a
// restatement of recorded values. That difference is why they are three options
// and not one.
//
// The dossier and the growth fit also share one handler set, so each of their
// options rebuilds it from BOTH services. Either may run first; neither may
// rebuild that set from a service it did not just construct.

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
)

// WithAccountBrief binds the summarize lane both of the company view's
// grounded-prose surfaces are written by — the standing brief and the
// prepared "Ask Margince" questions — and the routing version that
// identifies the binding in every cached brief's fingerprint.
//
// Without it both serve their deterministic floor rather than failing: a
// role that runs no model still answers the endpoints, and generated_by
// tells the reader which of the two they have. routingVersion rides the
// fingerprint so re-pointing this lane rewrites cached briefs instead of
// leaving text attributed to a model that no longer writes it.
func WithAccountBrief(brain completer, routingVersion string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.orgBriefSvc = orgbrief.NewService(pool, s.org360Svc, s.peopleStore, brain, routingVersion, time.Now)
		s.orgBriefHandlers = orgbrief.NewHandlers(s.orgBriefSvc, s.sorDispatch.isOverlay)
	}
}

// WithCompanyDossier binds the lane that writes what a company IS, and the
// routing version that identifies the binding in every cached dossier's
// fingerprint.
//
// Unlike the growth fit's, this lane is an improvement rather than a
// precondition: the floor already describes the company from the same fields,
// one restated value per sentence. What the model adds is prose a person reads
// before a call instead of a list they skim.
//
// It rebuilds the shared handler set from BOTH services for the reason
// WithGrowthFit does — replacing that set while remembering only one service
// would leave the other endpoint answering from a service the Server no longer
// holds. Either option may run first.
func WithCompanyDossier(brain completer, routingVersion string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.orgDossierSvc = orgdossier.NewService(pool, s.peopleStore, brain, routingVersion, time.Now)
		s.orgDossierHandlers = orgdossier.NewHandlers(
			s.orgDossierSvc, s.orgGrowthFitSvc, s.sorDispatch.isOverlay)
	}
}

// WithGrowthFit binds the lane that judges how well a company fits what we
// sell, and the routing version that identifies the binding in every cached
// assessment's fingerprint.
//
// This is the one company-view lane whose absence CHANGES THE ANSWER rather
// than only its prose. The dossier's floor still describes a company; growth
// fit's floor abstains by DOSS-PARAM-7, because grading is not a restatement of
// recorded values. So an unwired deployment serves "here is what I would need
// to know", labelled deterministic, and never a band nobody stands behind.
//
// The dossier service is rebound alongside it rather than rebuilt, because the
// two share one handler set: replacing that set while remembering only one
// service would leave the other endpoint answering from a service the Server no
// longer holds.
func WithGrowthFit(brain completer, routingVersion string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.orgGrowthFitSvc = orgdossier.NewGrowthFitService(
			pool, s.peopleStore, offeringConfirmed(s.peopleStore), brain, routingVersion, time.Now)
		s.orgDossierHandlers = orgdossier.NewHandlers(
			s.orgDossierSvc, s.orgGrowthFitSvc, s.sorDispatch.isOverlay)
	}
}
