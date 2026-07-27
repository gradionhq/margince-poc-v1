// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
)

// The three states a shipped site's certification can be in. They are separate
// words because they are separate claims: a current record describes what this
// build sends, a stale one describes prompts it no longer sends, and an absent
// one describes nothing. Collapsing stale into absent would hide a lie behind a
// gap; collapsing absent into a zeroed row would print a measurement nobody
// made.
const (
	statusCurrent = "current"
	statusStale   = "stale"
	statusAbsent  = "absent"
)

// unmeasured is what a column shows when there is no record to read it from. A
// dash rather than a zero, because zero accepted runs is a result and "never
// run" is not.
const unmeasured = "-"

// reportColumns is the table's header and, by its length, how wide a row is —
// so an absent site's dashes and a certified site's numbers cannot drift apart
// from the header they sit under.
var reportColumns = []string{
	"SITE", "SCOPE", "STATUS", "BAND",
	"PROVIDER", "MODEL", "ENV",
	"RUNS", "PASSED", "RELIABILITY",
	"ACCEPTED", "WRONG_ANSWER", "INVALID", "ABSTAINED",
}

// readinessRow is one shipped site as the report renders it: the site itself,
// which is known from the census alone, the record covering it, which may not
// exist, and THAT SITE'S OWN share of it. certified says which of the two a row
// is — a Record zero value and a genuinely all-zero record are
// indistinguishable otherwise.
//
// The tally is per site rather than per record because a record is written per
// TASK: printing the task's pooled numbers on each of its sites' rows gives
// four identical rows wearing four different labels, and a reader cannot tell
// which site the numbers came from — which is the whole reason the row is the
// site.
type readinessRow struct {
	site      aitasks.Site
	record    aicert.Record
	tally     aicert.SiteTally
	certified bool
	stale     bool
}

// renderReadiness reports every shipped site's certification state: the band a
// record reached, the per-outcome counts behind it, the scope the run actually
// covered, and the (provider, model, env) it was measured on.
//
// The sites come from the census rather than from the records, because absence
// is the finding this report exists to surface and a missing record cannot name
// itself. stamps is the stamp this build computes per task (currentStamps): a
// record carrying another one was scored against something this build no longer
// sends.
//
// Nothing here fails or exits non-zero. The certification lane is paid, manual
// and BYOK-gated, so this is a view a human reads before a release decision —
// not a gate, which would make every prompt edit wait on a paid run.
func renderReadiness(sites []aitasks.Site, stamps map[string]string, records []aicert.Record) string {
	rows, unclaimed := readinessRows(sites, stamps, records)

	var buf strings.Builder
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	var writeErr error
	writeRow := func(format string, args ...any) {
		if writeErr != nil {
			return
		}
		_, writeErr = fmt.Fprintf(w, format, args...)
	}

	writeRow("%s\n\n", summarize(rows))
	writeRow("%s\n", strings.Join(reportColumns, "\t"))
	for _, row := range rows {
		writeRow("%s\n", strings.Join(row.cells(), "\t"))
	}
	for _, line := range legend(rows, sites, unclaimed) {
		writeRow("%s\n", line)
	}
	if writeErr == nil {
		writeErr = w.Flush()
	}
	if writeErr != nil {
		// An in-memory strings.Builder never actually errors, but a write into
		// the tabwriter is still, mechanically, a write — checked like any other
		// rather than assumed infallible.
		return fmt.Sprintf("aicert: formatting the readiness report: %v\n", writeErr)
	}
	return buf.String()
}

// readinessRows pairs every shipped site with the records covering it, and
// returns alongside them the records no shipped site claims.
//
// A record is written per TASK and covers every site that task ships, so one
// record can produce several rows and a task certified on two bindings gives a
// site two. The site is the unit here — it is what the contract ships and what
// a scenario names — and each row carries only the scenarios that ran on it.
func readinessRows(sites []aitasks.Site, stamps map[string]string, records []aicert.Record) ([]readinessRow, []aicert.Record) {
	byTask := map[string][]aicert.Record{}
	for _, rec := range records {
		byTask[rec.Task] = append(byTask[rec.Task], rec)
	}

	rows := make([]readinessRow, 0, len(sites))
	claimed := map[string]bool{}
	for _, site := range sites {
		task := string(site.Task)
		measured := false
		for _, rec := range byTask[task] {
			// A record covers a site only if it ran a scenario ON that site.
			// One that did not measured nothing here, and a row built from its
			// pooled numbers would report another site's runs under this name.
			tally, ok := rec.ForSite(site.Variant)
			if !ok {
				continue
			}
			claimed[recordKey(rec)] = true
			measured = true
			rows = append(rows, readinessRow{
				site: site, record: rec, tally: tally, certified: true,
				stale: rec.PromptVersion != stamps[task],
			})
		}
		if !measured {
			rows = append(rows, readinessRow{site: site})
		}
	}

	var unclaimed []aicert.Record
	for _, rec := range records {
		if !claimed[recordKey(rec)] {
			unclaimed = append(unclaimed, rec)
		}
	}
	return rows, unclaimed
}

// currentStamps is the certification stamp this build computes per task — the
// value a record's own stamp must equal to still describe what ships. A task
// with no scenarios gets no entry, so every record for it reads as stale: a
// record whose scenarios are gone cannot be current against anything.
//
// It fails rather than skipping a task it cannot stamp. The stamp covers the
// request each site's own code builds, so a task that cannot be stamped is a
// corpus this build cannot run — and every record for it would otherwise read
// as stale for a reason the report never states.
func currentStamps(ctx context.Context, corpus []aicert.Scenario, census *aitasks.Registry) (map[string]string, error) {
	byTask := map[string][]aicert.Scenario{}
	for _, sc := range corpus {
		byTask[sc.Task] = append(byTask[sc.Task], sc)
	}
	stamps := make(map[string]string, len(byTask))
	for task, scenarios := range byTask {
		stamp, err := aicert.PromptVersion(ctx, scenarios, census)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", task, err)
		}
		stamps[task] = stamp
	}
	return stamps, nil
}

// recordKey identifies one record the way its own file path does — the four
// fields that make it a distinct measurement.
func recordKey(rec aicert.Record) string {
	return rec.Task + "/" + rec.Provider + "/" + rec.ServedModel + "/" + rec.EnvClass
}

// status names which of the three states this row is in.
func (r readinessRow) status() string {
	switch {
	case !r.certified:
		return statusAbsent
	case r.stale:
		return statusStale
	default:
		return statusCurrent
	}
}

// scope is how much of the site the row can claim. With a record it is what that
// run folded to; with none it is the most a run could ever cover, which is how
// the agent loop's one-turn limit stays visible while nothing is certified.
func (r readinessRow) scope() string {
	if !r.certified {
		return r.site.CertifiedScope()
	}
	if r.record.CertifiedScope == "" {
		return unmeasured
	}
	return r.record.CertifiedScope
}

// cells renders the row's columns, one per reportColumns entry. An absent row
// pads with dashes rather than the record's zero value: every number below is a
// count of runs that happened.
func (r readinessRow) cells() []string {
	site := string(r.site.Task) + "/" + r.site.Variant
	if !r.certified {
		cells := []string{site, r.scope(), r.status()}
		for len(cells) < len(reportColumns) {
			cells = append(cells, unmeasured)
		}
		return cells
	}
	rec, tally := r.record, r.tally
	return []string{
		site, r.scope(), r.status(), tally.Verdict,
		rec.Provider, rec.ServedModel, rec.EnvClass,
		fmt.Sprintf("%d", tally.Runs), fmt.Sprintf("%d", tally.Passed),
		fmt.Sprintf("%.2f", tally.Reliability()),
		fmt.Sprintf("%d", tally.ReportedAccepted), fmt.Sprintf("%d", tally.ReportedWrongAnswer),
		fmt.Sprintf("%d", tally.ReportedInvalid), fmt.Sprintf("%d", tally.ReportedAbstained),
	}
}

// summarize opens the report with the one number a reader wants first: how much
// of what ships is currently certified at all.
func summarize(rows []readinessRow) string {
	sites, covered := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		key := string(row.site.Task) + "/" + row.site.Variant
		sites[key] = true
		if row.status() == statusCurrent {
			covered[key] = true
		}
	}
	return fmt.Sprintf("AI certification readiness: %d of %d shipped sites carry a current record.",
		len(covered), len(sites))
}

// legend states what the table's words mean and what a row does NOT say. Both
// belong in the output rather than in a reader's head: the binding is part of
// every claim here, and a row read as a property of the product rather than of
// one deployment is the way this report would mislead.
func legend(rows []readinessRow, sites []aitasks.Site, unclaimed []aicert.Record) []string {
	lines := []string{
		"",
		"Every row is one (provider, model, env) binding: a band says what that deployment did,",
		"and green-lights no other. A record is written per task and covers every site that task",
		"ships, so sites sharing a task share its record — but each row's numbers are that SITE's",
		"own scenarios, never the task's pooled total.",
		"RUNS/PASSED is how often the site did what its scenarios asked. ACCEPTED, WRONG_ANSWER,",
		"INVALID and ABSTAINED are what the site's validator REPORTED: a run can be accepted and",
		"still fail, when the scenario asked for an abstention.",
	}
	var stale, absent int
	for _, row := range rows {
		switch row.status() {
		case statusStale:
			stale++
		case statusAbsent:
			absent++
		}
	}
	if stale > 0 {
		lines = append(lines,
			fmt.Sprintf("%d stale: the record was scored against a scenario, or a prompt built from one,", stale),
			"that this build no longer sends. Re-certify to make its band a claim again.")
	}
	if absent > 0 {
		lines = append(lines,
			fmt.Sprintf("%d absent: never certified on any binding. This is the honest state, not a failure —", absent),
			"the columns are dashes because no run has measured them.")
	}
	if stale > 0 || absent > 0 {
		lines = append(lines,
			"Run `make e2e-ai TASK=<task> MODEL=<provider:model>` to certify (paid: real model, real network).")
	}
	shippedTasks := map[string]bool{}
	for _, site := range sites {
		shippedTasks[string(site.Task)] = true
	}
	for _, rec := range unclaimed {
		name := fmt.Sprintf("Record %s/%s_%s_%s", rec.Task, rec.Provider, rec.ServedModel, rec.EnvClass)
		if !shippedTasks[rec.Task] {
			lines = append(lines, name+" covers no site this build registers — a stale artifact of a task that has moved.")
			continue
		}
		// The record's task ships, but nothing in it says which site each run
		// measured, so no row can carry its numbers honestly.
		lines = append(lines, name+" names no scenario of any site its task ships, so no row above can attribute it.")
	}
	return lines
}
