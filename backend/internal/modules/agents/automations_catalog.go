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
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// dueInDaysSchema is the one-knob schema of the task starters: how
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

// RoutableLeadFields mirrors the closed field set the people module's
// routing engine matches rules on — lead-local columns only
// (segregation-in-scoring: routing never reads the contact graph).
var RoutableLeadFields = []string{"source", "company_name", "candidate_org_key"}

// leadRoutingSchema is the route_lead params shape (features/03 §3
// AC-S5): an ordered round-robin pool, an optional per-owner cap, and
// ordered field-match rules that outrank the rotation. This schema is
// the config source of truth for the editor; the people module's
// RoutingConfig decodes the identical shape.
func leadRoutingSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"owners": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "format": "uuid"},
				"description": "Round-robin pool of user ids, in rotation order.",
			},
			"cap_per_owner": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Max open (new/working) leads an owner may hold; omitted = uncapped.",
			},
			"rules": map[string]any{
				"type":        "array",
				"description": "Evaluated in order before round-robin; a matching lead goes to the rule's owner if under cap.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"field", "equals", "owner_id"},
					"properties": map[string]any{
						"field":    map[string]any{"enum": RoutableLeadFields},
						"equals":   map[string]any{"type": "string"},
						"owner_id": map[string]any{"type": "string", "format": "uuid"},
					},
				},
			},
		},
	}
}

// validateLeadRoutingParams is the hand-rolled counterpart of
// leadRoutingSchema — same anti-DSL posture as validateDueInDays: an
// unknown knob or a mistyped value is a 422, never a stored blob the
// engine chokes on later.
func validateLeadRoutingParams(params map[string]any) error {
	for key, value := range params {
		switch key {
		case "owners":
			list, ok := value.([]any)
			if !ok {
				return &ParamError{Field: "params.owners", Reason: "must be an array of user ids"}
			}
			for i, item := range list {
				if !isUUIDString(item) {
					return &ParamError{Field: fmt.Sprintf("params.owners[%d]", i), Reason: "must be a UUID"}
				}
			}
		case "cap_per_owner":
			n, ok := value.(float64) // decoded JSON numbers arrive as float64
			if !ok || n != math.Trunc(n) {
				return &ParamError{Field: "params.cap_per_owner", Reason: "must be an integer"}
			}
			if n < 1 {
				return &ParamError{Field: "params.cap_per_owner", Reason: "must be at least 1"}
			}
		case "rules":
			list, ok := value.([]any)
			if !ok {
				return &ParamError{Field: "params.rules", Reason: "must be an array of rules"}
			}
			for i, item := range list {
				if err := validateRoutingRule(i, item); err != nil {
					return err
				}
			}
		default:
			return &ParamError{Field: "params." + key, Reason: "not a parameter of this automation type"}
		}
	}
	return nil
}

func validateRoutingRule(i int, item any) error {
	at := func(field string) string { return fmt.Sprintf("params.rules[%d].%s", i, field) }
	rule, ok := item.(map[string]any)
	if !ok {
		return &ParamError{Field: fmt.Sprintf("params.rules[%d]", i), Reason: "must be an object"}
	}
	for key, value := range rule {
		switch key {
		case "field":
			name, ok := value.(string)
			if !ok || !slices.Contains(RoutableLeadFields, name) {
				return &ParamError{Field: at("field"), Reason: "must be one of " + strings.Join(RoutableLeadFields, ", ")}
			}
		case "equals":
			if _, ok := value.(string); !ok {
				return &ParamError{Field: at("equals"), Reason: "must be a string"}
			}
		case "owner_id":
			if !isUUIDString(value) {
				return &ParamError{Field: at("owner_id"), Reason: "must be a UUID"}
			}
		default:
			return &ParamError{Field: at(key), Reason: "not a rule property"}
		}
	}
	for _, required := range []string{"field", "equals", "owner_id"} {
		if _, present := rule[required]; !present {
			return &ParamError{Field: at(required), Reason: "is required"}
		}
	}
	return nil
}

func isUUIDString(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	_, err := ids.Parse(s)
	return err == nil
}

// ParamError maps to 422 at the transport.
type ParamError struct {
	Field  string
	Reason string
}

func (e *ParamError) Error() string { return e.Field + ": " + e.Reason }

// Catalog returns the closed starter library. Each key names the
// workflow.Handler compose registers under it: stage_change_create_task
// ships in this module's StarterWorkflows; route_lead's engine lives in
// the people module (the transactional assignment is lead-store SQL)
// and compose wires it to this entry.
func Catalog() []CatalogEntry {
	return []CatalogEntry{
		{
			Key:          "route_lead",
			Name:         "Route new leads",
			Description:  "Assigns every new lead an owner: matching rules first, else round-robin across the pool, never exceeding capacity caps.",
			Trigger:      "lead.created",
			Action:       "assign_owner",
			Tier:         "green",
			ParamsSchema: leadRoutingSchema(),
			Validate:     validateLeadRoutingParams,
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
