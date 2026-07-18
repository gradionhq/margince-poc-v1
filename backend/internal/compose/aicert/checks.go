// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

const (
	checkKindJSONSchema  = "json_schema"
	checkKindContains    = "contains"
	checkKindNotContains = "not_contains"
	checkKindMinFacts    = "min_facts"
)

// factsHolder is the shape every one of this codebase's extraction tasks
// emits: a JSON object with (at least) a top-level "facts" array —
// compose/sitecorpusread.go's structured-output contract. min_facts checks
// against that array's length rather than a bespoke corpus-only shape, so
// the check exercises the real contract, not a stand-in for it.
type factsHolder struct {
	Facts []json.RawMessage `json:"facts"`
}

// RunChecks evaluates every structural check in cs against output, one
// candidate's raw completion text, and reports every failure rather than
// stopping at the first — a scenario author fixing a check wants the full
// list, not one failure at a time. passed is true only when every check
// passed.
func RunChecks(cs []Check, output string) (passed bool, failures []string) {
	for _, c := range cs {
		if err := runCheck(c, output); err != nil {
			failures = append(failures, err.Error())
		}
	}
	return len(failures) == 0, failures
}

func runCheck(c Check, output string) error {
	switch c.Kind {
	case checkKindContains:
		if !strings.Contains(output, c.Arg) {
			return fmt.Errorf("contains %q: not found in output", c.Arg)
		}
		return nil
	case checkKindNotContains:
		if strings.Contains(output, c.Arg) {
			return fmt.Errorf("not_contains %q: found in output", c.Arg)
		}
		return nil
	case checkKindMinFacts:
		return checkMinFacts(c.Arg, output)
	case checkKindJSONSchema:
		if err := schema.ValidateJSON(c.Schema, output); err != nil {
			return fmt.Errorf("json_schema: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown check kind %q", c.Kind)
	}
}

func checkMinFacts(arg, output string) error {
	minCount, err := strconv.Atoi(arg)
	if err != nil {
		return fmt.Errorf("min_facts: arg %q is not an integer: %w", arg, err)
	}
	var holder factsHolder
	if err := json.Unmarshal([]byte(output), &holder); err != nil {
		return fmt.Errorf("min_facts: output is not a JSON object with a facts array: %w", err)
	}
	if len(holder.Facts) < minCount {
		return fmt.Errorf("min_facts: got %d facts, want >= %d", len(holder.Facts), minCount)
	}
	return nil
}
