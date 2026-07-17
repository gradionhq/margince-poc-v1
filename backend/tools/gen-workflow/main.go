// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command gen-workflow scaffolds a new workflow.Handler (AC-W1,
// features/03 §5.3): a write-once handler skeleton plus its test stub,
// shaped to compile against internal/shared/ports/workflow.Handler as
// emitted. It stops there on purpose — it does not touch Catalog() or
// StarterWorkflows(). Wiring a scaffold into the registry (and getting
// the catalog Key to match Spec().Name) is a deliberate, reviewed edit
// a generator must never paper over, so it prints the steps instead of
// taking them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var dir = flag.String("dir", "internal/modules/automation", "package directory to scaffold the handler into")

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	handlerPath, testPath, err := generate(*dir, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-workflow:", err)
		os.Exit(1)
	}

	fmt.Printf("scaffolded:\n  %s\n  %s\n\n", handlerPath, testPath)
	fmt.Print(nextSteps(args[0]))
}

const usage = `usage: gen-workflow [-dir path] <handler_name>

  handler_name  snake_case, e.g. flag_idle_deals — becomes the emitted
                handler's Spec().Name and the file names.
  -dir          package directory to scaffold into (default
                internal/modules/automation)`

// nameRE is the snake_case shape every starter handler's catalog Key
// already uses (route_lead, stage_change_create_task): lowercase words
// joined by single underscores, so the emitted Spec().Name reads like
// its siblings and the generated file name derives from it with no
// further transliteration.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func validateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf(
			"handler name %q must be snake_case (lowercase letters, digits, single underscores, starting with a letter — e.g. flag_idle_deals)",
			name)
	}
	return nil
}

// generate writes the handler skeleton and its test stub into dir,
// deriving both file names and the emitted struct/Spec().Name from
// name. It refuses if EITHER target already exists — write-once is
// checked for the whole pair before anything is written, so a
// collision on one half never leaves the other half touched.
func generate(dir, name string) (handlerPath, testPath string, err error) {
	if err := validateName(name); err != nil {
		return "", "", err
	}

	structName := camelCase(name)
	titleName := strings.ToUpper(structName[:1]) + structName[1:]

	handlerPath = filepath.Join(dir, "handlers_"+name+".go")
	testPath = filepath.Join(dir, "handlers_"+name+"_test.go")

	for _, p := range []string{handlerPath, testPath} {
		switch _, statErr := os.Stat(p); {
		case statErr == nil:
			return "", "", fmt.Errorf(
				"%s already exists — gen-workflow never overwrites a scaffold (write-once); remove it by hand first if you really mean to regenerate it",
				p)
		case !errors.Is(statErr, os.ErrNotExist):
			return "", "", fmt.Errorf("checking %s: %w", p, statErr)
		}
	}

	handlerSrc, err := format.Source([]byte(fmt.Sprintf(handlerTemplate, name, structName)))
	if err != nil {
		return "", "", fmt.Errorf("formatting the handler template: %w", err)
	}
	testSrc, err := format.Source([]byte(fmt.Sprintf(testTemplate, structName, titleName)))
	if err != nil {
		return "", "", fmt.Errorf("formatting the test template: %w", err)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.WriteFile(handlerPath, handlerSrc, 0o600); err != nil {
		return "", "", fmt.Errorf("writing %s: %w", handlerPath, err)
	}
	if err := os.WriteFile(testPath, testSrc, 0o600); err != nil {
		return "", "", fmt.Errorf("writing %s: %w", testPath, err)
	}
	return handlerPath, testPath, nil
}

// camelCase turns a snake_case handler name into the unexported struct
// name every starter handler uses (stage_change_create_task ->
// stageChangeCreateTask in handlers_event.go).
func camelCase(snake string) string {
	var b strings.Builder
	for i, part := range strings.Split(snake, "_") {
		if i == 0 {
			b.WriteString(part)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

// nextSteps is printed after a successful scaffold. It states the
// key==Spec().Name invariant explicitly: a catalog entry whose Key
// doesn't match the handler's declared name registers an orphan that
// HandleEvent never dispatches to — a silent no-op, not an error.
func nextSteps(name string) string {
	return fmt.Sprintf(`Next steps:
  1. Fill in Match's condition and Plan's effect in handlers_%[1]s.go.
  2. Add a Catalog() entry in automations_catalog.go whose Key equals
     %[1]q exactly — it must match this handler's Spec().Name
     character for character. A mismatched key registers an orphan:
     the catalog entry exists but no run ever dispatches to it, and
     nothing errors to tell you so.
  3. Register the handler in StarterWorkflows() (handlers_event.go);
     compose/workflows.go already ranges over that slice, so nothing
     else needs to change to wire it into the running engine.
`, name)
}

// handlerTemplate is the emitted handlers_<name>.go. It compiles as
// emitted: Match defaults to always-fire and Plan to a no-op Effect, and
// the trigger/tier are declared (never blank) so RegisterWorkflow's
// name/trigger assertions hold from the first commit — verb %[1]s is
// the handler name (Spec().Name and the idempotency key), %[2]s the
// struct name.
const handlerTemplate = `// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// %[2]s was scaffolded by gen-workflow. It compiles and registers as
// emitted, but Match always fires and Plan is a no-op — replace both,
// then follow the gen-workflow next-steps to wire it into the catalog
// and StarterWorkflows().

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

type %[2]s struct {
	ex Executors
}

func (%[2]s) Spec() workflow.Spec {
	return workflow.Spec{
		Name: %[1]q,
		Trigger: workflow.Trigger{
			// Set EventType (a bus event, e.g. "deal.stage_changed") XOR
			// Schedule — never both. Schedule is NOT a cron expression: it
			// is a clock:<name> marker string this engine never parses
			// (handlers_clock.go's noActivityScheduleMarker doc);
			// the real cadence comes from the River periodic job's own
			// interval. A clock handler also needs its own candidate
			// source wired at the time-scan (timescan.go's
			// activityScanHandlers, today's only wired source) — without
			// one it registers and compiles fine but is never evaluated
			// (see handlers_clock.go's renewalReminder for the
			// honestly-documented example of a handler still waiting on
			// one). This placeholder only keeps the trigger non-empty;
			// replace it before this handler goes anywhere near
			// StarterWorkflows().
			EventType: "replace_me.set_the_real_trigger_event_type",
		},
		// TierGreen auto-executes; TierYellow stages for approval before
		// Apply runs. Set the tier this handler's effect actually needs.
		Tier: mcp.TierGreen,
	}
}

func (%[2]s) Match(_ context.Context, _ workflow.Event) (bool, error) {
	// Replace with the real predicate. Returning true unconditionally is
	// only a valid scaffold default, never a valid shipped handler.
	return true, nil
}

func (%[2]s) Plan(_ context.Context, _ workflow.Event) (workflow.Effect, error) {
	// Replace with the real effect: build one or more workflow.Action
	// values (see ApplyActions in engine.go for the closed action set).
	return workflow.Effect{}, nil
}

func (h %[2]s) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, h.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (%[2]s) IdempotencyKey(ev workflow.Event) string {
	return %[1]q + ":" + ev.ID.String()
}
`

// testTemplate is the emitted handlers_<name>_test.go: the AC-W1
// "declares trigger + risk tier" property, checked against the actual
// zero-value handler so it fails the moment a hand edit blanks out the
// name or the trigger. %[1]s is the struct name, %[2]s its Title-cased
// form used in the test function name.
const testTemplate = `// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// TestGenerated%[2]sDeclaresTriggerAndTier is the AC-W1 scaffold
// property: RegisterWorkflow panics on an empty name or an empty
// trigger, so a handler that can register must declare both, plus a
// known risk tier. Replace or extend this once Match/Plan are real.
func TestGenerated%[2]sDeclaresTriggerAndTier(t *testing.T) {
	spec := %[1]s{}.Spec()

	if spec.Name == "" {
		t.Fatal("Spec().Name is empty — RegisterWorkflow refuses an unnamed handler")
	}
	if spec.Trigger.EventType == "" && spec.Trigger.Schedule == "" {
		t.Fatal("Spec().Trigger declares neither EventType nor Schedule — RegisterWorkflow refuses a triggerless handler")
	}
	switch spec.Tier {
	case mcp.TierGreen, mcp.TierYellow, mcp.TierDynamic:
	default:
		t.Fatalf("Spec().Tier %%v is not a known risk tier", spec.Tier)
	}
}
`
