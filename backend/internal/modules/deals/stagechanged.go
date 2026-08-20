// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// What a consumer of deal.stage_changed reads, and how it reads it.
//
// Exported from the module that EMITS the event so a consumer never restates
// its shape: a second copy of the field names in another package is a copy that
// keeps compiling after this one is renamed, and the consumer would then act on
// zero values rather than fail.

import (
	"encoding/json"
	"fmt"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// EventDealStageChanged is the event type a stage move publishes.
const EventDealStageChanged = "deal.stage_changed"

// StageChanged is one stage move as its consumers need it: the transition, and
// the snapshot of everything frozen at that moment. The pointer fields are the
// ones a deal may not carry — an unpriced deal has no amount, a direct deal no
// partner.
type StageChanged struct {
	FromStatus         string
	ToStatus           string
	AmountMinor        *int64
	Currency           *string
	PartnerOrgID       *ids.UUID
	PartnerAttribution *string
	FxRateToBase       *string
}

// DecodeStageChanged reads one stage-move payload off the bus.
func DecodeStageChanged(payload json.RawMessage) (StageChanged, error) {
	var wire crmcontracts.PublicEventDealStageChanged
	if err := json.Unmarshal(payload, &wire); err != nil {
		return StageChanged{}, fmt.Errorf("decode deal.stage_changed: %w", err)
	}
	moved := StageChanged{
		FromStatus:         wire.FromStatus,
		ToStatus:           wire.ToStatus,
		AmountMinor:        wire.AmountMinorAtChange,
		Currency:           wire.CurrencyAtChange,
		PartnerAttribution: wire.PartnerAttribution,
		FxRateToBase:       wire.FxRateToBase,
	}
	if wire.PartnerOrgId != nil {
		partner := ids.UUID(*wire.PartnerOrgId)
		moved.PartnerOrgID = &partner
	}
	return moved, nil
}
