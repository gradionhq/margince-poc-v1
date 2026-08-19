// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The inbox in the shape a SECOND door serves it.
//
// The HTTP handlers here were the only caller of the store rows until the
// governed tool surface gained a queue of its own (ADR-0055: a passport is
// governed identically on both doors). These four methods are what the tool
// door composes over, and they exist so the two doors cannot come to describe
// one approval differently: the status a caller is told, the lazy expiry folded
// into it, and the evidence that travels with a proposal are decided ONCE, in
// wire(), for whoever is asking.
//
// They add no authority of their own. Every one of them is the same call the
// handlers make, and the admission, the decide-authority probe and the caps a
// passport may spend are all answered underneath.

import (
	"context"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListWire is List in the contract shape.
func (s *Service) ListWire(ctx context.Context, in ListInput) ([]crmcontracts.Approval, storekit.Page, error) {
	rows, page, err := s.List(ctx, in)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	now := s.now()
	out := make([]crmcontracts.Approval, 0, len(rows))
	for _, a := range rows {
		out = append(out, wire(a, now))
	}
	return out, page, nil
}

// GetWire is Get in the contract shape.
func (s *Service) GetWire(ctx context.Context, id ids.ApprovalID) (crmcontracts.Approval, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return crmcontracts.Approval{}, err
	}
	return wire(a, s.now()), nil
}

// DecideWire is Decide in the contract shape.
func (s *Service) DecideWire(ctx context.Context, id ids.ApprovalID, approve bool, reason *string) (crmcontracts.Approval, error) {
	a, err := s.Decide(ctx, id, approve, reason)
	if err != nil {
		return crmcontracts.Approval{}, err
	}
	return wire(a, s.now()), nil
}

// DecideBundleWire is DecideBundle in the contract shape, each member carrying
// what the decision did to it.
func (s *Service) DecideBundleWire(ctx context.Context, bundleID ids.UUID, approve bool, reason *string) ([]crmcontracts.ApprovalBundleMember, error) {
	members, err := s.DecideBundle(ctx, bundleID, approve, reason)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]crmcontracts.ApprovalBundleMember, 0, len(members))
	for _, member := range members {
		out = append(out, crmcontracts.ApprovalBundleMember{
			Approval: wire(member.Approval, now),
			Outcome:  crmcontracts.ApprovalBundleMemberOutcome(member.Outcome),
		})
	}
	return out, nil
}
