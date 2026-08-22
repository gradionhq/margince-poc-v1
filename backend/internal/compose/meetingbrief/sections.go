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
	"sort"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	"github.com/gradionhq/margince/backend/internal/compose/personcontext"
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

// specOrder is the order the sections render in: goal and commitments lead
// because burying the ask is the canonical prep failure, and company context
// is last because it is background a reader skims only if they have time.
var specOrder = map[crmcontracts.MeetingBriefSectionKind]int{
	crmcontracts.MeetingBriefSectionKindHeader:         0,
	crmcontracts.MeetingBriefSectionKindGoal:           1,
	crmcontracts.MeetingBriefSectionKindWhatChanged:    2,
	crmcontracts.MeetingBriefSectionKindAttendees:      3,
	crmcontracts.MeetingBriefSectionKindCommitments:    4,
	crmcontracts.MeetingBriefSectionKindDealState:      5,
	crmcontracts.MeetingBriefSectionKindRisks:          6,
	crmcontracts.MeetingBriefSectionKindTalkingPoints:  7,
	crmcontracts.MeetingBriefSectionKindCompanyContext: 8,
}

// Section is one heading with its lines, before the grounding filter runs.
type Section struct {
	Kind      crmcontracts.MeetingBriefSectionKind
	Sentences []Sentence
}

// Deterministic writes all nine sections from the assembled input alone.
//
// The order is the spec's and is not a rendering choice: goal and commitments
// lead because burying the ask is the canonical prep failure, and company
// context is last because it is background a reader skims only if they have
// time.
func Deterministic(in Input) []Section {
	// One ranked claim set, consumed in section order: the goal takes the
	// sharpest ask, risks take what is wrong, deal state takes what was
	// settled, and talking points get what nobody else said. The commitments
	// ledger reads the whole set without taking: it is a list to check, not
	// a reading to avoid repeating.
	ranked := rankClaims(in)
	built := []Section{
		{Kind: crmcontracts.MeetingBriefSectionKindHeader, Sentences: headerSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindGoal, Sentences: goalSection(in, ranked)},
		{Kind: crmcontracts.MeetingBriefSectionKindWhatChanged, Sentences: whatChangedSection(in, ranked)},
		{Kind: crmcontracts.MeetingBriefSectionKindAttendees, Sentences: attendeesSection(in)},
		{Kind: crmcontracts.MeetingBriefSectionKindRisks, Sentences: risksSection(in, ranked)},
		{Kind: crmcontracts.MeetingBriefSectionKindCommitments, Sentences: commitmentsSection(ranked)},
		{Kind: crmcontracts.MeetingBriefSectionKindDealState, Sentences: dealStateSection(in, ranked)},
		{Kind: crmcontracts.MeetingBriefSectionKindTalkingPoints, Sentences: talkingPointsSection(ranked)},
		{Kind: crmcontracts.MeetingBriefSectionKindCompanyContext, Sentences: companyContextSection(in)},
	}
	// Rendered in the spec's order whatever order they were filled in.
	sort.SliceStable(built, func(i, j int) bool { return specOrder[built[i].Kind] < specOrder[built[j].Kind] })
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
	// Which engagement, before which deal: on an account running two bodies of
	// work the project is what tells the reader they are in the right room, and
	// a deal line alone leaves that to be inferred from the deal's name.
	if in.Project != nil {
		out = append(out, Sentence{Text: projectHeaderLine(*in.Project), Evidence: self})
	}
	if in.Deal != nil {
		out = append(out, Sentence{
			Text:     dealHeaderLine(*in.Deal),
			Evidence: []Evidence{{EntityType: citeDeal, EntityID: in.Deal.ID}},
		})
	}
	out = append(out, Sentence{Text: lastTouchLine(in), Evidence: self})
	return out
}

// dealHeaderLine states the commercial stake in one line: what is on the table,
// where it sits, and when it is meant to land.
func dealHeaderLine(deal DealIn) string {
	parts := []string{deal.Name}
	if amount := personcontext.SpokenAmount(deal.AmountMinor, deal.Currency); amount != "" {
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
// The goal is what must be true when the meeting ends: the sharpest open ask
// the record holds — a promise of ours to close out, a question to answer, a
// decision being waited on. It takes that claim out of the set, so no later
// section restates it. It never says "move the deal on from its stage": a
// sentence that restates the stage field the rep is looking at is filler, and
// the package's own rule is that an ungroundable section is absent.
func goalSection(in Input, ranked *rankedClaims) []Sentence {
	if ask, ok := ranked.take(openOfKind(kindCommitmentOurs, kindOpenQuestion, kindDecision)); ok {
		return []Sentence{{
			Text:     goalLine(ask, in.Now),
			Nature:   natureRecommendation,
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: ask.SourceID}},
		}}
	}
	// A delivery meeting months after close-won usually has no open claims,
	// and this section went silent exactly there — on the engagement the
	// meeting is about. The project's own next step is the ask the record
	// supports.
	//
	// Cited to the MEETING, not the project: the evidence vocabulary the brief
	// shares with the account brief has no `project` kind, and a citation the
	// reader cannot resolve is dropped whole rather than shown. The meeting is
	// the record that carries the project link, so it is the honest source for
	// a claim about which engagement this room is here to move.
	if in.Project != nil {
		return []Sentence{{
			Text:     projectGoalLine(*in.Project),
			Nature:   natureRecommendation,
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: in.ActivityID}},
		}}
	}
	return nil
}

func goalLine(ask ClaimIn, now time.Time) string {
	switch ask.Kind {
	case kindOpenQuestion:
		return fmt.Sprintf("Answer the open question from %s: %s", ask.PersonName, ask.Body)
	case kindDecision:
		return fmt.Sprintf("Get the decision %s is holding: %s", ask.PersonName, ask.Body)
	default:
		if ask.DueAt != nil && ask.DueAt.Before(now) {
			return fmt.Sprintf("Close out what we owe %s, overdue since %s: %s", ask.PersonName, ask.DueAt.UTC().Format("2 Jan"), ask.Body)
		}
		return fmt.Sprintf("Close out what we promised %s: %s", ask.PersonName, ask.Body)
	}
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
// The ledger: every promise and question, complete, in rank order. It does
// not take from the set — the goal and a risk may each name one of these
// lines as a reading of it, and the ledger still has to show the whole of
// what is owed.
func commitmentsSection(ranked *rankedClaims) []Sentence {
	claims := ranked.all(ofKind(kindCommitmentOurs, kindCommitmentTheirs, kindOpenQuestion))
	out := make([]Sentence, 0, len(claims))
	for _, claim := range claims {
		out = append(out, Sentence{
			Text:     commitmentLine(claim),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
		})
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
func dealStateSection(in Input, ranked *rankedClaims) []Sentence {
	out := make([]Sentence, 0, dealStateCap)
	if len(in.Recent) > 0 {
		last := in.Recent[0]
		out = append(out, Sentence{
			Text:     lastConversationLine(last),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: last.ID}},
		})
	}
	// Settled things: the decisions taken. An open objection is a risk and was
	// taken there; open asks went to the goal and the ledger; what they said
	// matters is what they will raise, so it is a talking point.
	for _, claim := range ranked.takeAll(ofKind(kindDecision), dealStateCap-len(out)) {
		out = append(out, Sentence{
			Text:     dealStateLine(claim),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
		})
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
	return fmt.Sprintf("Agreed with %s: %s", claim.PersonName, claim.Body)
}

// risksSection (M, ≤3) is OMITTED when empty, and that is spelled in the spec
// rather than inferred. A risks heading over nothing reads as "we looked and
// found none", which is a claim this floor cannot make.
func risksSection(in Input, ranked *rankedClaims) []Sentence {
	isRisk := func(c ClaimIn) bool { _, ok := riskLine(c, in.Now); return ok }
	claims := ranked.takeAll(isRisk, riskCap)
	out := make([]Sentence, 0, len(claims))
	for _, claim := range claims {
		text, _ := riskLine(claim, in.Now)
		out = append(out, Sentence{
			Text:     text,
			Nature:   natureAssessment,
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
		})
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

// talkingPointsSection (M, ≤5) is each point tied to a specific captured
// statement — never a generic opener, because a talking point nobody said is
// the filler the first hard rule forbids. Each point is a MOVE: the evidence
// and what to do with it in the room, not a label on a record.
func talkingPointsSection(ranked *rankedClaims) []Sentence {
	claims := ranked.takeAll(ofKind(kindObjection, kindDecisionProcess, kindPriority, kindSuccessCriterion, kindCommitmentTheirs), talkingPointCap)
	out := make([]Sentence, 0, len(claims))
	for _, claim := range claims {
		out = append(out, Sentence{
			Text:     talkingPointLine(claim),
			Nature:   natureRecommendation,
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: claim.SourceID}},
		})
	}
	return out
}

const talkingPointCap = 5

func talkingPointLine(claim ClaimIn) string {
	switch claim.Kind {
	case kindObjection:
		if claim.Status == statusOpen {
			return fmt.Sprintf("%s objected to %s and we have not answered — bring the answer, or say when.", claim.PersonName, claim.Body)
		}
		return fmt.Sprintf("%s once objected to %s — confirm it is settled before moving on.", claim.PersonName, claim.Body)
	case kindDecisionProcess:
		return fmt.Sprintf("%s described how they decide: %s — walk the next step of it in the room.", claim.PersonName, claim.Body)
	case kindSuccessCriterion:
		return fmt.Sprintf("%s calls success %s — tie what you show to it.", claim.PersonName, claim.Body)
	case kindCommitmentTheirs:
		return fmt.Sprintf("%s owes us %s — ask where it stands.", claim.PersonName, claim.Body)
	default:
		return fmt.Sprintf("%s said %s matters — lead with it.", claim.PersonName, claim.Body)
	}
}

// companyContextSection (D) is background, collapsed and last.
//
// It answers "when did this room last meet, and about what" — the question a
// recurring delivery review opens with, and the one thing on the page that is
// about the CONVERSATION rather than the state of play.
//
// It used to say the company is where the lead attendee works, which the
// header already implies. The section kind is a closed enum by contract, so
// this replaces that line rather than adding a ninth heading, and the brief
// keeps its two-to-three-minute budget.
//
// Empty is honest and stays empty: a first meeting with a room has no history,
// and inventing background for one is the filler the spec's first rule forbids.
func companyContextSection(in Input) []Sentence {
	out := make([]Sentence, 0, len(in.PriorMeetings))
	for _, prior := range in.PriorMeetings {
		out = append(out, Sentence{
			Text:     priorMeetingLine(prior, in.Now),
			Evidence: []Evidence{{EntityType: citeActivity, EntityID: prior.ID}},
		})
	}
	return out
}
