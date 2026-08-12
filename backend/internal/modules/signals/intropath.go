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

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// Art50Disclosure is the Art. 50 AI-assisted disclosure every proposed
// draft renders (EU AI Act Art. 50; A33/ADR-0025) — one spelling, machine
// readable in the payload AND human readable inside the draft body.
// Exported so every AI-drafted-content surface reuses this one string
// rather than drafting its own wording (compose/offerdraft.go's
// ai_disclosure is the other consumer).
const Art50Disclosure = "This message was drafted with AI assistance (EU AI Act Art. 50 disclosure)."

// IntroPath proposes the warm-intro path for a warm signal: the strongest
// visible contact at the resolved organization is the route in.
func (s *Store) IntroPath(ctx context.Context, signalID ids.SignalID, now time.Time) (crmcontracts.SignalIntroPath, error) {
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
	var disclosure string
	out.NextMove.DraftSubject, out.NextMove.DraftBody, disclosure =
		renderIntroDraft(kind, route, orgName, sig.Summary)
	// The machine-readable field carries the SAME sentence the body does, in
	// the same language. Two spellings of one disclosure is a reader being told
	// one thing and an auditor another.
	out.NextMove.AiDisclosure = disclosure
	return out, nil
}

// introPhrases is the wording of one warm-intro draft in one language.
//
// This path does not use the shared floor table's phrases, and the reason is
// worth stating: the table writes correspondence WITH a counterparty, while
// these two messages are an ask TO a colleague ("would you introduce us") and a
// first approach to a stranger. The shared vocabulary they do use is the
// language the message is written in and the band-none rule that a first
// approach may not open by following up on nothing.
type introPhrases struct {
	IntroSubject  string
	IntroBody     string
	DirectSubject string
	DirectBody    string
	// Disclosure is the Art. 50 line in this language. Translated rather than
	// shared, because a legally required sentence a reader cannot read has not
	// disclosed anything - and an English footer under German prose is the
	// clearest possible tell that the message was machine-made.
	Disclosure string
	// Relationships names each relationship kind in prose. Without it the raw
	// enum ("deal_stakeholder") lands mid-sentence in a message a rep sends.
	Relationships map[crmcontracts.SignalWarmContactRelationshipKind]string
}

// The two moves in each language. The German is Sie throughout to a
// counterparty and du to nobody, since the route contact is a business
// relationship rather than a colleague in the seat sense.
var introTable = map[textlang.Lang]introPhrases{
	textlang.English: {
		IntroSubject: "Could you introduce us at %s?",
		IntroBody: "Hi %s,\n\nSomething came up on our side about %s: %s. You know the " +
			"right people there - would you be open to making an intro?\n\n%s",
		DirectSubject: "Getting in touch about %s",
		DirectBody: "Hi %s,\n\nI am writing because of something we picked up about %s: %s. " +
			"Given that we %s, this felt worth raising with you directly.\n\n%s",
		Disclosure: Art50Disclosure,
		Relationships: map[crmcontracts.SignalWarmContactRelationshipKind]string{
			crmcontracts.SignalWarmContactRelationshipKindDealStakeholder: "are working together on a deal",
			crmcontracts.SignalWarmContactRelationshipKindEmployment:      "know each other through your company",
		},
	},
	textlang.German: {
		IntroSubject: "Können Sie uns bei %s vorstellen?",
		IntroBody: "Hallo %s,\n\nbei uns ist etwas zu %s aufgekommen: %s. Sie kennen dort " +
			"die richtigen Ansprechpartner - wären Sie bereit, uns vorzustellen?\n\n%s",
		DirectSubject: "Kurze Anfrage zu %s",
		DirectBody: "Hallo %s,\n\nich melde mich, weil wir etwas zu %s aufgenommen haben: %s. " +
			"Da wir %s, wollte ich das direkt mit Ihnen besprechen.\n\n%s",
		Disclosure: "Diese Nachricht wurde mit KI-Unterstützung verfasst (Offenlegung nach Art. 50 EU-KI-Verordnung).",
		Relationships: map[crmcontracts.SignalWarmContactRelationshipKind]string{
			crmcontracts.SignalWarmContactRelationshipKindDealStakeholder: "gemeinsam an einem Vorgang arbeiten",
			crmcontracts.SignalWarmContactRelationshipKindEmployment:      "über Ihr Unternehmen in Kontakt stehen",
		},
	},
	textlang.Vietnamese: {
		IntroSubject: "Anh/chị có thể giới thiệu chúng tôi tại %s không?",
		IntroBody: "Chào %s,\n\nchúng tôi vừa ghi nhận một việc liên quan đến %s: %s. " +
			"Anh/chị quen những người phù hợp ở đó - anh/chị có thể giới thiệu giúp không?\n\n%s",
		DirectSubject: "Xin được liên hệ về %s",
		DirectBody: "Chào %s,\n\ntôi liên hệ vì chúng tôi ghi nhận một việc liên quan đến %s: %s. " +
			"Vì hai bên %s, tôi muốn trao đổi trực tiếp với anh/chị.\n\n%s",
		Disclosure: "Thư này được soạn với sự hỗ trợ của AI (công bố theo Điều 50 Đạo luật AI của EU).",
		Relationships: map[crmcontracts.SignalWarmContactRelationshipKind]string{
			crmcontracts.SignalWarmContactRelationshipKindDealStakeholder: "đang cùng làm việc trong một cơ hội",
			crmcontracts.SignalWarmContactRelationshipKindEmployment:      "có liên hệ qua công ty của anh/chị",
		},
	},
}

// renderIntroDraft is the deterministic V1 draft: it names the contact,
// the relationship, and the signal it derives from, and always ends with
// the Art. 50 disclosure. (The Voice-DNA styled draft is the E07 seam —
// it replaces the wording, never the disclosure or the evidence.)
//
// The language comes from the signal's own summary, which is the only text
// this path holds. An unresolvable one writes English, the last rung of the
// resolution ladder (DRAFT-AC-E-2). Neither subject may be a follow-up line:
// both moves are a first approach on this topic, and "Following up with X" to
// somebody who has heard nothing is the invented history DRAFT-AC-E-3 forbids.
func renderIntroDraft(kind crmcontracts.SignalIntroPathNextMoveKind, route crmcontracts.SignalWarmContact, orgName, signalSummary string) (subject, body, disclosure string) {
	lang := textlang.Detect(signalSummary)
	phrases, ok := introTable[lang]
	if !ok {
		phrases = introTable[draftfloor.DefaultLang]
	}

	name := anonymousGreetingName(lang)
	if route.FullName != nil && *route.FullName != "" {
		name = *route.FullName
	}

	if kind == "intro_request" {
		return fmt.Sprintf(phrases.IntroSubject, orgName),
			fmt.Sprintf(phrases.IntroBody, name, orgName, signalSummary, phrases.Disclosure),
			phrases.Disclosure
	}
	return fmt.Sprintf(phrases.DirectSubject, orgName),
		fmt.Sprintf(phrases.DirectBody, name, orgName, signalSummary,
			phrases.relationship(route.RelationshipKind), phrases.Disclosure),
		phrases.Disclosure
}

// relationship names a relationship kind in prose, or says nothing specific
// about it. An unrecognized kind is a new enum member this table has not
// learned yet, and a vague clause is better in a rep's outbound message than
// the raw identifier.
func (p introPhrases) relationship(kind crmcontracts.SignalWarmContactRelationshipKind) string {
	if named, ok := p.Relationships[kind]; ok {
		return named
	}
	return p.Relationships[crmcontracts.SignalWarmContactRelationshipKindEmployment]
}

// anonymousGreetingName is what to call somebody whose name is not on file.
func anonymousGreetingName(lang textlang.Lang) string {
	switch lang {
	case textlang.German:
		return "zusammen"
	case textlang.Vietnamese:
		return "anh/chị"
	default:
		return "there"
	}
}
