// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"fmt"
	"time"
)

// AgentSpec is one catalog entry: a named, scheduled, budgeted goal.
// The catalog is code, not configuration — adding an agent is a
// reviewed change, exactly like adding a workflow handler.
type AgentSpec struct {
	Name string
	Goal string
	// DueHourUTC is the daily trigger hour. Workspace-local scheduling
	// (each tenant's own 06:00) needs the workspace timezone plumbed
	// through the seeder; V1 runs the catalog on UTC.
	DueHourUTC int
	Budget     Budget
	// Tools is what this goal may call, and the SCOPE MODEL is why it has
	// to exist. A passport carries scopes, and `write` is all-or-nothing
	// across twelve verbs — so an agent that needs `log_activity` is
	// handed `archive_record`, `merge_records` and `update_record` with
	// it. Neither the passport nor the admission gate can say "this one
	// write and no other"; only the catalog entry can, because only it
	// knows what the goal is for.
	//
	// It NARROWS and never grants. Every call still passes the same gate
	// against the same passport, so this is a second lock and not a
	// second key — one registry, one audit stream, agent ≤ human
	// (ADR-0009 Decision 5). A name here the passport does not admit
	// stays refused.
	//
	// Required and non-empty, held by TestEveryAgentSpecNamesRegisteredTools:
	// an empty set is read as "no narrowing" at the Job seam, so a spec that
	// loses its list quietly regains the whole catalog — and a misspelt verb
	// is how an agent silently loses the one tool its goal depends on.
	Tools []string
}

// Catalog is the V1 agent set (B-EP06.22): the Morning Brief and the
// overnight at-risk sweep — the two judgment tasks Surface A and the
// deterministic workflow path structurally cannot do.
func Catalog() []AgentSpec {
	return []AgentSpec{
		{
			Name: "morning_brief",
			Goal: "Assemble the Morning Brief for this workspace: search for open deals, " +
				"read the ones with recent activity, and produce a ranked list (at most 7) of " +
				"deals the team can win this week. For each: why it is on the list, what changed " +
				"recently, and one recommended next move — every claim grounded in a record you " +
				"actually read, citing its id. A quiet day yields a short list; never pad it.",
			DueHourUTC: 6,
			// Reads only. list_records is here AND search_records is not
			// enough on its own: the goal enumerates OPEN deals, which is a
			// status filter, and search_records takes text and a type.
			Tools: []string{
				"search_records", "list_records", "read_record",
				"catch_me_up_on", "account_coverage", "whats_slipping_this_week",
			},
		},
		{
			Name: "overnight_at_risk_sweep",
			Goal: "Sweep this workspace's open deals for risk: find deals with no activity in " +
				"14+ days, stakeholders gone quiet, or missing next steps. Log ONE note activity " +
				"per at-risk deal summarizing the risk and the evidence (cite the records you " +
				"read). Do not advance stages, send anything, or archive anything.",
			DueHourUTC: 2,
			// The goal's last sentence is a prohibition the SCOPE model
			// cannot express: this sweep needs log_activity, and the write
			// scope that admits it also admits advance_deal, archive_record,
			// merge_records and eight more. The sentence stays — it stops the
			// model spending a step discovering the refusal — but this list
			// is what makes it true.
			Tools: []string{
				"search_records", "list_records", "read_record",
				"catch_me_up_on", "whats_slipping_this_week", "at_risk_relationships",
				"log_activity",
			},
		},
	}
}

// TriggerRef names one occurrence of a scheduled spec; the runner's
// idempotency (one run per trigger occurrence) hangs off this string.
func (a AgentSpec) TriggerRef(day time.Time) string {
	return fmt.Sprintf("%s:%s", a.Name, day.UTC().Format("2006-01-02"))
}

// DueAt is when the given day's occurrence becomes runnable.
func (a AgentSpec) DueAt(day time.Time) time.Time {
	d := day.UTC()
	return time.Date(d.Year(), d.Month(), d.Day(), a.DueHourUTC, 0, 0, 0, time.UTC)
}

// SpecByName resolves a stored job's spec; a job naming a spec the
// catalog no longer carries fails its run loudly.
func SpecByName(name string) (AgentSpec, bool) {
	for _, spec := range Catalog() {
		if spec.Name == name {
			return spec, true
		}
	}
	return AgentSpec{}, false
}
