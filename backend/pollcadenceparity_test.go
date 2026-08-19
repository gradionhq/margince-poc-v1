// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A connector that POSTPONES a tick on an unreachable provider asks to run again
// after a fixed delay, and that delay has to be at least the cadence its
// dispatcher already ticks at.
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
// It lives HERE, at the root, rather than as a copy in each unit's own suite,
// because the obligation belongs to the tier and not to a unit. It is derived
// from the tree — every unit that declares a `pollRetryDelay` is checked against
// its own `api/jobs.yaml` — so a THIRD connector that postpones its ticks is
// covered on the day it is written, by nobody remembering to copy a test.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// The two declarations this gate reconciles, each read as a scalar out of the
// file that owns it: the Go constant a unit postpones by, and the `cadence:` its
// jobs fragment declares. Regular expressions rather than a parser on either
// side — one number in one grammar, and a YAML dependency for a single scalar
// buys nothing a mismatch here would not already report.
var (
	retryDelayConstant = regexp.MustCompile(`(?m)^const pollRetryDelay = (.+)$`)
	cadenceDeclaration = regexp.MustCompile(`(?m)^\s*cadence:\s*(\S+)\s*$`)
	goDurationLiteral  = regexp.MustCompile(`^(\d+)\s*\*\s*time\.(Second|Minute|Hour)$`)
)

func TestEveryPostponingConnectorPostponesByItsOwnDeclaredCadence(t *testing.T) {
	units, err := filepath.Glob("../extensions/*/pollfailure.go")
	if err != nil {
		t.Fatalf("scanning the extension tier: %v", err)
	}
	// A tree with NO postponing connector is a real state — the tier ships
	// whatever units it ships — but an empty glob is also what a renamed file or a
	// wrong relative path looks like, and those two must not be indistinguishable.
	// Two units postpone today; the gate reports its own reach rather than passing
	// silently over nothing.
	if len(units) == 0 {
		t.Fatal("no extensions/*/pollfailure.go found — either no connector postpones its ticks any more, in which case delete this gate, or this path no longer reaches the tier")
	}
	checked := 0
	for _, source := range units {
		unit := filepath.Base(filepath.Dir(source))
		t.Run(unit, func(t *testing.T) {
			delay, declares := durationIn(t, source, retryDelayConstant, "a pollRetryDelay constant")
			if !declares {
				t.Skipf("%s declares no pollRetryDelay, so it postpones nothing to reconcile", unit)
			}
			checked++
			fragment := filepath.Join(filepath.Dir(source), "api", "jobs.yaml")
			cadence, hasCadence := durationIn(t, fragment, cadenceDeclaration, "a cadence")
			if !hasCadence {
				t.Fatalf("%s postpones its ticks by %s but declares no cadence in %s — there is nothing for the delay to agree with", unit, delay, fragment)
			}
			if delay != cadence {
				t.Fatalf("%s postpones by %s and its dispatcher ticks every %s — a delay shorter than the cadence polls a refusing provider harder during an outage than the connector does in health",
					unit, delay, cadence)
			}
		})
	}
	if checked == 0 {
		t.Fatal("every unit with a pollfailure.go was skipped, so this gate reconciled nothing")
	}
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
