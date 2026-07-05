// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The closed automation catalog (B-E15.1, ADR-0035): the automation
// *types* a workspace may instantiate. Adding a member is a code
// change, never data — the anti-builder guard. The catalog, not the
// request, is the source of an instance's trigger/action/tier; params
// are validated here against each entry's schema, so a jsonb blob the
// engine would choke on never reaches the table.

import (
	"encoding/json"
	"fmt"
	"math"
)

// CatalogEntry describes one instantiable automation type. Key equals
// the backing workflow.Handler's Spec().Name — one vocabulary across
// the catalog, the engine, and the run records.
type CatalogEntry struct {
	Key          string
	Name         string
	Description  string
	Trigger      string // event type that fires it
	Action       string // closed workflow.ActionKind it plans
	Tier         string // green | yellow (from the handler, never the caller)
	ParamsSchema map[string]any
	Validate     func(params map[string]any) error
}

// dueInDaysSchema is the shared one-knob schema of both starters: how
// many days out the created task is due.
func dueInDaysSchema(defaultDays int, description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"due_in_days": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     30,
				"default":     defaultDays,
				"description": description,
			},
		},
	}
}

// validateDueInDays is the hand-rolled counterpart of dueInDaysSchema —
// the closed catalog owns two tiny schemas, which does not buy a
// jsonschema dependency.
func validateDueInDays(params map[string]any) error {
	for key, value := range params {
		if key != "due_in_days" {
			return &ParamError{Field: "params." + key, Reason: "not a parameter of this automation type"}
		}
		n, ok := value.(float64) // decoded JSON numbers arrive as float64
		if !ok || n != math.Trunc(n) {
			return &ParamError{Field: "params.due_in_days", Reason: "must be an integer"}
		}
		if n < 1 || n > 30 {
			return &ParamError{Field: "params.due_in_days", Reason: "must be between 1 and 30 days"}
		}
	}
	return nil
}

// ParamError maps to 422 at the transport.
type ParamError struct {
	Field  string
	Reason string
}

func (e *ParamError) Error() string { return e.Field + ": " + e.Reason }

// Catalog returns the closed starter library, aligned 1:1 with
// StarterWorkflows: route_lead and stage_change_create_task.
func Catalog() []CatalogEntry {
	return []CatalogEntry{
		{
			Key:          "route_lead",
			Name:         "Route new leads",
			Description:  "Answers every new lead with a triage task so no lead sits unseen.",
			Trigger:      "lead.created",
			Action:       "create_task",
			Tier:         "green",
			ParamsSchema: dueInDaysSchema(1, "How many days the triage task gets before it is due."),
			Validate:     validateDueInDays,
		},
		{
			Key:          "stage_change_create_task",
			Name:         "Follow up on stage changes",
			Description:  "Mints a follow-up task on the deal timeline after every open stage move.",
			Trigger:      "deal.stage_changed",
			Action:       "create_task",
			Tier:         "green",
			ParamsSchema: dueInDaysSchema(2, "How many days out the follow-up task is due."),
			Validate:     validateDueInDays,
		},
	}
}

// CatalogEntryByKey resolves one type; ok=false for anything outside
// the closed set.
func CatalogEntryByKey(key string) (CatalogEntry, bool) {
	for _, entry := range Catalog() {
		if entry.Key == key {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

// DueInDays reads the shared parameter with its per-type default —
// the one place the starters interpret their params blob.
func DueInDays(params json.RawMessage, defaultDays int) (int, error) {
	if len(params) == 0 {
		return defaultDays, nil
	}
	var decoded struct {
		DueInDays *int `json:"due_in_days"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return 0, fmt.Errorf("crmagents: automation params: %w", err)
	}
	if decoded.DueInDays == nil {
		return defaultDays, nil
	}
	return *decoded.DueInDays, nil
}
