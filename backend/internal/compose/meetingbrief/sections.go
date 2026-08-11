// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// The eight sections of ADR-0097 D5, in their fixed order.
//
// Four of them are specified as deterministic (header, attendees, commitments,
// company context) and four as model-written (goal, deal state, risks, talking
// points). No model lane is wired to this surface yet, so all eight are written
// here from the assembled records and `generated_by` says `deterministic`
// rather than passing a composition off as a written brief. When a lane
// arrives, the four M sections gain a writer and this file stays as their
// floor — a workspace with no model gets a plainer brief, not a blank one.
//
// Every sentence cites a record. That is not a nicety of the floor: it is the
// same contract the model sections will be held to, so the card renders and
// behaves identically whichever wrote it.

import (
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The citable record types. A brief may only point at things the reader can
// open, and these are the ones a prep surface can open in place.
const (
	citeActivity = "activity"
	citeDeal     = "deal"
	citePerson   = "person"
)

// The two natures this floor writes. A line that RECOMMENDS an action and one
// that READS a risk out of a record are both labelled, because a reader
// forgives a wrong opinion and does not forgive a wrong fact — the sentences
// that are not plain facts are the ones that must say so. Anything unlabelled
// is a fact, which is the contract's default.
const (
	natureAssessment     = string(crmcontracts.Assessment)
	natureRecommendation = string(crmcontracts.Recommendation)
)

// The claim kinds this floor reads, bound to the contract enum rather than
// spelled as string literals. A kind renamed upstream then fails to COMPILE
// here, instead of silently emptying the section that reads it — a section that
// quietly stops having anything to say is invisible to every gate.
const (
	kindCommitmentOurs   = string(crmcontracts.CommitmentOurs)
	kindCommitmentTheirs = string(crmcontracts.CommitmentTheirs)
	kindOpenQuestion     = string(crmcontracts.OpenQuestion)
	kindDecision         = string(crmcontracts.Decision)
	kindDecisionProcess  = string(crmcontracts.DecisionProcess)
	kindObjection        = string(crmcontracts.Objection)
	kindPriority         = string(crmcontracts.Priority)
	kindSuccessCriterion = string(crmcontracts.SuccessCriterion)
)

// statusOpen is the claim status the risk and goal rules test. A claim already
// done is not a watch-out and not an ask.
const statusOpen = string(crmcontracts.ConversationClaimStatusOpen)

// Section is one heading with its lines, before the grounding filter runs.
type Section struct {
	Kind      crmcontracts.MeetingBriefSectionKind
	Sentences []Sentence
}

// Deterministic writes all eight sections from the assembled input alone.
//
// The order is the spec's and is not a rendering choice: goal and commitments
// lead because burying the ask is the canonical prep failure, and company
// context is last because it is background a reader skims only if they have
// time.
func Deterministic(in Input) []Section {
	built := []Section{
		{Kind: crmcontracts.MeetingBriefSectionKindHeader, Sentences: headerSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindGoal, Sentences: goalSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindAttendees, Sentences: attendeesSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindCommitments, Sentences: commitmentsSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindDealState, Sentences: dealStateSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindRisks, Sentences: risksSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindTalkingPoints, Sentences: talkingPointsSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindCompanyContext, Sentences: companyContextSection(in)},
	}
	for i := range built {
		built[i].Sentences = claims.Dedupe(built[i].Sentences)
	}
	return built
}

// headerSection (D) is what the meeting IS: when, with whom, about which deal,
// and how long since anyone in the room last heard from us.
func headerSection(in Input) []Sentence {
	self := []Evidence{{EntityType: citeActivity, EntityID: in.ActivityID}}
	out := []Sentence{{Text: meetingLine(in), Evidence: self}}
	if in.Deal != nil {
		out = append(out, Sentence{
			Text:     dealHeaderLine(*in.Deal),
			Evidence: []Evidence{{EntityType: citeDeal, EntityID: in.Deal.ID}},
		})
	}
	out = append(out, Sentence{Text: lastTouchLine(in), Evidence: self})
	return out
}

func meetingLine(in Input) string {
	subject := in.Subject
	if subject == "" {
		subject = "Meeting"
	}
	when := in.StartsAt.Format("Mon 2 Jan 15:04 MST")
	if in.Company == "" {
		return fmt.Sprintf("%s, %s.", subject, when)
	}
	return fmt.Sprintf("%s with %s, %s.", subject, in.Company, when)
}

// dealHeaderLine states the commercial stake in one line: what is on the table,
// where it sits, and when it is meant to land.
func dealHeaderLine(deal DealIn) string {
	parts := []string{deal.Name}
	if amount := spokenAmount(deal.AmountMinor, deal.Currency); amount != "" {
		parts = append(parts, amount)
	}
	if deal.Stage != "" {
		parts = append(parts, deal.Stage)
	}
	if deal.CloseDate != nil {
		parts = append(parts, "close "+deal.CloseDate.Format("2 Jan 2006"))
	}
	return strings.Join(parts, " · ") + "."
}

// lastTouchLine says how long the room has been quiet. Days, not a timestamp:
// the reader is deciding whether to open with an apology, and "eleven days"
// answers that where a date makes them do the arithmetic.
func lastTouchLine(in Input) string {
	if in.LastTouchAt == nil {
		return "Nothing has been captured with anyone in this room before."
	}
	days := int(in.Now.Sub(*in.LastTouchAt).Hours() / 24)
	switch {
	case days <= 0:
		return "Last touch was today."
	case days == 1:
		return "Last touch was yesterday."
	default:
		return fmt.Sprintf("Last touch was %d days ago.", days)
	}
}

// goalSection (M) leads, because burying the ask is the canonical prep failure.
//
// The floor states the ask the RECORD supports rather than inventing one: an
// open question to answer, a promise to close out, or the deal's own next step.
// It never says "build rapport" — a goal nobody can check against a record is
// the external-context filler the spec's first hard rule forbids.
func goalSection(in Input) []Sentence {
	if question, ok := firstOfKind(in, kindOpenQuestion); ok {
		return []Sentence{{
			Text:     fmt.Sprintf("Answer the open question from %s: %s", question.PersonName, question.Body),
			Nature:   natureRecommendation,
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: question.SourceID}},
		}}
	}
	if ours, ok := firstOfKind(in, kindCommitmentOurs); ok {
		return []Sentence{{
			Text:     fmt.Sprintf("Close out what we promised %s: %s", ours.PersonName, ours.Body),
			Nature:   natureRecommendation,
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: ours.SourceID}},
		}}
	}
	if in.Deal == nil {
		return nil
	}
	return []Sentence{{
		Text:     fmt.Sprintf("Move %s on from %s.", in.Deal.Name, stageOrUnnamed(in.Deal.Stage)),
		Nature:   natureRecommendation,
		Evidence: []Evidence{{EntityType: citeDeal, EntityID: in.Deal.ID}},
	}}
}

func stageOrUnnamed(stage string) string {
	if stage == "" {
		return "its current stage"
	}
	return stage
}

// attendeesSection (D list + M one-liners) names the room, with the people the
// reader has never spoken to flagged.
//
// The first-time flag is the point of the section. Walking in without knowing
// that a decision-maker in the room has never heard from you is the failure it
// exists to prevent, so it is stated in words rather than left to a badge the
// prose does not mention.
func attendeesSection(in Input) []Sentence {
	out := make([]Sentence, 0, len(in.Attendees))
	for _, attendee := range in.Attendees {
		out = append(out, Sentence{
			Text:     attendeeLine(attendee, in.Now),
			Evidence: []Evidence{{EntityType: citePerson, EntityID: attendee.PersonID}},
		})
	}
	return out
}

func attendeeLine(attendee AttendeeIn, now time.Time) string {
	parts := []string{attendee.FullName}
	if attendee.Title != "" {
		parts = append(parts, attendee.Title)
	}
	if attendee.DealRole != "" {
		parts = append(parts, readableRole(attendee.DealRole))
	}
	line := strings.Join(parts, ", ")
	if attendee.FirstTime {
		return line + " — first time you are meeting them."
	}
	days := int(now.Sub(*attendee.LastTouch).Hours() / 24)
	if days <= 0 {
		return line + " — last spoke today."
	}
	return fmt.Sprintf("%s — last spoke %d days ago.", line, days)
}

// readableRole turns the stored role key into words. The keys are a naming
// convention rather than an enum, so an unrecognized one is rendered as it was
// stored — inventing a label for a role nobody defined would be a claim.
func readableRole(role string) string {
	return strings.ReplaceAll(role, "_", " ")
}

// commitmentsSection (D) is what is outstanding, ours and theirs, each with the
// conversation it was made in and where it stands.
//
// Ours come first. A rep who walks in without having done what they promised
// has a different meeting than one who has, and reading their own overdue
// promise first is what changes the opening sentence.
func commitmentsSection(in Input) []Sentence {
	out := make([]Sentence, 0, len(in.Commitments))
	for _, kind := range []string{kindCommitmentOurs, kindCommitmentTheirs, kindOpenQuestion} {
		for _, claim := range in.Commitments {
			if claim.Kind != kind {
				continue
			}
			out = append(out, Sentence{
				Text:     commitmentLine(claim),
				Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
			})
		}
	}
	return out
}

func commitmentLine(claim ClaimIn) string {
	var opener string
	switch claim.Kind {
	case kindCommitmentOurs:
		opener = "We owe " + claim.PersonName
	case kindCommitmentTheirs:
		opener = claim.PersonName + " owes us"
	default:
		opener = claim.PersonName + " asked"
	}
	line := fmt.Sprintf("%s: %s", opener, claim.Body)
	if claim.DueAt != nil {
		line += fmt.Sprintf(" (due %s)", claim.DueAt.UTC().Format("2 Jan"))
	}
	if source := commitmentSource(claim); source != "" {
		line += ", from " + source
	}
	return line + fmt.Sprintf(" — %s.", claim.Status)
}

// commitmentSource names the conversation in prose. The label is the thread
// subject; without one the sentence says nothing rather than pasting the record
// id, which the grounding filter would drop the whole sentence for.
func commitmentSource(claim ClaimIn) string {
	if claim.SourceLabel == "" {
		return ""
	}
	return fmt.Sprintf("%q", claim.SourceLabel)
}

// dealStateSection (M) is where the deal stands: what was last said, what was
// objected to, and what nobody has decided.
func dealStateSection(in Input) []Sentence {
	out := make([]Sentence, 0, dealStateCap)
	if len(in.Recent) > 0 {
		last := in.Recent[0]
		out = append(out, Sentence{
			Text:     lastConversationLine(last),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: last.ID}},
		})
	}
	for _, kind := range []string{kindObjection, kindDecision, kindSuccessCriterion, kindPriority} {
		for _, claim := range in.Commitments {
			if claim.Kind != kind || len(out) == dealStateCap {
				continue
			}
			out = append(out, Sentence{
				Text:     dealStateLine(claim),
				Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
			})
		}
	}
	return out
}

// dealStateCap is the spec's three-to-five bullets. The ceiling is enforced;
// the floor is not, because padding to three would mean writing a bullet the
// records do not support.
const dealStateCap = 5

func lastConversationLine(last ActIn) string {
	subject := last.Subject
	if subject == "" {
		subject = last.Kind
	}
	switch last.Direction {
	case "inbound":
		return fmt.Sprintf("They wrote last, about %q.", subject)
	case "outbound":
		return fmt.Sprintf("We wrote last, about %q.", subject)
	default:
		return fmt.Sprintf("The last thing captured was %q.", subject)
	}
}

func dealStateLine(claim ClaimIn) string {
	switch claim.Kind {
	case kindObjection:
		return fmt.Sprintf("%s objected: %s", claim.PersonName, claim.Body)
	case kindDecision:
		return fmt.Sprintf("Agreed with %s: %s", claim.PersonName, claim.Body)
	case kindSuccessCriterion:
		return fmt.Sprintf("%s measures success by: %s", claim.PersonName, claim.Body)
	default:
		return fmt.Sprintf("%s is focused on: %s", claim.PersonName, claim.Body)
	}
}

// risksSection (M, ≤3) is OMITTED when empty, and that is spelled in the spec
// rather than inferred. A risks heading over nothing reads as "we looked and
// found none", which is a claim this floor cannot make.
func risksSection(in Input) []Sentence {
	out := make([]Sentence, 0, riskCap)
	for _, claim := range in.Commitments {
		if len(out) == riskCap {
			break
		}
		if text, ok := riskLine(claim, in.Now); ok {
			out = append(out, Sentence{
				Text:     text,
				Nature:   natureAssessment,
				Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
			})
		}
	}
	return out
}

const riskCap = 3

// riskLine turns a claim into a watch-out only when the RECORD says something
// is wrong: a promise of ours past its date, or an objection nobody closed. A
// risk read out of anything else would be the deal-history-ignoring filler the
// spec forbids.
func riskLine(claim ClaimIn, now time.Time) (string, bool) {
	if claim.Kind == kindObjection && claim.Status == statusOpen {
		return fmt.Sprintf("%s's objection is still open: %s", claim.PersonName, claim.Body), true
	}
	overdue := claim.Kind == kindCommitmentOurs &&
		claim.Status == statusOpen &&
		claim.DueAt != nil && claim.DueAt.Before(now)
	if overdue {
		return fmt.Sprintf("We are past due to %s on: %s", claim.PersonName, claim.Body), true
	}
	return "", false
}

// talkingPointsSection (M, 3–5) is each point tied to a specific captured
// statement — never a generic opener, because a talking point nobody said is
// the filler the first hard rule forbids.
func talkingPointsSection(in Input) []Sentence {
	out := make([]Sentence, 0, talkingPointCap)
	for _, kind := range []string{kindPriority, kindSuccessCriterion, kindDecisionProcess, kindObjection} {
		for _, claim := range in.Commitments {
			if claim.Kind != kind || len(out) == talkingPointCap {
				continue
			}
			out = append(out, Sentence{
				Text:     talkingPointLine(claim),
				Nature:   natureRecommendation,
				Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
			})
		}
	}
	return out
}

const talkingPointCap = 5

func talkingPointLine(claim ClaimIn) string {
	switch claim.Kind {
	case kindObjection:
		return fmt.Sprintf("Address what %s objected to: %s", claim.PersonName, claim.Body)
	case kindDecisionProcess:
		return fmt.Sprintf("Confirm the process %s described: %s", claim.PersonName, claim.Body)
	case kindSuccessCriterion:
		return fmt.Sprintf("Tie the demo to what %s called success: %s", claim.PersonName, claim.Body)
	default:
		return fmt.Sprintf("Pick up what %s said matters: %s", claim.PersonName, claim.Body)
	}
}

// companyContextSection (D) is background, collapsed and last. Provider
// research is not wired to this surface, so it says what THIS installation
// recorded and nothing more: an empty section is honest, and a filled one made
// of inference would be exactly the filler the spec's first rule forbids.
func companyContextSection(in Input) []Sentence {
	if in.Company == "" || len(in.Attendees) == 0 {
		return nil
	}
	return []Sentence{{
		Text:     fmt.Sprintf("%s is where %s works.", in.Company, in.Attendees[0].FullName),
		Evidence: []Evidence{{EntityType: citePerson, EntityID: in.Attendees[0].PersonID}},
	}}
}

// firstOfKind returns the newest claim of one kind that is still open. Claims
// arrive newest-first from the store, so the first match is the newest.
func firstOfKind(in Input, kind string) (ClaimIn, bool) {
	for _, claim := range in.Commitments {
		if claim.Kind == kind && claim.Status == statusOpen {
			return claim, true
		}
	}
	return ClaimIn{}, false
}

// spokenAmount renders a deal's value the way somebody would SAY it: "€95k",
// not "95000.00 EUR". The exact figure belongs on the deal card, where a reader
// is checking a number; here it is one clause of a header line.
func spokenAmount(minor int64, currency string) string {
	if minor == 0 || currency == "" {
		return ""
	}
	symbol := map[string]string{"EUR": "€", "USD": "$", "GBP": "£"}[currency]
	if symbol == "" {
		symbol = currency + " "
	}
	major := minor / 100
	if major >= 1000 {
		return fmt.Sprintf("%s%dk", symbol, major/1000)
	}
	return fmt.Sprintf("%s%d", symbol, major)
}
