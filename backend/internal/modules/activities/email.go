// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The one send path (B-EP07.12): both transports — the HTTP handler
// and the MCP send_email tool — commit an outbound email through THIS
// method, so the ordering invariant (authorization refuses before the
// consent gate answers) and the consent check itself cannot fork.

import (
	"context"
	"fmt"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SendEmailInput is one consented outbound send anchored to an
// existing activity (the thread being replied to).
type SendEmailInput struct {
	Recipients     []string // to + cc, all consent-checked
	Subject        string
	Body           string
	ConsentPurpose string
}

// SendEmail runs the governed send: anchor visibility → write grant →
// consent gate → outbound activity in the write shape. The transport
// hand-off is the deployment's SMTP/provider seam; V1's durable fact
// is the logged activity.
func (s *Store) SendEmail(ctx context.Context, anchorID ids.UUID, in SendEmailInput, gate ConsentGate) (crmcontracts.Activity, error) {
	if gate == nil {
		// Fail closed: a send surface without its suppression gate is a
		// wiring defect, not an implicit allow.
		return crmcontracts.Activity{}, fmt.Errorf("send path has no consent authority wired: %w", apperrors.ErrConsentNotGranted)
	}
	// Authorization FIRST, consent second: the anchor's visibility and
	// the write grant must refuse before the consent gate answers, or
	// the 409-vs-403 difference becomes a consent oracle for callers
	// with no rights at all.
	anchor, err := s.GetActivity(ctx, anchorID, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, err
	}
	if err := gate.RequireGrantedForEmails(ctx, in.Recipients, in.ConsentPurpose); err != nil {
		return crmcontracts.Activity{}, err
	}

	var links []ActivityLinkInput
	if anchor.Links != nil {
		links = make([]ActivityLinkInput, 0, len(*anchor.Links))
		for _, l := range *anchor.Links {
			links = append(links, ActivityLinkInput{EntityType: string(l.EntityType), EntityID: ids.UUID(l.EntityId)})
		}
	}
	direction := "outbound"
	sent, _, err := s.LogActivity(ctx, LogActivityInput{
		Kind:      "email",
		Subject:   &in.Subject,
		Body:      &in.Body,
		Direction: &direction,
		Links:     links,
		Source:    "manual",
	})
	return sent, err
}
