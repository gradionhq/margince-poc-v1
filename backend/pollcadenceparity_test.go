// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A connector that POSTPONES a tick on an unreachable provider asks to run again
// after a fixed delay, and that delay has to EQUAL the cadence its dispatcher
// already ticks at — and has to survive the seam's ceiling on the way to the
// queue.
//
// WHY THE EQUALITY IS LOAD-BEARING, and not a tidiness preference: a postponed
// child sits in `scheduled`, one of the states the fan-out's uniqueness window
// covers, so while it waits the dispatcher's next insert for that workspace
// collapses into it — the postponement REPLACES the tick it would have raced.
// That is the whole argument that an outage changes what a tick reports rather
// than how often it runs. Widen a unit's cadence to 600s and leave its delay at
// 120s and the argument quietly inverts: the snoozed row fires five times before
// the dispatcher would have run again, so the connector polls a refusing provider
// HARDER during an outage than it does in health. Nothing about either file would
// look wrong.
//
// THE CEILING IS PART OF THE SAME OBLIGATION, and leaving it out left the gate
// green over the exact inversion it exists to catch: jobs.rescheduleFor clamps to
// maxRescheduleDelay, so a unit declaring a 30-minute cadence and a matching
// 30-minute delay reconciles perfectly and then snoozes for fifteen — polling a
// refusing provider twice as hard during an outage as in health. The bound is read
// out of the seam's own source rather than trusted from a comment saying it "sits
// well above any cadence a connector declares", which was a claim about today's
// tree that nothing enforced.
//
// It lives HERE, at the root, rather than as a copy in each unit's own suite,
// because the obligation belongs to the tier and not to a unit. And the unit list
// is derived from who actually POSTPONES — a file calling extension.Reschedule —
// rather than from a filename, so a unit that moves the concept somewhere else, or
// a third connector written next year, is covered without anybody remembering a
// test exists. A unit that classifies but never postpones has nothing to
// reconcile and is correctly absent.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The two declarations this gate reconciles, each read as a scalar out of the
// file that owns it: the Go constant a unit postpones by, and the `cadence:` its
// jobs fragment declares. Regular expressions rather than a parser on either
// side — one number in one grammar, and a YAML dependency for a single scalar
// buys nothing a mismatch here would not already report.
var (
	// Deliberately anchored on the NAME and not on `^const`, so moving the
	// constant into a `const (...)` block does not silently take a unit out of
	// this gate's reach.
	retryDelayConstant = regexp.MustCompile(`(?m)^\s*(?:const\s+)?pollRetryDelay\s*=\s*(.+?)\s*$`)
	cadenceDeclaration = regexp.MustCompile(`(?m)^\s*cadence:\s*(\S+)\s*$`)
	rescheduleCeiling  = regexp.MustCompile(`(?m)^\s*(?:const\s+)?maxRescheduleDelay\s*=\s*(.+?)\s*$`)
	goDurationLiteral  = regexp.MustCompile(`^(\d+)\s*\*\s*time\.(Second|Minute|Hour)$`)
)

// faultSource is the seam that clamps a postponement. Read as text for the same
// reason the two declarations are: the bound is unexported, and exporting a
// constant so a test can read it puts a number on a package's API that nothing
// else needs.
const faultSource = "internal/platform/jobs/fault.go"

func TestEveryPostponingConnectorPostponesByItsOwnDeclaredCadence(t *testing.T) {
	ceiling, hasCeiling := durationIn(t, faultSource, rescheduleCeiling, "the postponement ceiling")
	if !hasCeiling {
		t.Fatalf("%s declares no maxRescheduleDelay — either the seam stopped clamping a postponement, in which case delete this half of the gate, or this expression no longer finds the bound", faultSource)
	}
	units, err := postponingUnits()
	if err != nil {
		t.Fatalf("scanning the extension tier: %v", err)
	}
	// A tree with NO postponing connector is a real state — the tier ships
	// whatever units it ships — but an empty glob is also what a renamed file or a
	// wrong relative path looks like, and those two must not be indistinguishable.
	// Two units postpone today; the gate reports its own reach rather than passing
	// silently over nothing.
	if len(units) == 0 {
		t.Fatal("no unit under extensions/ calls extension.Reschedule — either no connector postpones its ticks any more, in which case delete this gate, or this scan no longer reaches the tier")
	}
	for unit, source := range units {
		t.Run(unit, func(t *testing.T) {
			// NOT a skip. This unit postpones — that is how it got into the list —
			// so a missing delay is a unit asking the queue for an interval that no
			// declaration reconciles, which is the thing being gated rather than a
			// case to pass over.
			delay, declares := durationIn(t, source, retryDelayConstant, "a pollRetryDelay")
			if !declares {
				t.Fatalf("%s calls extension.Reschedule but declares no pollRetryDelay in %s — the interval it asks the queue for is reconciled against nothing", unit, source)
			}
			if delay > ceiling {
				t.Fatalf("%s postpones by %s and the seam clamps a postponement at %s — the delay it actually gets is the ceiling, so it would poll a refusing provider harder during an outage than it does in health", unit, delay, ceiling)
			}
			fragment := filepath.Join(filepath.Dir(source), "api", "jobs.yaml")
			cadence, hasCadence := durationIn(t, fragment, cadenceDeclaration, "a cadence")
			if !hasCadence {
				t.Fatalf("%s postpones its ticks by %s but declares no cadence in %s — there is nothing for the delay to agree with", unit, delay, fragment)
			}
			if delay != cadence {
				t.Fatalf("%s postpones by %s and its dispatcher ticks every %s — the two must be equal: a shorter delay polls a refusing provider harder during an outage than the connector does in health, and a longer one lets the dispatcher insert a second tick before the postponed row wakes, which is the collapse the postponement depends on",
					unit, delay, cadence)
			}
		})
	}
}

// postponingUnits maps each extension unit that calls extension.Reschedule to the
// file it calls it from.
//
// Deriving the list from the CALL rather than from a filename is what makes this
// gate reach a unit that keeps its disposition somewhere other than a
// pollfailure.go. A unit calling it from two files is refused rather than resolved
// by directory order: the delay is reconciled against one constant, and which file
// that constant lives in stops being obvious the moment there are two candidates.
func postponingUnits() (map[string]string, error) {
	sources, err := filepath.Glob("../extensions/*/*.go")
	if err != nil {
		return nil, err
	}
	found := make(map[string]string, len(sources))
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(raw), "extension.Reschedule(") {
			continue
		}
		unit := filepath.Base(filepath.Dir(source))
		if prior, dup := found[unit]; dup {
			return nil, fmt.Errorf("%s postpones from both %s and %s — which pollRetryDelay this gate reconciles would be decided by directory order", unit, prior, source)
		}
		found[unit] = source
	}
	return found, nil
}

// durationIn reads ONE duration out of one file with one expression, and refuses
// a file that declares the same thing twice.
//
// The duplicate refusal is the load-bearing half. A fragment that grew a second
// cadenced job would make "the declared cadence" ambiguous, and taking the first
// match would bind a connector's poll delay to some other job's clock — silently,
// and in the direction that reads as agreement.
func durationIn(t *testing.T, path string, expr *regexp.Regexp, what string) (time.Duration, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	found := expr.FindAllStringSubmatch(string(raw), -1)
	if len(found) == 0 {
		return 0, false
	}
	if len(found) > 1 {
		t.Fatalf("%s declares %s %d times — which one this gate reconciles would be decided by file order", path, what, len(found))
	}
	return parseDuration(t, path, what, found[0][1]), true
}

// parseDuration reads either spelling: the YAML fragment's `120s`, and the Go
// constant's `120 * time.Second`. Two grammars because two files own the number,
// and normalising them is the whole point of comparing them.
func parseDuration(t *testing.T, path, what, raw string) time.Duration {
	t.Helper()
	if parsed, err := time.ParseDuration(raw); err == nil {
		return parsed
	}
	parts := goDurationLiteral.FindStringSubmatch(raw)
	if parts == nil {
		t.Fatalf("%s declares %s as %q, which this gate cannot read as a duration — write it as `<n> * time.Second` or a Go duration string", path, what, raw)
	}
	parsed, err := time.ParseDuration(parts[1] + map[string]string{"Second": "s", "Minute": "m", "Hour": "h"}[parts[2]])
	if err != nil {
		t.Fatalf("%s declares %s as %q: %v", path, what, raw, err)
	}
	return parsed
}
