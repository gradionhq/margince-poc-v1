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
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// promptVersion changes whenever the prompt or the shape of the assembled
// input changes. It rides the fingerprint, so a deploy that rewrites the
// prompt invalidates every cached brief rather than serving text written to
// the old instructions.
const promptVersion = "org-brief-v2"

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
	WonLifetime  int64     `json:"won_lifetime_minor"`
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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Stage       string `json:"stage,omitempty"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency,omitempty"`
	Stalled     bool   `json:"stalled"`
}

// ActIn is one recent timeline item as the brief reads it.
type ActIn struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject,omitempty"`
	At      string `json:"at"`
}

// briefInputActivities bounds how much of the timeline the brief reads. A
// brief is about what is happening now; a longer window costs prefill and
// buys older news.
const briefInputActivities = 12

// FromView assembles the input from an already-read 360. Nothing here
// re-queries: the 360 ran under the caller's gates, so anything absent from
// it is absent because that caller may not see it.
func FromView(view crmcontracts.Organization360) Input {
	in := Input{
		Name:        view.Organization.DisplayName,
		WonLifetime: 0,
	}
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
	if view.Deals.WonLifetime.AmountMinor != nil {
		in.WonLifetime = *view.Deals.WonLifetime.AmountMinor
		if view.Deals.WonLifetime.Currency != nil {
			in.WonCurrency = *view.Deals.WonLifetime.Currency
		}
	}
	for _, deal := range view.Deals.Data {
		d := DealIn{ID: deal.DealId.String(), Name: deal.Name, Stalled: deal.Stalled}
		if deal.StageName != nil {
			d.Stage = *deal.StageName
		}
		if deal.Amount != nil && deal.Amount.AmountMinor != nil {
			d.AmountMinor = *deal.Amount.AmountMinor
			if deal.Amount.Currency != nil {
				d.Currency = *deal.Amount.Currency
			}
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
