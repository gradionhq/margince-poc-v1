// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// How the installation handles leads (ADR-0090/A135 settings mechanism):
// the first-response target is opt-in. Off, no lead carries an SLA field,
// the sla_state filter matches nothing, the queue orders by score alone and
// the breach scan records nothing. On, the target is the installation's
// own number rather than a compile-time constant.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DefaultFirstResponseTarget is the target an installation that turns the
// SLA on without choosing a number gets (formulas §18, LEADSLA default).
const DefaultFirstResponseTarget = 240 * time.Minute

const (
	firstResponseMinMinutes = 15
	firstResponseMaxMinutes = 7 * 24 * 60
)

// FirstResponseEnabled is whether the first-response target is tracked at
// all. Off by default: a fresh installation should not open on a list where
// every lead reads "overdue".
var FirstResponseEnabled = settings.Define[bool](
	"people.first_response_enabled", leadVocabularyObject, "update", false, nil)

// FirstResponseTargetMinutes is how long a lead may wait for its first
// genuine response once its clock starts.
var FirstResponseTargetMinutes = settings.Define[int](
	"people.first_response_target_minutes", leadVocabularyObject, "update",
	int(DefaultFirstResponseTarget/time.Minute),
	func(minutes int) error {
		if minutes < firstResponseMinMinutes || minutes > firstResponseMaxMinutes {
			return fmt.Errorf("the target is %d..%d minutes", firstResponseMinMinutes, firstResponseMaxMinutes)
		}
		return nil
	})

// Definitions is people's contribution to the settings registry; compose
// concatenates each module's list.
func Definitions() []settings.Definition {
	return []settings.Definition{FirstResponseEnabled, FirstResponseTargetMinutes}
}

// leadSLAPolicy is the resolved setting pair every SLA computation reads.
type leadSLAPolicy struct {
	enabled bool
	target  time.Duration
}

// atRisk is the tail of the target inside which an unanswered lead reads as
// at_risk rather than within_target: the last quarter.
func (p leadSLAPolicy) atRisk() time.Duration { return p.target / 4 }

func (p leadSLAPolicy) targetMinutes() int { return int(p.target / time.Minute) }

// loadLeadSLAPolicy reads the two settings inside the caller's transaction,
// by key, without the settings object gate: the policy is an input to a
// lead read that is already gated by lead:read, and a connector or agent
// principal reading leads must not need a settings grant to see them.
func loadLeadSLAPolicy(ctx context.Context, tx pgx.Tx) (leadSLAPolicy, error) {
	policy := leadSLAPolicy{target: DefaultFirstResponseTarget}
	rows, err := tx.Query(ctx, `SELECT key, value FROM setting WHERE key = ANY($1)`,
		[]string{FirstResponseEnabled.Key(), FirstResponseTargetMinutes.Key()})
	if err != nil {
		return policy, fmt.Errorf("load lead sla policy: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw json.RawMessage
		if err := rows.Scan(&key, &raw); err != nil {
			return policy, err
		}
		switch key {
		case FirstResponseEnabled.Key():
			if err := json.Unmarshal(raw, &policy.enabled); err != nil {
				return policy, fmt.Errorf("decode %s: %w", key, err)
			}
		case FirstResponseTargetMinutes.Key():
			var minutes int
			if err := json.Unmarshal(raw, &minutes); err != nil {
				return policy, fmt.Errorf("decode %s: %w", key, err)
			}
			policy.target = time.Duration(minutes) * time.Minute
		}
	}
	return policy, rows.Err()
}

// slaPolicy resolves the policy for a store operation that has not opened
// its transaction yet.
func (s *Store) slaPolicy(ctx context.Context) (leadSLAPolicy, error) {
	var policy leadSLAPolicy
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		policy, err = loadLeadSLAPolicy(ctx, tx)
		return err
	})
	return policy, err
}

// WithSettings wires the installation settings store the lead-settings
// endpoints write through; compose injects it.
func (s *Store) WithSettings(store *settings.Store) *Store {
	s.settings = store
	return s
}

// GetLeadSettings answers the installation's lead handling.
func (s *Store) GetLeadSettings(ctx context.Context) (crmcontracts.LeadSettings, error) {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionRead); err != nil {
		return crmcontracts.LeadSettings{}, err
	}
	policy, err := s.slaPolicy(ctx)
	if err != nil {
		return crmcontracts.LeadSettings{}, err
	}
	return crmcontracts.LeadSettings{
		FirstResponseEnabled: policy.enabled, FirstResponseTargetMinutes: policy.targetMinutes(),
	}, nil
}

// UpdateLeadSettingsInput is a sparse patch; nil leaves the setting alone.
type UpdateLeadSettingsInput struct {
	FirstResponseEnabled       *bool
	FirstResponseTargetMinutes *int
}

// UpdateLeadSettings changes the lead handling (admin/ops). Each setting's
// own validator and audit verb apply through the settings store.
func (s *Store) UpdateLeadSettings(ctx context.Context, in UpdateLeadSettingsInput) (crmcontracts.LeadSettings, error) {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionUpdate); err != nil {
		return crmcontracts.LeadSettings{}, err
	}
	if s.settings == nil {
		return crmcontracts.LeadSettings{}, fmt.Errorf("people: lead settings are not wired; the installation cannot change them")
	}
	// Both land in ONE transaction: a target without its switch, or the
	// reverse, is not a state the list should ever render.
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if in.FirstResponseEnabled != nil {
			if err := setLeadSetting(ctx, tx, s.settings, FirstResponseEnabled.Key(), *in.FirstResponseEnabled); err != nil {
				return err
			}
		}
		if in.FirstResponseTargetMinutes != nil {
			return setLeadSetting(ctx, tx, s.settings, FirstResponseTargetMinutes.Key(), *in.FirstResponseTargetMinutes)
		}
		return nil
	})
	if err != nil {
		return crmcontracts.LeadSettings{}, err
	}
	return s.GetLeadSettings(ctx)
}

// setLeadSetting encodes one value and writes it through the settings store's
// transactional seam, which validates, audits and refuses a frozen setting.
func setLeadSetting[T bool | int](ctx context.Context, tx pgx.Tx, store *settings.Store, key string, value T) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("people: encoding %s: %w", key, err)
	}
	return store.SetRawTx(ctx, tx, key, raw)
}
