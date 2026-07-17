// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The assign_lead_owner params schema and its hand-rolled validator
// (features/03 §3 AC-S5): the round-robin pool, the per-owner cap, and
// the ordered field-match rules. Split out of automations_catalog.go —
// which keeps the CatalogEntry list and the simple single-knob starter
// schemas — because the routing config is a self-contained concept with
// its own nested-object shape and its own multi-level validation, the
// one entry whose params are more than a single integer.

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The keys of one routing rule — the lead field a rule matches on, the
// literal it must equal, and the owner a match routes to. keyOwnerID is
// also the filter-field name preview ranges over (automations_preview.go),
// so it is the shared spelling of that one wire field, not two copies.
const (
	ruleKeyField  = "field"
	ruleKeyEquals = "equals"
	keyOwnerID    = "owner_id"
)

// RoutableLeadFields mirrors the closed field set the people module's
// routing engine matches rules on — lead-local columns only
// (segregation-in-scoring: routing never reads the contact graph).
var RoutableLeadFields = []string{"source", "company_name", "candidate_org_key"}

// leadRoutingSchema is the assign_lead_owner params shape (features/03 §3
// AC-S5): an ordered round-robin pool, an optional per-owner cap, and
// ordered field-match rules that outrank the rotation. This schema is
// the config source of truth for the editor; the people module's
// RoutingConfig decodes the identical shape.
func leadRoutingSchema() map[string]any {
	return map[string]any{
		schemaKeyType:            schemaTypeObject,
		schemaKeyAdditionalProps: false,
		schemaKeyProperties: map[string]any{
			"owners": map[string]any{
				schemaKeyType:        "array",
				"items":              map[string]any{schemaKeyType: schemaTypeString, "format": "uuid"},
				schemaKeyDescription: "Round-robin pool of user ids, in rotation order.",
			},
			"cap_per_owner": map[string]any{
				schemaKeyType:        "integer",
				"minimum":            1,
				schemaKeyDescription: "Max open (new/working) leads an owner may hold; omitted = uncapped.",
			},
			"rules": map[string]any{
				schemaKeyType:        "array",
				schemaKeyDescription: "Evaluated in order before round-robin; a matching lead goes to the rule's owner if under cap.",
				"items": map[string]any{
					schemaKeyType:            schemaTypeObject,
					schemaKeyAdditionalProps: false,
					"required":               []string{ruleKeyField, ruleKeyEquals, keyOwnerID},
					schemaKeyProperties: map[string]any{
						ruleKeyField:  map[string]any{"enum": RoutableLeadFields},
						ruleKeyEquals: map[string]any{schemaKeyType: schemaTypeString},
						keyOwnerID:    map[string]any{schemaKeyType: schemaTypeString, "format": "uuid"},
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
			if err := validateRoutingOwners(value); err != nil {
				return err
			}
		case "cap_per_owner":
			if err := validateRoutingCap(value); err != nil {
				return err
			}
		case "rules":
			if err := validateRoutingRules(value); err != nil {
				return err
			}
		default:
			return &ParamError{Field: "params." + key, Reason: errNotAParameter}
		}
	}
	return nil
}

func validateRoutingOwners(value any) error {
	list, ok := value.([]any)
	if !ok {
		return &ParamError{Field: "params.owners", Reason: "must be an array of user ids"}
	}
	for i, item := range list {
		if !isUUIDString(item) {
			return &ParamError{Field: fmt.Sprintf("params.owners[%d]", i), Reason: "must be a UUID"}
		}
	}
	return nil
}

func validateRoutingCap(value any) error {
	n, ok := value.(float64) // decoded JSON numbers arrive as float64
	if !ok || n != math.Trunc(n) {
		return &ParamError{Field: "params.cap_per_owner", Reason: "must be an integer"}
	}
	if n < 1 {
		return &ParamError{Field: "params.cap_per_owner", Reason: "must be at least 1"}
	}
	return nil
}

func validateRoutingRules(value any) error {
	list, ok := value.([]any)
	if !ok {
		return &ParamError{Field: "params.rules", Reason: "must be an array of rules"}
	}
	for i, item := range list {
		if err := validateRoutingRule(i, item); err != nil {
			return err
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
		case ruleKeyField:
			name, ok := value.(string)
			if !ok || !slices.Contains(RoutableLeadFields, name) {
				return &ParamError{Field: at(ruleKeyField), Reason: "must be one of " + strings.Join(RoutableLeadFields, ", ")}
			}
		case ruleKeyEquals:
			if _, ok := value.(string); !ok {
				return &ParamError{Field: at(ruleKeyEquals), Reason: "must be a string"}
			}
		case keyOwnerID:
			if !isUUIDString(value) {
				return &ParamError{Field: at(keyOwnerID), Reason: "must be a UUID"}
			}
		default:
			return &ParamError{Field: at(key), Reason: "not a rule property"}
		}
	}
	for _, required := range []string{ruleKeyField, ruleKeyEquals, keyOwnerID} {
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
