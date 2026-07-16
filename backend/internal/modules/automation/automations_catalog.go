// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

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
	// Seeded marks the entry as one of the SIX starter templates
	// SeedStarterAutomationsTx (automations.go) enrolls into a fresh
	// workspace, enabled, on bootstrap (UAT.md:72 — "exactly the six
	// seeded templates"). An entry with Seeded false is still fully
	// instantiable through the API (the catalog stays the closed,
	// authorable set) — it just never lands in a workspace unasked.
	Seeded bool
}

// singleIntParamSchema is the one-knob schema every "how many days"
// starter shares — due_in_days, no_activity_days, check_in_days, and
// days_before all take the identical bounded-integer shape and differ
// only in which key names the knob and what its own default/bounds are.
func singleIntParamSchema(key string, defaultValue, minValue, maxValue int, description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			key: map[string]any{
				"type":        "integer",
				"minimum":     minValue,
				"maximum":     maxValue,
				"default":     defaultValue,
				"description": description,
			},
		},
	}
}

// validateSingleIntParam is singleIntParamSchema's hand-rolled
// counterpart — the closed catalog owns its own tiny schemas, which
// does not buy a jsonschema dependency: an unknown knob or a mistyped
// or out-of-range value is a 422, never a stored blob the engine
// chokes on later.
func validateSingleIntParam(key string, minValue, maxValue int) func(params map[string]any) error {
	return func(params map[string]any) error {
		for k, value := range params {
			if k != key {
				return &ParamError{Field: "params." + k, Reason: "not a parameter of this automation type"}
			}
			n, ok := value.(float64) // decoded JSON numbers arrive as float64
			if !ok || n != math.Trunc(n) {
				return &ParamError{Field: "params." + key, Reason: "must be an integer"}
			}
			if n < float64(minValue) || n > float64(maxValue) {
				return &ParamError{Field: "params." + key,
					Reason: fmt.Sprintf("must be between %d and %d", minValue, maxValue)}
			}
		}
		return nil
	}
}

// dueInDaysSchema is the shared shape of the create_task starters that
// key their one knob "due_in_days": how many days out the created task
// is due.
func dueInDaysSchema(defaultDays int, description string) map[string]any {
	return singleIntParamSchema("due_in_days", defaultDays, 1, 30, description)
}

// validateDueInDays is dueInDaysSchema's validator.
var validateDueInDays = validateSingleIntParam("due_in_days", 1, 30)

// noActivityReminderSchema is no_activity_reminder's one-knob shape,
// keyed "no_activity_days" — the exact property name
// workflows_clock_handlers.go's noActivityDays reads off an instance's
// params, so the editor's schema and the handler's reader can never
// drift onto two different knob names for the same automation.
func noActivityReminderSchema() map[string]any {
	return singleIntParamSchema("no_activity_days", defaultNoActivityDays, 1, 365,
		"How many quiet days (no genuine activity) before the reminder fires.")
}

// validateNoActivityReminderParams is noActivityReminderSchema's validator.
var validateNoActivityReminderParams = validateSingleIntParam("no_activity_days", 1, 365)

// checkInCadenceSchema is check_in_cadence's own one-knob shape, keyed
// "check_in_days" — checkInCadenceDays' (workflows_clock_handlers.go)
// own reader, distinct from no_activity_reminder's key so a workspace
// may enable both with independent cadences.
func checkInCadenceSchema() map[string]any {
	return singleIntParamSchema("check_in_days", defaultCheckInDays, 1, 365,
		"How many quiet days (no genuine activity) before the check-in reminder fires.")
}

// validateCheckInCadenceParams is checkInCadenceSchema's validator.
var validateCheckInCadenceParams = validateSingleIntParam("check_in_days", 1, 365)

// renewalReminderSchema is renewal_reminder's one-knob shape, keyed
// "days_before" — renewalDaysBefore's (workflows_clock_handlers.go) own
// reader.
func renewalReminderSchema() map[string]any {
	return singleIntParamSchema("days_before", defaultRenewalDaysBefore, 1, 365,
		"How many days ahead of the renewal date to remind.")
}

// validateRenewalReminderParams is renewalReminderSchema's validator.
var validateRenewalReminderParams = validateSingleIntParam("days_before", 1, 365)

// noParamsSchema is the empty-knob schema for a starter with nothing to
// parameterize (stage_change_notify, post_meeting_recap): both fire
// unconditionally off their trigger alone, so the only valid params
// blob is the empty one.
func noParamsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
	}
}

// validateNoParams rejects any key at all — the starter reads no params.
func validateNoParams(params map[string]any) error {
	for key := range params {
		return &ParamError{Field: "params." + key, Reason: "not a parameter of this automation type"}
	}
	return nil
}

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
			return &ParamError{Field: "params." + key, Reason: "not a parameter of this automation type"}
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

// assignLeadOwnerName mirrors people.assignLeadOwnerName: the catalog
// key MUST equal the backing handler's Spec().Name, but a module never
// imports a sibling (ADR-0054 §9) — so this is its own copy of the
// literal, not a shared symbol. Kept in lockstep by
// internal/compose/leadrouting_config_test.go, which resolves this
// exact key against the real people.LeadRoutingWorkflow handler.
const assignLeadOwnerName = "assign_lead_owner"

// Catalog returns the closed automation library — the full authorable
// set: the six Seeded starter templates (seededCatalogEntries) plus the
// authorable-only entries (authorableOnlyCatalogEntries) that never
// enroll into a fresh workspace unasked. Split into two builders so
// each stays a short, single-purpose list rather than one long literal.
func Catalog() []CatalogEntry {
	return append(seededCatalogEntries(), authorableOnlyCatalogEntries()...)
}

// seededCatalogEntries is UAT.md:72's pinned six — the exact set
// SeedStarterAutomationsTx (automations.go) enrolls, ENABLED, into
// every fresh workspace. Each Key names the workflow.Handler
// automation's own StarterWorkflows registers under it.
func seededCatalogEntries() []CatalogEntry {
	return []CatalogEntry{
		{
			Key:          noActivityReminderName,
			Name:         "No-activity reminder",
			Description:  "Reminds an entity's owner once its most recent captured activity has gone quiet for N days.",
			Trigger:      noActivityScheduleMarker,
			Action:       string(ActionTypeCreateTask),
			Tier:         tierGreen,
			ParamsSchema: noActivityReminderSchema(),
			Validate:     validateNoActivityReminderParams,
			Seeded:       true,
		},
		{
			Key:          renewalReminderName,
			Name:         "Renewal reminder",
			Description:  "Reminds an entity's owner as its configured renewal date approaches.",
			Trigger:      renewalScheduleMarker,
			Action:       string(ActionTypeCreateTask),
			Tier:         tierGreen,
			ParamsSchema: renewalReminderSchema(),
			Validate:     validateRenewalReminderParams,
			Seeded:       true,
		},
		{
			Key:          stageChangeNotifyName,
			Name:         "Stage-change notify",
			Description:  "Notifies the deal's owner on every stage move, including the closes that end the follow-up cadence.",
			Trigger:      eventDealStageChanged,
			Action:       string(ActionTypeNotify),
			Tier:         tierGreen,
			ParamsSchema: noParamsSchema(),
			Validate:     validateNoParams,
			Seeded:       true,
		},
		{
			Key:          routeLeadName,
			Name:         "Route new lead to a task",
			Description:  "Mints a follow-up task the moment a new lead is captured, so every lead gets a first next step.",
			Trigger:      eventLeadCreated,
			Action:       string(ActionTypeCreateTask),
			Tier:         tierGreen,
			ParamsSchema: dueInDaysSchema(defaultRouteLeadDueInDays, "How many days out the follow-up task on the new lead is due."),
			Validate:     validateDueInDays,
			Seeded:       true,
		},
		{
			Key:          checkInCadenceName,
			Name:         "Check-in cadence",
			Description:  "Reminds an entity's owner to re-engage once it has gone quiet for the automation's own (typically longer) cadence.",
			Trigger:      checkInCadenceScheduleMarker,
			Action:       string(ActionTypeCreateTask),
			Tier:         tierGreen,
			ParamsSchema: checkInCadenceSchema(),
			Validate:     validateCheckInCadenceParams,
			Seeded:       true,
		},
		{
			Key:          postMeetingRecapName,
			Name:         "Post-meeting recap draft",
			Description:  "Drafts a follow-up recap whenever a meeting is logged.",
			Trigger:      eventActivityCaptured,
			Action:       string(ActionTypeDraftEmail),
			Tier:         tierGreen,
			ParamsSchema: noParamsSchema(),
			Validate:     validateNoParams,
			Seeded:       true,
		},
	}
}

// authorableOnlyCatalogEntries are real, instantiable catalog types that
// SeedStarterAutomationsTx never enrolls (Seeded false) — reachable
// through the API, never through the bootstrap floor.
func authorableOnlyCatalogEntries() []CatalogEntry {
	return []CatalogEntry{
		{
			// AUTHORABLE, not seeded: the honest name for the owner-
			// assignment behaviour the OLD route_lead key carried before
			// AUTO-NOTE-2's reconciliation (migrations/core/0075 re-keys
			// any live row). Tier is green, not actionDefs' "dynamic" label
			// for assign_owner (catalog_actions.go) — the automation.tier
			// column's own CHECK constraint (migrations/core/0035) and the
			// wire contract's tier enum (crm.yaml) both close tier to
			// green|yellow, and green is the honest default: no shipped
			// automation's own params carry a scale signal yet
			// (assign_owner_tier.go's own doc), so every real firing today
			// resolves single-entity — resolveAssignOwnerTier's own
			// zero-value case.
			Key:          assignLeadOwnerName,
			Name:         "Assign new leads an owner",
			Description:  "Assigns every new lead an owner: matching rules first, else round-robin across the pool, never exceeding capacity caps.",
			Trigger:      eventLeadCreated,
			Action:       string(ActionTypeAssignOwner),
			Tier:         tierGreen,
			ParamsSchema: leadRoutingSchema(),
			Validate:     validateLeadRoutingParams,
			Seeded:       false,
		},
		{
			// AUTHORABLE, not seeded: a create_task-on-stage-change template
			// that predates the six-template pin (UAT.md:72). The six's own
			// stage-change template is stage_change_notify (above); this one
			// stays registered and instantiable rather than removed — an
			// extra available template, never a seeded seventh.
			Key:          stageChangeCreateTaskName,
			Name:         "Follow up on stage changes",
			Description:  "Mints a follow-up task on the deal timeline after every open stage move.",
			Trigger:      eventDealStageChanged,
			Action:       string(ActionTypeCreateTask),
			Tier:         tierGreen,
			ParamsSchema: dueInDaysSchema(2, "How many days out the follow-up task is due."),
			Validate:     validateDueInDays,
			Seeded:       false,
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
