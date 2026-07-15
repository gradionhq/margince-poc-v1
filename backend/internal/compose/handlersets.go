// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/quotas"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
)

// Aliases give the embedded handler sets distinct field names; each
// alias carries its module's full method set.
type (
	authHandlers         = identity.Handlers
	peopleHandlers       = people.Handlers
	dealsHandlers        = deals.Handlers
	activitiesHandlers   = activities.Handlers
	approvalsHandlers    = approvals.Handlers
	searchHandlers       = search.Handlers
	consentHandlers      = consent.Handlers
	collectionsHandlers  = collections.Handlers
	signalsHandlers      = signals.Handlers
	privacyHandlers      = privacy.Handlers
	agentsHandlers       = agents.Handlers
	voiceHandlers        = ai.Handlers
	customfieldsHandlers = customfields.Handlers
	quotasHandlers       = quotas.Handlers
)
