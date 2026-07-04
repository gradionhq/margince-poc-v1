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
		},
		{
			Name: "overnight_at_risk_sweep",
			Goal: "Sweep this workspace's open deals for risk: find deals with no activity in " +
				"14+ days, stakeholders gone quiet, or missing next steps. Log ONE note activity " +
				"per at-risk deal summarizing the risk and the evidence (cite the records you " +
				"read). Do not advance stages, send anything, or archive anything.",
			DueHourUTC: 2,
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
