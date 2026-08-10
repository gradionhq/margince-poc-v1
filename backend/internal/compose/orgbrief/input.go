// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// What the brief is written from, and the fingerprint that decides whether
// a cached one is still true.
//
// The input is assembled by asking the 360 — the same composite read the
// page itself renders — so the brief describes exactly what its reader can
// see, and cannot describe anything else. That is the whole per-viewer
// rule: it is not enforced by a filter here, it is inherited from running
// the caller's own gated read.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// promptVersion changes whenever ANYTHING about how a brief is written
// changes: the model prompt, the shape of the assembled input, or the wording
// the deterministic floor produces. It rides the fingerprint, so such a deploy
// invalidates every cached brief rather than serving text written the old way.
//
// The floor's wording counts because the floor's OUTPUT is what gets cached. A
// deploy that reworded it and left this alone kept serving the old sentences
// to every account whose facts had not moved — which is most of them.
const promptVersion = "org-brief-v5"

// Input is what one brief is written from: the account's identity, its
// pipeline, its people, and what has moved recently — each already pruned
// to the reader's row scope by the read that produced it.
type Input struct {
	Name         string    `json:"name"`
	Industry     string    `json:"industry,omitempty"`
	SizeBand     string    `json:"size_band,omitempty"`
	Strength     int       `json:"strength"`
	ContactCount int       `json:"contact_count"`
	Contacts     []NamedIn `json:"contacts,omitempty"`
	OpenDeals    []DealIn  `json:"open_deals,omitempty"`
	// WonLifetime is minor units, and reaches the model as `won_lifetime`
	// rendered — see MarshalJSON, and DealIn.AmountMinor for why.
	WonLifetime int64 `json:"-"`
	// WonCurrency is the won total's OWN currency — the workspace base, which
	// the 360 converts to at each deal's frozen close-time rate. It has no
	// relation to whatever the open deals are priced in, so it must never be
	// labelled with theirs.
	WonCurrency string   `json:"won_currency,omitempty"`
	LostCount   int      `json:"lost_count"`
	OpenTasks   []TaskIn `json:"open_tasks,omitempty"`
	Recent      []ActIn  `json:"recent,omitempty"`
	// SectionsOmitted names what the reader could NOT see. It rides the
	// fingerprint so two readers with different grants never share a cached
	// brief, and it tells the writer to stay silent about those sections
	// rather than inferring around the gap.
	SectionsOmitted []string `json:"sections_omitted,omitempty"`

	// Profile is what the COMPANY is — what it sells, to whom, how it
	// differentiates — as opposed to everything above, which is how it stands
	// with us. Curated statements a site read produced and a human accepted,
	// so the brief can describe the company without inventing a word about it.
	Profile []ProfileIn `json:"profile,omitempty"`
}

// NamedIn is a record the brief may write about and must be able to cite:
// contacts carry their ids for the same reason deals and activities do. Names
// alone invited the prompt to make a claim about a person that no citation
// could ground, so the sentence was dropped and the reader lost a true
// statement.
type NamedIn struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TaskIn is one open task the brief may write about.
//
// It carries the due date because a task sentence without one names a chore
// and says nothing about when it is wanted, and neither writer may infer that
// — the deterministic one has no other source for it, and the model must not
// guess it.
type TaskIn struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Due is RFC3339 in UTC, empty when the task carries no due date. The
	// format is fixed so two due dates compare as strings the way the instants
	// they name compare.
	Due string `json:"due,omitempty"`
}

// DealIn is one open deal as the brief reads it.
type DealIn struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stage string `json:"stage,omitempty"`
	// Amount is the MAJOR-unit figure, rendered — "180000.00", not the
	// 18000000 the column holds. The prompt carried minor units and said
	// nothing about it, so the model read a 180,000 EUR deal as eighteen
	// million and wrote that onto a customer-facing screen whose own card,
	// two inches above, said 180,000.
	//
	// Rendered here rather than divided at the point of use, because /100 is
	// wrong too: a zero-decimal currency has no minor unit, so dividing
	// understates ¥18,000,000 by a hundred. values.MajorUnits carries the
	// ISO-4217 table both this and the offer-draft price check read.
	// AmountMinor is the exact integer, and it is what the package does
	// ARITHMETIC on: the deterministic fallback sums open deals, and summing
	// rendered decimal strings would reintroduce the rounding a minor-unit
	// integer exists to prevent.
	//
	// It does not reach the model. MarshalJSON renders `amount` from it, so
	// the figure the model reads is DERIVED from the integer at the moment it
	// is written rather than stored beside it — two spellings of one number
	// that a caller can set independently are two numbers.
	AmountMinor int64  `json:"-"`
	Currency    string `json:"currency,omitempty"`
	Stalled     bool   `json:"stalled"`
}

// ActIn is one recent timeline item as the brief reads it.
type ActIn struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject,omitempty"`
	At      string `json:"at"`
	// Done says whether a timeline item that CAN be finished has been. It is a
	// pointer because most items cannot: a call happened, and asking whether it
	// is "done" is a category error that a plain false would answer anyway.
	//
	// Without it the same task reached the model twice — once under open_tasks
	// and once here, as a past-dated timeline row with no state — and nothing
	// linked the two shapes or said the second was still outstanding. The model
	// did the reasonable thing with a dated entry and wrote that the account's
	// open tasks had been completed, directly above a card showing one of them
	// overdue.
	Done *bool `json:"done,omitempty"`
}

// MarshalJSON writes the amount as the figure a person would say — "180000.00"
// for 18000000 EUR, "18000000" for the same integer in JPY — rather than the
// minor-unit integer the column holds.
//
// The prompt used to carry the integer, under a key that said `amount_minor` to
// this file and nothing at all to the model. It read a 180,000 EUR deal as
// eighteen million and wrote that onto a customer-facing screen whose own card,
// two inches above, said 180,000.
//
// Derived here rather than divided at the point of use, and derived rather than
// stored, for two different reasons. `/100` is wrong for a zero-decimal
// currency — ¥18,000,000 IS eighteen million yen — so the ISO-4217 table
// decides the scale. And a rendered copy kept beside the integer is a second
// number a caller can set on its own; taking it at the moment of writing means
// there is only ever one.
func (d DealIn) MarshalJSON() ([]byte, error) {
	type wire DealIn // no methods, so no recursion back into this one
	return json.Marshal(struct {
		wire
		Amount string `json:"amount,omitempty"`
	}{wire: wire(d), Amount: renderedAmount(d.AmountMinor, d.Currency)})
}

// MarshalJSON renders the won-to-date total for the reason DealIn's does: it
// was minor units under a name only this file understood.
func (in Input) MarshalJSON() ([]byte, error) {
	type wire Input
	return json.Marshal(struct {
		wire
		WonLifetime string `json:"won_lifetime,omitempty"`
	}{wire: wire(in), WonLifetime: renderedAmount(in.WonLifetime, in.WonCurrency)})
}

// renderedAmount is the one rendering both use. An amount with no currency
// renders as nothing: a figure printed without its code is a number whose scale
// the reader has to guess, which is the defect rather than a lesser form of it.
func renderedAmount(minor int64, currency string) string {
	if minor == 0 || currency == "" {
		return ""
	}
	return values.MajorUnits(minor, currency)
}

// briefInputActivities bounds how much of the timeline the brief reads. A
// brief is about what is happening now; a longer window costs prefill and
// buys older news.
const briefInputActivities = 12

// FromView assembles the input from an already-read 360. Nothing here
// re-queries: the 360 ran under the caller's gates, so anything absent from
// it is absent because that caller may not see it.
func FromView(view crmcontracts.Organization360) Input {
	in := Input{Name: view.Organization.DisplayName}
	if view.Organization.Industry != nil {
		in.Industry = *view.Organization.Industry
	}
	if view.Organization.SizeBand != nil {
		in.SizeBand = string(*view.Organization.SizeBand)
	}
	for _, omitted := range view.SectionsOmitted {
		in.SectionsOmitted = append(in.SectionsOmitted, string(omitted))
	}
	if view.Strength != nil {
		in.Strength = view.Strength.Score
		in.ContactCount = view.Strength.ContactCount
	}
	if view.People != nil {
		for _, contact := range view.People.Data {
			in.Contacts = append(in.Contacts, NamedIn{
				ID: contact.PersonId.String(), Name: contact.FullName,
			})
		}
	}
	foldDeals(view, &in)
	foldTasks(view, &in)
	foldRecent(view, &in)
	return in
}

func foldDeals(view crmcontracts.Organization360, in *Input) {
	if view.Deals == nil {
		return
	}
	in.LostCount = view.Deals.LostCount
	if view.Deals.WonLifetime.AmountMinor != nil && view.Deals.WonLifetime.Currency != nil {
		in.WonCurrency = *view.Deals.WonLifetime.Currency
		in.WonLifetime = *view.Deals.WonLifetime.AmountMinor
	}
	for _, deal := range view.Deals.Data {
		d := DealIn{ID: deal.DealId.String(), Name: deal.Name, Stalled: deal.Stalled}
		if deal.StageName != nil {
			d.Stage = *deal.StageName
		}
		// Both halves or neither: a figure with no currency cannot be rendered
		// into major units at all, and one printed without its code is a number
		// whose scale the reader has to guess — which is the whole defect.
		if deal.Amount != nil && deal.Amount.AmountMinor != nil && deal.Amount.Currency != nil {
			d.Currency = *deal.Amount.Currency
			d.AmountMinor = *deal.Amount.AmountMinor
		}
		in.OpenDeals = append(in.OpenDeals, d)
	}
}

func foldTasks(view crmcontracts.Organization360, in *Input) {
	if view.NextSteps == nil {
		return
	}
	for _, step := range view.NextSteps.Data {
		task := TaskIn{ID: step.ActivityId.String(), Name: step.Subject}
		if step.DueAt != nil {
			task.Due = step.DueAt.UTC().Format(time.RFC3339)
		}
		in.OpenTasks = append(in.OpenTasks, task)
	}
}

// foldRecent takes the newest slice of the timeline. A brief is about what
// is happening now; a longer window costs prefill and buys older news.
func foldRecent(view crmcontracts.Organization360, in *Input) {
	if view.Activities == nil {
		return
	}
	for i, activity := range view.Activities.Data {
		if i >= briefInputActivities {
			break
		}
		act := ActIn{
			ID:   activity.Id.String(),
			Kind: string(activity.Kind),
			At:   activity.OccurredAt.UTC().Format(time.RFC3339),
		}
		if activity.Subject != nil {
			act.Subject = *activity.Subject
		}
		// Only for the kinds that can BE finished. A call or a mail is not
		// outstanding or complete, and answering false for one would invent a
		// state the record does not have.
		if activity.Kind == crmcontracts.ActivityKindTask {
			done := activity.IsDone != nil && *activity.IsDone
			act.Done = &done
		}
		in.Recent = append(in.Recent, act)
	}
}

// Fingerprint identifies the assembled input, together with the prompt and
// the routing that will turn it into prose.
//
// It hashes the INPUT rather than the organization's row version, because
// facts, deals, activities and grants all move without touching that row —
// a version-keyed cache would serve a brief describing a pipeline the
// account no longer has. routingVersion folds in the model binding, so
// re-pointing a lane rewrites briefs instead of leaving text attributed to
// a model that no longer writes it.
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

// ProfileIn is one curated statement about the company. The field name rides
// along because it is what the statement ANSWERS — "who they sell to" reads
// very differently from "what they sell", and the value alone loses that.
type ProfileIn struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// briefProfileFields is the subset worth putting in front of a salesperson,
// in the order a person would ask. The store holds sixteen fields; the rest
// are registry and address detail that describe a legal entity rather than a
// business, and a brief that recited them would read like a company register.
var briefProfileFields = []string{
	"offer_summary",
	"icp",
	"value_proposition",
	"usp",
	"customer_pains",
	"desired_outcomes",
	"buying_center",
	"sales_motion",
}

// briefProfileValueMax bounds one statement, in CHARACTERS. These are prose
// fields with no length cap of their own, and a single essay would crowd out
// every other fact the card is written from.
const briefProfileValueMax = 400

// withoutProfile is the Input as ASK may see it. Ask restates facts and never
// judges, so the curated company prose stays out of its prompt and is quoted
// verbatim instead — a paraphrase nobody checked is worth less than the
// sentence a human already accepted.
//
// The BRIEF now receives it: the brief is allowed to assess, and a fit
// assessment cannot be written without knowing what the company sells. What
// keeps that honest there is the nature label, not withholding the text.
func (in Input) withoutProfile() Input {
	in.Profile = nil
	return in
}

// foldProfile takes the curated statements in a fixed order, so the same
// account fingerprints the same way whatever order the store returned.
func (in *Input) foldProfile(fields []crmcontracts.CompanyProfileField) {
	byField := make(map[string]string, len(fields))
	for _, field := range fields {
		value := truncateRunes(strings.TrimSpace(field.Value), briefProfileValueMax)
		if value == "" {
			continue
		}
		byField[string(field.Field)] = value
	}
	for _, name := range briefProfileFields {
		if value, ok := byField[name]; ok {
			in.Profile = append(in.Profile, ProfileIn{Field: name, Value: value})
		}
	}
}

// truncateRunes cuts at a character boundary. A byte slice through German
// prose splits the umlaut that straddles the limit, and the broken sequence
// reaches the reader as the replacement character — from a field whose whole
// promise is that it shows their own approved words.
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	kept := 0
	for offset := range value {
		if kept == limit-1 {
			// The ellipsis says the statement was cut, and counts against the
			// limit rather than pushing past it. Without it the card shows an
			// approved sentence that stops mid-thought and reads as though the
			// author wrote it that way.
			return value[:offset] + "…"
		}
		kept++
	}
	return value
}
