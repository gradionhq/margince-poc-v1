// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The warm-intro path proposal (B-E08.4, features/07 §9): given a warm
// signal, name the route-in contact, the relationship we have, and a
// concrete suggested next move — an actionable path, not a notification.
// PROPOSAL ONLY: this file drafts and returns; it sends nothing and
// mutates nothing. The outbound ride is the 🟡 confirm-first send tool
// (features/02 §4) — the warm room proposes, the rep sends. The draft
// renders the Art. 50 AI-assisted disclosure (§11 gate 9) and carries
// evidence/provenance back to the warm signal.

package signals

import (
	"context"
	"fmt"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// art50Disclosure is the Art. 50 AI-assisted disclosure every proposed
// draft renders (EU AI Act Art. 50; A33/ADR-0025) — one spelling, machine
// readable in the payload AND human readable inside the draft body.
const art50Disclosure = "This message was drafted with AI assistance (EU AI Act Art. 50 disclosure)."

// IntroPath proposes the warm-intro path for a warm signal: the strongest
// visible contact at the resolved organization is the route in.
func (s *Store) IntroPath(ctx context.Context, signalID ids.UUID, now time.Time) (crmcontracts.SignalIntroPath, error) {
	warmth, err := s.Warmth(ctx, signalID, now)
	if err != nil {
		return crmcontracts.SignalIntroPath{}, err
	}
	if !warmth.Warm {
		return crmcontracts.SignalIntroPath{}, &NoWarmthError{
			Reason: "signal is cold: no live contact at the resolved organization, so there is no warm path to propose"}
	}

	var sig crmcontracts.Signal
	var orgName string
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		if sig, err = readSignal(ctx, tx, signalID, storekit.LiveOnly); err != nil {
			return err
		}
		// The proposal names the organization — that is a read of the org
		// record, so it carries the row-scope gate like any other read.
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", ids.UUID(warmth.ResolvedOrgId)); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT display_name FROM organization WHERE id = $1`,
			ids.UUID(warmth.ResolvedOrgId)).Scan(&orgName)
	})
	if err != nil {
		return crmcontracts.SignalIntroPath{}, fmt.Errorf("intro-path context: %w", err)
	}

	route := warmth.Contacts[0] // Warmth orders strongest-first
	out := crmcontracts.SignalIntroPath{
		SignalId:      sig.Id,
		ResolvedOrgId: warmth.ResolvedOrgId,
		ContactId:     route.PersonId,
		ContactName:   route.FullName,
		Relationship:  route,
	}
	out.Evidence.SourceSignalId = sig.Id
	out.Evidence.ResolvedOrgId = warmth.ResolvedOrgId
	out.Evidence.ContactIds = warmth.ContactIds

	// The move is a real branch: when the signal resolved (under consent)
	// to a specific person who is NOT the route-in contact, the play is
	// asking our contact for an intro; otherwise it is a direct draft.
	kind := crmcontracts.SignalIntroPathNextMoveKind("draft_to_contact")
	if sig.ResolvedPersonId != nil && openapi_types.UUID(*sig.ResolvedPersonId) != route.PersonId {
		kind = crmcontracts.SignalIntroPathNextMoveKind("intro_request")
	}
	out.NextMove.Kind = kind
	out.NextMove.DraftSubject, out.NextMove.DraftBody = renderIntroDraft(kind, route, orgName, sig.Summary)
	out.NextMove.AiDisclosure = art50Disclosure
	return out, nil
}

// renderIntroDraft is the deterministic V1 draft: it names the contact,
// the relationship, and the signal it derives from, and always ends with
// the Art. 50 disclosure. (The Voice-DNA styled draft is the E07 seam —
// it replaces the wording, never the disclosure or the evidence.)
func renderIntroDraft(kind crmcontracts.SignalIntroPathNextMoveKind, route crmcontracts.SignalWarmContact, orgName, signalSummary string) (subject, body string) {
	name := "there"
	if route.FullName != nil && *route.FullName != "" {
		name = *route.FullName
	}
	relationship := string(route.RelationshipKind)
	if route.RelationshipRole != nil && *route.RelationshipRole != "" {
		relationship += " (" + *route.RelationshipRole + ")"
	}
	switch kind {
	case "intro_request":
		subject = "Could you introduce us at " + orgName + "?"
		body = fmt.Sprintf(
			"Hi %s,\n\nSomething came up on our side about %s: %s. You know the right people there (%s) — would you be open to making an intro?\n\n%s",
			name, orgName, signalSummary, relationship, art50Disclosure)
	default:
		subject = "Following up with " + orgName
		body = fmt.Sprintf(
			"Hi %s,\n\nReaching out because of a recent signal on %s: %s. Given our %s relationship, this felt worth a direct conversation.\n\n%s",
			name, orgName, signalSummary, relationship, art50Disclosure)
	}
	return subject, body
}
