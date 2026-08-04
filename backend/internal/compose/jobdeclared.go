// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The two families that read the CLOSED set rather than the job table.
//
// Every other gauge in this section is a projection of river_job at scrape
// time, so it can only ever name a kind that happens to have rows. That
// collapses three different situations into one absence: a declared kind
// running idle, a kind nobody ever wired, and rows of a kind the contract no
// longer declares. The catalogue below names what is declared whether or not
// any row exists, and the unrecognised-kind gauge names what has rows and is
// not declared — between them the three states are told apart.

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// writeDeclaredInfo renders one series per declared kind, valued 1.
//
// It is written on every scrape, including one where the fleet is idle: the
// whole point is to be there when no row is, so an alert can join a kind's
// declaration against gauges that may legitimately have nothing to say about
// it. That is the opposite posture from the unrecognised families, which are
// absent unless they have something to report.
func writeDeclaredInfo(w io.Writer) error {
	const name = "margince_job_declared_info"
	if err := writeFamilyHeader(w, name,
		"Every job kind the contract declares, present whether or not the job table currently holds a row of it -- the catalogue an alert joins against, so an idle kind can be told apart from one nobody wired. The value is always 1. queue is absent where a kind's insert options belong to its callers rather than to the contract; timeout_seconds is -1 where the kind deliberately runs without a deadline, and absent where the wall clock is an operator's dial and so is not stated by the declaration at all."); err != nil {
		return err
	}
	for kind, spec := range jobs.Declared() {
		if _, err := fmt.Fprintf(w, "%s{%s} 1\n", name, declaredLabels(kind, spec)); err != nil {
			return err
		}
	}
	return nil
}

// declaredLabels renders one kind's label set.
//
// A label the contract does not actually govern is OMITTED rather than
// filled in, because a published number is one an alert will act on: the
// point of the declaration is to end declared-versus-actual drift, and
// inventing a value the runtime never promised to honour would reintroduce
// it on the surface built to detect it.
func declaredLabels(kind string, spec jobs.Spec) string {
	pairs := []string{
		labelPair("kind", kind),
		labelPair("role", roleName(spec.Role)),
	}
	// api/jobs.yaml records a queue for every kind, but it BINDS one only
	// where the contract supplies the insert options. A caller-owned kind
	// takes its queue from scattered enqueue sites, where nothing checks it
	// against this file.
	if spec.OptsOwner != jobs.OptsCaller {
		pairs = append(pairs, labelPair("queue", spec.Queue))
	}
	if unit := fanOutUnitName(spec.FanOutUnit); unit != "" {
		pairs = append(pairs, labelPair("fan_out_unit", unit))
	}
	if seconds, stated := declaredTimeoutSeconds(spec.Timeout); stated {
		pairs = append(pairs, labelPair("timeout_seconds", strconv.FormatInt(seconds, 10)))
	}
	return strings.Join(pairs, ",")
}

// labelPair renders one name=value pair through the injective escaper every
// other series in this section uses. Numbers go through it too: a label
// value is text on the wire whatever it means, and one spelling of the
// escaping is what keeps a value that must never reach a parser raw from
// finding a path that skips it.
func labelPair(name, value string) string {
	return name + "=" + label(value)
}

// roleName is the exposition's spelling of a declared role.
//
// The zero Role is a Spec nobody declared, which the generator cannot emit —
// it is named rather than left blank so a hand-edited or half-regenerated
// table shows up as a kind with an odd role instead of as a kind whose role
// label went missing, which reads as a rendering bug.
func roleName(r jobs.Role) string {
	switch r {
	case jobs.Dispatcher:
		return "dispatcher"
	case jobs.Workspace:
		return "workspace"
	}
	return "undeclared"
}

// fanOutUnitName says what ONE child of this kind stands for, and answers
// the empty string for a kind that fans out to nothing — the label is then
// omitted rather than given an invented value.
func fanOutUnitName(u jobs.FanOutUnit) string {
	switch u {
	case jobs.FanOutWorkspace:
		return "workspace"
	case jobs.FanOutConnection:
		return "connection"
	case jobs.FanOutBuild:
		return "build"
	}
	return ""
}

// declaredTimeoutSeconds answers a kind's declared wall clock in whole
// seconds, and whether the declaration states one at all.
//
// It never answers zero. Zero is River's silent one-minute default wearing
// the same digits as a deliberate absence, and telling those two apart is
// what the declaration exists for: a deliberate absence is -1, the value
// TimeoutPolicy.Duration itself hands River.
//
// An {operator: …} policy is not stated by the file — api/jobs.yaml calls it
// "not knowable here at all", and the value is computed at the worker's
// registration from a dial the exposition process does not hold. It is
// reported as unstated, so the label is absent rather than carrying a guess
// an alert would then act on.
//
// A deadline shorter than the second this label counts in is floored at one
// rather than truncated: truncation would spell a real deadline as the very
// zero the arms above exist to keep off the wire.
func declaredTimeoutSeconds(p jobs.TimeoutPolicy) (int64, bool) {
	switch {
	case p.None:
		return -1, true
	case p.FromOperator:
		return 0, false
	case p.Fixed > 0:
		return max(int64(p.Fixed/time.Second), 1), true
	}
	// A policy that is none of the three is a Spec nobody declared, whose
	// zero timeout is River's silent minute under another name. Publishing
	// nothing says that much honestly; publishing a number would not.
	return 0, false
}

// writeUnrecognisedKindGauge reports work whose kind the contract does not
// declare — the counterpart of the unrecognised-STATE gauge next door, and
// written on the same terms: only when it has something to report, because
// its whole purpose is to be absent.
//
// The rows it names are still counted by the gauges above. Those answer what
// a queue is actually holding, and work of a retired kind is work; removing
// it there would make the depth gauge understate the queue in order to make
// this one stand out.
func writeUnrecognisedKindGauge(w io.Writer, rows []jobs.StateRow) error {
	counts := undeclaredKindCounts(rows)
	if len(counts) == 0 {
		return nil
	}
	const name = "margince_job_unrecognised_kind"
	if err := writeFamilyHeader(w, name,
		"Jobs whose kind the contract does not declare -- rows of a kind that was removed, outliving it in River's job retention. Present only when such work exists; investigate rather than graph. These rows are also counted in the gauges above, which report what each queue is actually holding."); err != nil {
		return err
	}
	for _, kind := range sortedKeysOf(counts, strings.Compare) {
		if _, err := fmt.Fprintf(w, "%s{kind=%s} %d\n", name, label(kind), counts[kind]); err != nil {
			return err
		}
	}
	return nil
}

// undeclaredKindCounts totals every row of every kind the contract does not
// declare, across all states: the question is how much work of a kind nobody
// declares is in this table, and a retired kind's discarded backlog is as
// much an answer to that as its waiting rows are.
func undeclaredKindCounts(rows []jobs.StateRow) map[string]int64 {
	counts := map[string]int64{}
	for _, r := range rows {
		if _, declared := jobs.SpecFor(r.Kind); declared {
			continue
		}
		counts[r.Kind] += r.Count
	}
	return counts
}
