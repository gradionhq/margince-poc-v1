// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// renderJobMetrics answers the exposition text for one snapshot.
func renderJobMetrics(t *testing.T, snap jobs.Snapshot) string {
	t.Helper()
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, snap); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	return buf.String()
}

// infoSeriesFor answers the one margince_job_declared_info line for a kind.
func infoSeriesFor(t *testing.T, exposition, kind string) string {
	t.Helper()
	prefix := `margince_job_declared_info{kind="` + kind + `"`
	for line := range strings.Lines(exposition) {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSuffix(line, "\n")
		}
	}
	t.Fatalf("no info series for kind %q\ngot:\n%s", kind, exposition)
	return ""
}

// TestTheInfoMetricCarriesEveryDeclaredKind — both job surfaces read
// river_job at scrape time, so a declared kind with no rows and a kind
// nobody ever wired look identical. The catalogue is what separates them,
// and an alert cannot join on a kind the exposition never names.
func TestTheInfoMetricCarriesEveryDeclaredKind(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{})

	if !strings.Contains(out, "# HELP margince_job_declared_info ") {
		t.Errorf("the catalogue has no HELP line; the text is the contract with whoever "+
			"reads a dashboard six months from now\ngot:\n%s", out)
	}
	declared := 0
	for kind := range jobs.Declared() {
		declared++
		want := `margince_job_declared_info{kind="` + kind + `"`
		if !strings.Contains(out, want) {
			t.Errorf("no info series for declared kind %q — an alert cannot join on a kind "+
				"the exposition never names", kind)
		}
	}
	if declared == 0 {
		t.Fatal("the declared table is empty; this gate would pass vacuously")
	}
	if got := strings.Count(out, "margince_job_declared_info{"); got != declared {
		t.Errorf("the catalogue carries %d series for %d declared kinds — a duplicate label "+
			"set makes Prometheus reject the entire scrape", got, declared)
	}
}

// TestTheCatalogueNamesEveryDeclaredKindsRole — a null workspace_id on a
// job row is correct for a dispatcher and a defect for a workspace kind,
// and the role is what lets a reader of the gauges tell which it is looking
// at without a second table.
func TestTheCatalogueNamesEveryDeclaredKindsRole(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{})

	for kind, spec := range jobs.Declared() {
		series := infoSeriesFor(t, out, kind)
		want := map[jobs.Role]string{
			jobs.Dispatcher: `role="dispatcher"`,
			jobs.Workspace:  `role="workspace"`,
		}[spec.Role]
		if want == "" {
			t.Fatalf("%s carries role %d, which the declaration does not define", kind, spec.Role)
		}
		if !strings.Contains(series, want) {
			t.Errorf("%s: want %s in %s", kind, want, series)
		}
	}
}

// TestADeclaredTimeoutIsNeverPublishedAsZeroSeconds — zero was ambiguous
// between River's silent one-minute default and a deliberate absence, which
// is the ambiguity this whole contract exists to remove. A deliberate
// absence is -1; a wall clock the operator sets is not knowable from the
// declaration at all, so the label is absent rather than guessed at.
func TestADeclaredTimeoutIsNeverPublishedAsZeroSeconds(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{})

	if strings.Contains(out, `timeout_seconds="0"`) {
		t.Errorf("a kind published a zero-second deadline, which reads as River's silent "+
			"minute and as a deliberate absence at the same time\ngot:\n%s", out)
	}

	var sawNone, sawOperator, sawFixed int
	for kind, spec := range jobs.Declared() {
		series := infoSeriesFor(t, out, kind)
		switch {
		case spec.Timeout.None:
			sawNone++
			if !strings.Contains(series, `timeout_seconds="-1"`) {
				t.Errorf("%s deliberately has no deadline; want timeout_seconds=\"-1\", got %s", kind, series)
			}
		case spec.Timeout.FromOperator():
			sawOperator++
			if strings.Contains(series, "timeout_seconds=") {
				t.Errorf("%s takes its wall clock from an operator's dial, which this process "+
					"does not hold — publishing a number here feeds an alert a deadline the "+
					"runtime does not honour. got %s", kind, series)
			}
		default:
			sawFixed++
			want := `timeout_seconds="` + strconv.FormatInt(int64(spec.Timeout.Fixed.Seconds()), 10) + `"`
			if !strings.Contains(series, want) {
				t.Errorf("%s: want %s in %s", kind, want, series)
			}
		}
	}
	if sawNone == 0 || sawOperator == 0 || sawFixed == 0 {
		t.Fatalf("covered %d none / %d operator / %d fixed policies; a branch with no declared "+
			"kind behind it is not actually gated here", sawNone, sawOperator, sawFixed)
	}
}

// TestTheQueueIsPublishedOnlyWhereTheContractGovernsIt — a caller-owned
// kind takes its insert options from scattered enqueue sites, so the
// declared queue is documentation rather than a governed value. Publishing
// it would hand an alert a lane the runtime never promised to use, which is
// exactly the declared-versus-actual drift the declaration exists to end.
func TestTheQueueIsPublishedOnlyWhereTheContractGovernsIt(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{})

	var sawCaller, sawGoverned int
	for kind, spec := range jobs.Declared() {
		series := infoSeriesFor(t, out, kind)
		if spec.OptsOwner == jobs.OptsCaller {
			sawCaller++
			if strings.Contains(series, "queue=") {
				t.Errorf("%s: the contract does not govern this kind's queue, so the "+
					"exposition must not publish one. got %s", kind, series)
			}
			continue
		}
		sawGoverned++
		want := `queue="` + spec.Queue + `"`
		if !strings.Contains(series, want) {
			t.Errorf("%s: want %s in %s", kind, want, series)
		}
	}
	if sawCaller == 0 || sawGoverned == 0 {
		t.Fatalf("covered %d caller-owned / %d contract-owned kinds; one side is unexercised",
			sawCaller, sawGoverned)
	}
}

// TestTheFanOutUnitSaysWhatOneChildStandsFor — a child row is one
// connection's renewal or one tenant's pass, and the two read identically
// on the gauges. A kind that fans out to nothing carries no unit rather
// than an invented one.
func TestTheFanOutUnitSaysWhatOneChildStandsFor(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{})

	names := map[jobs.FanOutUnit]string{
		jobs.FanOutWorkspace:  "workspace",
		jobs.FanOutConnection: "connection",
		jobs.FanOutBuild:      "build",
	}
	var sawUnit, sawNone int
	for kind, spec := range jobs.Declared() {
		series := infoSeriesFor(t, out, kind)
		name, fansOut := names[spec.FanOutUnit]
		if !fansOut {
			sawNone++
			if strings.Contains(series, "fan_out_unit=") {
				t.Errorf("%s fans out to nothing but published a unit: %s", kind, series)
			}
			continue
		}
		sawUnit++
		if want := `fan_out_unit="` + name + `"`; !strings.Contains(series, want) {
			t.Errorf("%s: want %s in %s", kind, want, series)
		}
	}
	if sawUnit == 0 || sawNone == 0 {
		t.Fatalf("covered %d fan-out / %d non-fan-out kinds; one side is unexercised", sawUnit, sawNone)
	}
}

// TestAnUndeclaredKindWithRowsIsReportedSeparately — rows for a kind the
// contract has since removed outlive it in River's retention, and folding
// them into the ordinary series is how a removed kind's work reads as
// healthy.
func TestAnUndeclaredKindWithRowsIsReportedSeparately(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "a_kind_no_longer_declared", Untenanted: true, State: "available", Count: 4},
	}})

	if !strings.Contains(out, `margince_job_unrecognised_kind{kind="a_kind_no_longer_declared"} 4`) {
		t.Errorf("rows for an undeclared kind must surface separately\ngot:\n%s", out)
	}
	if strings.Contains(out, `margince_job_declared_info{kind="a_kind_no_longer_declared"`) {
		t.Error("an undeclared kind was published as declared; the catalogue is the contract's, " +
			"never the job table's")
	}
	// Still counted where it is actually sitting: the depth gauge answers how
	// much work a queue is holding, and work of a retired kind is work.
	if !strings.Contains(out, `margince_job_queue_depth{queue="default",workspace_id=""} 4`) {
		t.Errorf("the undeclared rows vanished from the queue they are actually in\ngot:\n%s", out)
	}
}

// TestEveryStateOfAnUndeclaredKindIsCounted — the family answers "how much
// work of a kind nobody declares is in this table", so a row is counted
// whatever state it sits in. Counting only the waiting ones would report a
// retired kind's discarded backlog as nothing at all.
func TestEveryStateOfAnUndeclaredKindIsCounted(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "retired", Untenanted: true, State: "available", Count: 2},
		{Queue: "other", Kind: "retired", Untenanted: true, State: "discarded", Count: 3},
	}})

	if !strings.Contains(out, `margince_job_unrecognised_kind{kind="retired"} 5`) {
		t.Errorf("the family did not sum every state of the undeclared kind\ngot:\n%s", out)
	}
}

// TestAnIdleFleetEmitsNoUnrecognisedKindSeries — the family's whole purpose
// is to be absent, exactly as the unrecognised-STATE family next door is.
// A permanently empty series would be noise on every dashboard, and one
// that appeared on an idle fleet would be a false alarm on the surface an
// operator is meant to trust.
func TestAnIdleFleetEmitsNoUnrecognisedKindSeries(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{})

	if strings.Contains(out, "margince_job_unrecognised_kind") {
		t.Errorf("an idle fleet emitted an unrecognised-kind family, header and all\ngot:\n%s", out)
	}
}

// TestAFleetRunningOnlyDeclaredWorkStaysSilentToo — the case above proves
// an EMPTY table is silent; this proves a busy one is, which is the state
// the family actually has to stay quiet through.
func TestAFleetRunningOnlyDeclaredWorkStaysSilentToo(t *testing.T) {
	var rows []jobs.StateRow
	for kind := range jobs.Declared() {
		rows = append(rows, jobs.StateRow{Queue: "default", Kind: kind, Untenanted: true, State: "available", Count: 1})
	}
	out := renderJobMetrics(t, jobs.Snapshot{Rows: rows})

	if strings.Contains(out, "margince_job_unrecognised_kind") {
		t.Errorf("a fleet holding only declared work reported an unrecognised kind\ngot:\n%s", out)
	}
}

// TestAnUndeclaredKindsLabelCannotBreakTheWholeExposition — the kind comes
// off river_job verbatim, a table with no constraint on that column and
// direct app-role CRUD. Go's %q would encode a tab as \t, an escape the
// text format does not define, and a parser meeting one rejects the ENTIRE
// scrape rather than the single series.
func TestAnUndeclaredKindsLabelCannotBreakTheWholeExposition(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "q", Kind: "a\tb", Untenanted: true, State: "available", Count: 1},
		{Queue: "q", Kind: "<0x09>", Untenanted: true, State: "available", Count: 2},
	}})

	if strings.Contains(out, `\t`) {
		t.Errorf("an escape the text format does not define reached the wire\ngot:\n%s", out)
	}
	if !strings.Contains(out, `margince_job_unrecognised_kind{kind="a<0x09>b"} 1`) {
		t.Errorf("the control character was dropped or mis-escaped\ngot:\n%s", out)
	}
	// The injective half: a kind containing the six literal characters must
	// not render as the encoding of a real tab, or two distinct kinds collapse
	// into one label set and Prometheus rejects the scrape.
	if !strings.Contains(out, `margince_job_unrecognised_kind{kind="<0x3c>0x09>"} 2`) {
		t.Errorf("the literal escape sequence was not itself escaped\ngot:\n%s", out)
	}
}

// TestTheCatalogueIsWrittenInAStableOrder — a scrape target's series order
// should not flap between scrapes for no reason, and the catalogue is the
// longest family in the section.
func TestTheCatalogueIsWrittenInAStableOrder(t *testing.T) {
	snap := jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "q", Kind: "zeta_retired", Untenanted: true, State: "available", Count: 1},
		{Queue: "q", Kind: "alpha_retired", Untenanted: true, State: "available", Count: 1},
	}}
	first := renderJobMetrics(t, snap)
	for range 20 {
		if again := renderJobMetrics(t, snap); again != first {
			t.Fatal("two renders of one snapshot differed: the series order is unstable")
		}
	}
	if strings.Index(first, `margince_job_unrecognised_kind{kind="alpha_retired"`) >
		strings.Index(first, `margince_job_unrecognised_kind{kind="zeta_retired"`) {
		t.Error("the unrecognised-kind series are not in sorted order")
	}
}

// TestNoTimeoutPolicyResolvesToZeroSeconds covers the policies the compiled
// table does not carry today, which is where the zero can still come from:
// every declared timeout is minutes, so nothing above exercises a deadline
// shorter than the unit this label counts in, and truncating one would spell
// a real deadline as the absence the -1 exists to distinguish it from.
func TestNoTimeoutPolicyResolvesToZeroSeconds(t *testing.T) {
	cases := map[string]jobs.TimeoutPolicy{
		"sub-second": {Fixed: 500 * time.Millisecond},
		"one second": {Fixed: time.Second},
		"none":       {None: true},
	}
	for name, policy := range cases {
		seconds, stated := declaredTimeoutSeconds(policy)
		if !stated {
			t.Errorf("%s: the declaration states this deadline; the label must carry it", name)
			continue
		}
		if seconds == 0 {
			t.Errorf("%s: resolved to zero seconds, which reads as River's silent minute and "+
				"as a deliberate absence at the same time", name)
		}
	}

	// The two the file does not state: an operator's dial, and a Spec nobody
	// declared. Both must be reported as unstated rather than as a number.
	for name, policy := range map[string]jobs.TimeoutPolicy{
		"operator":   {OperatorField: "DeepReadCaps"},
		"undeclared": {},
	} {
		if _, stated := declaredTimeoutSeconds(policy); stated {
			t.Errorf("%s: the declaration states no deadline for this policy, so the exposition "+
				"must not publish one", name)
		}
	}
}

// TestARoleTheDeclarationDoesNotDefineIsNamedRatherThanBlank — the compiled
// table cannot carry one today, so this is about what a hand-edited or
// half-regenerated table would publish: a series with a blank role reads as
// a kind with no role, which is indistinguishable from a rendering bug.
func TestARoleTheDeclarationDoesNotDefineIsNamedRatherThanBlank(t *testing.T) {
	if got := roleName(jobs.Role(0)); got == "" {
		t.Error("a role the declaration does not define rendered as an empty label")
	}
	if roleName(jobs.Role(0)) == roleName(jobs.Dispatcher) {
		t.Error("an undefined role is indistinguishable from a dispatcher on the wire")
	}
}

// The ext_ split. A vanilla-built process scraping a composed database sees
// rows of a kind it has no declaration for on EVERY rolling deploy and every
// rollback, in both directions, because both builds run against one database
// for the length of the rollout. The three tests below are the same three every
// other family in this file gets — it is emitted, it is kept out of the family
// beside it, and it is absent when there is nothing to report.

// TestAnExtensionKindThisBuildDoesNotComposeIsItsOwnFamily — folding these rows
// into margince_job_unrecognised_kind would fire that alert on every deploy,
// and an alert that fires on every deploy is one nobody reads by the time a
// genuinely retired core kind appears.
func TestAnExtensionKindThisBuildDoesNotComposeIsItsOwnFamily(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "ext_absent_unit_refresh_ws", State: "available", Count: 4},
	}})

	if !strings.Contains(out, `margince_job_unrecognised_extension_kind{kind="ext_absent_unit_refresh_ws"} 4`) {
		t.Errorf("an ext_ kind this build does not compose was not reported\ngot:\n%s", out)
	}
	if strings.Contains(out, `margince_job_unrecognised_kind{kind="ext_absent_unit_refresh_ws"}`) {
		t.Errorf("an ext_ kind was counted in the CORE unrecognised family — that family is what an operator "+
			"alerts on for a retired kind, and a rolling deploy would trip it every time\ngot:\n%s", out)
	}
	// The core family must not appear at all here: with only ext_ rows in the
	// snapshot it has nothing to say, and an empty header is the noise both
	// families are written to avoid.
	if strings.Contains(out, "margince_job_unrecognised_kind{") {
		t.Errorf("the core unrecognised family appeared for a snapshot holding only ext_ rows\ngot:\n%s", out)
	}
	// Still counted where it is actually sitting, exactly as a retired core
	// kind's rows are: the depth gauge answers what a queue is holding.
	if !strings.Contains(out, `margince_job_queue_depth{queue="default"`) {
		t.Errorf("the ext_ rows vanished from the queue they are actually in\ngot:\n%s", out)
	}
}

// TestTheTwoUnrecognisedFamiliesSplitOneSnapshot — the split has to hold when
// both kinds of row are present, which is the realistic case: a rollout that
// adds a unit is also a rollout, and a core kind may have been retired in the
// same release.
func TestTheTwoUnrecognisedFamiliesSplitOneSnapshot(t *testing.T) {
	out := renderJobMetrics(t, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "retired_core_kind", State: "available", Count: 2},
		{Queue: "default", Kind: "ext_absent_unit_refresh", State: "available", Count: 3},
		{Queue: "other", Kind: "ext_absent_unit_refresh", State: "discarded", Count: 5},
	}})

	if !strings.Contains(out, `margince_job_unrecognised_kind{kind="retired_core_kind"} 2`) {
		t.Errorf("the core family lost its row\ngot:\n%s", out)
	}
	// Every state summed, for the reason the core family sums them: a discarded
	// backlog of a kind nobody composes is as much of an answer as a waiting one.
	if !strings.Contains(out, `margince_job_unrecognised_extension_kind{kind="ext_absent_unit_refresh"} 8`) {
		t.Errorf("the extension family did not sum every state\ngot:\n%s", out)
	}
}

// TestAComposedExtensionKindIsNotUnrecognised — the family reports what this
// build does not compose, so a kind the process DID declare through
// jobs.RegisterComposed must be absent from it and present in the catalogue,
// exactly as a core kind is.
func TestAComposedExtensionKindIsNotUnrecognised(t *testing.T) {
	const kind = "ext_composed_unit_refresh_ws"
	if err := jobs.RegisterComposed([]jobs.Spec{{
		Kind:      kind,
		GoType:    "extJobWorkspaceArgs",
		Role:      jobs.Workspace,
		Queue:     "default",
		Timeout:   jobs.TimeoutPolicy{Fixed: 5 * time.Minute},
		OptsOwner: jobs.OptsFanOut,
	}}); err != nil {
		t.Fatalf("RegisterComposed: %v", err)
	}
	t.Cleanup(func() {
		if err := jobs.RegisterComposed(nil); err != nil {
			t.Errorf("restoring the composed table: %v", err)
		}
	})

	out := renderJobMetrics(t, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: kind, State: "available", Count: 1},
	}})

	if strings.Contains(out, "margince_job_unrecognised_extension_kind") {
		t.Errorf("a kind this build composes was reported as one it does not\ngot:\n%s", out)
	}
	if !strings.Contains(out, `margince_job_declared_info{kind="`+kind+`"`) {
		t.Errorf("a composed kind is missing from the declared catalogue — the catalogue is what an alert "+
			"joins against, and a composed kind has to be joinable like a core one\ngot:\n%s", out)
	}
}

// TestAnIdleFleetEmitsNoUnrecognisedExtensionKindSeries — same posture as the
// core family next door: present only when it has something to report, so a
// permanently empty series is never on a dashboard.
func TestAnIdleFleetEmitsNoUnrecognisedExtensionKindSeries(t *testing.T) {
	if out := renderJobMetrics(t, jobs.Snapshot{}); strings.Contains(out, "margince_job_unrecognised_extension_kind") {
		t.Errorf("an idle fleet emitted an unrecognised-extension-kind family, header and all\ngot:\n%s", out)
	}
	// And a fleet holding only DECLARED work does not emit it either.
	out := renderJobMetrics(t, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: CloseDateSweepArgs{}.Kind(), State: "available", Count: 3},
	}})
	if strings.Contains(out, "margince_job_unrecognised_extension_kind") {
		t.Errorf("a healthy fleet emitted an unrecognised-extension-kind family\ngot:\n%s", out)
	}
}
