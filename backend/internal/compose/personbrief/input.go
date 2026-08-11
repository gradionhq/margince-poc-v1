// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personbrief

// What one brief is written from, and the fingerprint that decides whether a
// cached one still describes it.
//
// Nothing here re-queries. Every field is folded out of the Person360 the
// caller already assembled, which is what makes the brief's scope exactly the
// reader's own scope without a second set of gates to keep in agreement.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// promptVersion changes whenever anything about how a brief is WRITTEN
// changes — the instruction, the shape asked for, or the deterministic floor's
// wording. The floor's output is cached like the model's, so a reworded floor
// that did not bump this would serve its old sentences forever.
const promptVersion = "person-brief-v1"

// briefInputActivities bounds the timeline the brief reads. A brief is three
// to five sentences; a longer window buys nothing a reader will see and makes
// the fingerprint churn on activity that never changes the text.
const briefInputActivities = 10

// briefInputClaims bounds the claims the brief reads, newest first. The
// commitments card renders them all — the brief only needs enough to say what
// this person cares about.
const briefInputClaims = 8

// Input is what one brief is written from: who this person is commercially,
// what they have said they care about, and what recently happened — each
// already pruned to the reader's row scope by the read that produced it.
type Input struct {
	Name         string `json:"name"`
	Title        string `json:"title,omitempty"`
	Employer     string `json:"employer,omitempty"`
	BuyingRole   string `json:"buying_role,omitempty"`
	Strength     int    `json:"strength"`
	LastInbound  string `json:"last_inbound,omitempty"`
	LastOutbound string `json:"last_outbound,omitempty"`

	OpenDeal *DealIn   `json:"open_deal,omitempty"`
	Claims   []ClaimIn `json:"claims,omitempty"`
	Recent   []ActIn   `json:"recent,omitempty"`

	// SectionsOmitted names what the reader could NOT see. It rides the
	// fingerprint so two readers with different grants never share a cached
	// brief, and it tells the writer to stay silent about those sections
	// rather than inferring around the gap.
	SectionsOmitted []string `json:"sections_omitted,omitempty"`
}

// DealIn is the open deal this person sits on, as the brief reads it.
type DealIn struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stage string `json:"stage,omitempty"`
	// AmountMinor is the exact integer the column holds and what this package
	// does arithmetic on. It does NOT reach the model: MarshalJSON renders
	// `amount` from it in MAJOR units, because a prompt carrying minor units
	// once had a model read a 180,000 EUR deal as eighteen million and write
	// that onto a screen whose own card said 180,000. Dividing by 100 at the
	// point of use is wrong too — a zero-decimal currency has no minor unit —
	// so values.MajorUnits carries the ISO 4217 table.
	AmountMinor int64  `json:"-"`
	Currency    string `json:"currency,omitempty"`
	CloseDate   string `json:"close_date,omitempty"`
}

// MarshalJSON renders the deal's amount as a person would say it, derived from
// the integer at the moment it is written. Two spellings of one number that a
// caller can set independently are two numbers.
func (d DealIn) MarshalJSON() ([]byte, error) {
	type plain DealIn
	return json.Marshal(struct {
		plain
		Amount string `json:"amount,omitempty"`
	}{plain: plain(d), Amount: renderedAmount(d.AmountMinor, d.Currency)})
}

func renderedAmount(minor int64, currency string) string {
	if minor == 0 || currency == "" {
		return ""
	}
	return values.MajorUnits(minor, currency)
}

// ClaimIn is one thing this person said, as the brief reads it. The kind rides
// along because "she objected to X" and "she asked for X" are opposite claims
// about the same sentence, and the body alone loses which one it was.
type ClaimIn struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Body string `json:"body"`
	// SourceID is the activity the claim was read from — carried so a sentence
	// about a claim can cite the conversation rather than the derived row.
	SourceID string `json:"source_id"`
}

// ActIn is one recent timeline item as the brief reads it.
type ActIn struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Subject   string `json:"subject,omitempty"`
	Direction string `json:"direction,omitempty"`
	At        string `json:"at"`
}

// FromView folds the assembled 360 into the brief's input. Nothing re-queries:
// the reader's scope is already spent, and asking again would be a second set
// of gates that could disagree with the first.
func FromView(view crmcontracts.Person360) Input {
	in := Input{
		Name:            view.Person.FullName,
		SectionsOmitted: omittedNames(view.SectionsOmitted),
	}
	if view.Person.Title != nil {
		in.Title = *view.Person.Title
	}
	if view.Strength != nil {
		in.Strength = view.Strength.Score
	}
	in.LastInbound = stamp(view.LastInboundAt)
	in.LastOutbound = stamp(view.LastOutboundAt)
	in.Employer = currentEmployer(view)
	foldCommercial(&in, view)
	foldClaims(&in, view)
	foldRecent(&in, view)
	return in
}

// omittedNames renders the withheld sections as the plain strings the
// fingerprint hashes and the writer reads. The contract types them as an enum;
// the brief only needs to know which names are in the list.
func omittedNames(omitted []crmcontracts.Person360SectionsOmitted) []string {
	if len(omitted) == 0 {
		return nil
	}
	out := make([]string, 0, len(omitted))
	for _, section := range omitted {
		out = append(out, string(section))
	}
	return out
}

// currentEmployer names where this person works now. The 360 sorts the
// current-primary employment to index zero, so the first row is the answer.
func currentEmployer(view crmcontracts.Person360) string {
	if view.Employments == nil || len(view.Employments.Data) == 0 {
		return ""
	}
	first := view.Employments.Data[0]
	if !first.IsCurrentPrimary || first.OrganizationName == nil {
		return ""
	}
	return *first.OrganizationName
}

func foldCommercial(in *Input, view crmcontracts.Person360) {
	if view.Commercial == nil {
		return
	}
	if view.Commercial.Role != nil {
		in.BuyingRole = *view.Commercial.Role
	}
	deal := view.Commercial.Deal
	if deal == nil {
		return
	}
	folded := DealIn{ID: deal.DealId.String(), Name: deal.Title}
	if deal.Stage != nil {
		folded.Stage = *deal.Stage
	}
	if deal.AmountMinor != nil {
		folded.AmountMinor = *deal.AmountMinor
	}
	if deal.Currency != nil {
		folded.Currency = *deal.Currency
	}
	if deal.CloseDate != nil {
		folded.CloseDate = deal.CloseDate.String()
	}
	in.OpenDeal = &folded
}

func foldClaims(in *Input, view crmcontracts.Person360) {
	if view.Claims == nil {
		return
	}
	for _, claim := range *view.Claims {
		if len(in.Claims) == briefInputClaims {
			break
		}
		if claim.Status == crmcontracts.ConversationClaimStatusDismissed {
			// A dismissed claim is one a human said was never true. Writing a
			// brief from it would resurrect it in prose.
			continue
		}
		in.Claims = append(in.Claims, ClaimIn{
			ID:       claim.Id.String(),
			Kind:     string(claim.Kind),
			Body:     claim.Body,
			SourceID: claim.SourceActivityId.String(),
		})
	}
}

func foldRecent(in *Input, view crmcontracts.Person360) {
	if view.Activities == nil {
		return
	}
	for _, activity := range view.Activities.Data {
		if len(in.Recent) == briefInputActivities {
			break
		}
		folded := ActIn{
			ID:   activity.Id.String(),
			Kind: string(activity.Kind),
			At:   activity.OccurredAt.UTC().Format(time.RFC3339),
		}
		if activity.Subject != nil {
			folded.Subject = *activity.Subject
		}
		if activity.Direction != nil {
			folded.Direction = string(*activity.Direction)
		}
		in.Recent = append(in.Recent, folded)
	}
}

// stamp renders an optional instant in one fixed format, so two timestamps
// compare as strings the way the instants they name compare — and so the
// fingerprint does not churn on a formatting difference.
func stamp(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// Fingerprint keys the cache on everything that could change the text: the
// assembled input, the prompt version, and the model routing version.
//
// Keying on the person row would serve a brief describing a relationship that
// has since moved — activities and claims change without touching it.
// routingVersion folds in the model binding, so re-pointing a lane rewrites
// briefs instead of leaving text attributed to a model that no longer writes
// it.
func Fingerprint(in Input, routingVersion string) (string, error) {
	// json.Marshal orders struct fields by declaration, so the same input
	// hashes the same way across processes — a map would not.
	encoded, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("fingerprint the brief input: %w", err)
	}
	sum := sha256.Sum256([]byte(promptVersion + "\x00" + routingVersion + "\x00" + string(encoded)))
	return hex.EncodeToString(sum[:]), nil
}
