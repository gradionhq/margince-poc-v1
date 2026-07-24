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
	Tier         string // auto_execute | confirmation_required (from the handler, never the caller)
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

// The JSON-schema vocabulary the closed catalog's hand-built schemas
// speak — one spelling of each keyword so the schema helpers read
// uniformly and a mistyped one is a build error, not a schema the
// editor form silently cannot render.
const (
	schemaTypeObject         = "object"
	schemaTypeString         = "string"
	schemaKeyType            = "type"
	schemaKeyProperties      = "properties"
	schemaKeyAdditionalProps = "additionalProperties"
	schemaKeyDescription     = "description"
)

// minParamDays is the lower bound every "how many days" knob shares: a
// cadence of zero days is meaningless, so one is the floor for all of them.
const minParamDays = 1

// errNotAParameter is the 422 reason every closed-catalog validator
// returns for a params key outside its own schema. The routing rule keys
// (ruleKeyField/ruleKeyEquals/keyOwnerID) live beside their own schema in
// catalog_leadrouting_schema.go.
const errNotAParameter = "not a parameter of this automation type"

// singleIntParamSchema is the one-knob schema every "how many days"
// starter shares — due_in_days, no_activity_days, check_in_days, and
// days_before all take the identical bounded-integer shape and differ
// only in which key names the knob and what its own default/bounds are.
func singleIntParamSchema(key string, defaultValue, maxValue int, description string) map[string]any {
	return map[string]any{
		schemaKeyType:            schemaTypeObject,
		schemaKeyAdditionalProps: false,
		schemaKeyProperties: map[string]any{
			key: map[string]any{
				schemaKeyType:        "integer",
				"minimum":            minParamDays,
				"maximum":            maxValue,
				"default":            defaultValue,
				schemaKeyDescription: description,
			},
		},
	}
}

// validateSingleIntParam is singleIntParamSchema's hand-rolled
// counterpart — the closed catalog owns its own tiny schemas, which
// does not buy a jsonschema dependency: an unknown knob or a mistyped
// or out-of-range value is a 422, never a stored blob the engine
// chokes on later.
func validateSingleIntParam(key string, maxValue int) func(params map[string]any) error {
	return func(params map[string]any) error {
		for k, value := range params {
			if k != key {
				return &ParamError{Field: "params." + k, Reason: errNotAParameter}
			}
			n, ok := value.(float64) // decoded JSON numbers arrive as float64
			if !ok || n != math.Trunc(n) {
				return &ParamError{Field: "params." + key, Reason: "must be an integer"}
			}
			if n < float64(minParamDays) || n > float64(maxValue) {
				return &ParamError{
					Field:  "params." + key,
					Reason: fmt.Sprintf("must be between %d and %d", minParamDays, maxValue),
				}
			}
		}
		return nil
	}
}

// dueInDaysSchema is the shared shape of the create_task starters that
// key their one knob "due_in_days": how many days out the created task
// is due.
func dueInDaysSchema(defaultDays int, description string) map[string]any {
	return singleIntParamSchema("due_in_days", defaultDays, 30, description)
}

// validateDueInDays is dueInDaysSchema's validator.
var validateDueInDays = validateSingleIntParam("due_in_days", 30)

// noActivityReminderSchema is no_activity_reminder's one-knob shape,
// keyed "no_activity_days" — the exact property name
// handlers_clock.go's noActivityDays reads off an instance's
// params, so the editor's schema and the handler's reader can never
// drift onto two different knob names for the same automation.
func noActivityReminderSchema() map[string]any {
	return singleIntParamSchema("no_activity_days", defaultNoActivityDays, 365,
		"How many quiet days (no genuine activity) before the reminder fires.")
}

// validateNoActivityReminderParams is noActivityReminderSchema's validator.
var validateNoActivityReminderParams = validateSingleIntParam("no_activity_days", 365)

// checkInCadenceSchema is check_in_cadence's own one-knob shape, keyed
// "check_in_days" — checkInCadenceDays' (handlers_clock.go)
// own reader, distinct from no_activity_reminder's key so a workspace
// may enable both with independent cadences.
func checkInCadenceSchema() map[string]any {
	return singleIntParamSchema("check_in_days", defaultCheckInDays, 365,
		"How many quiet days (no genuine activity) before the check-in reminder fires.")
}

// validateCheckInCadenceParams is checkInCadenceSchema's validator.
var validateCheckInCadenceParams = validateSingleIntParam("check_in_days", 365)

// renewalReminderSchema is renewal_reminder's one-knob shape, keyed
// "days_before" — renewalDaysBefore's (handlers_clock.go) own
// reader.
func renewalReminderSchema() map[string]any {
	return singleIntParamSchema("days_before", defaultRenewalDaysBefore, 365,
		"How many days ahead of the renewal date to remind.")
}

// validateRenewalReminderParams is renewalReminderSchema's validator.
var validateRenewalReminderParams = validateSingleIntParam("days_before", 365)

// noParamsSchema is the empty-knob schema for a starter with nothing to
// parameterize (stage_change_notify, post_meeting_recap): both fire
// unconditionally off their trigger alone, so the only valid params
// blob is the empty one.
func noParamsSchema() map[string]any {
	return map[string]any{
		schemaKeyType:            schemaTypeObject,
		schemaKeyAdditionalProps: false,
		schemaKeyProperties:      map[string]any{},
	}
}

// validateNoParams rejects any key at all — the starter reads no params.
func validateNoParams(params map[string]any) error {
	for key := range params {
		return &ParamError{Field: "params." + key, Reason: errNotAParameter}
	}
	return nil
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
			Tier:         tierAutoExecute,
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
			Tier:         tierAutoExecute,
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
			Tier:         tierAutoExecute,
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
			Tier:         tierAutoExecute,
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
			Tier:         tierAutoExecute,
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
			Tier:         tierAutoExecute,
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
			// AUTO-NOTE-2's reconciliation (migrations/core/0078 re-keys
			// any live row). Tier is auto_execute, not actionDefs' "dynamic" label
			// for assign_owner (catalog_actions.go) — the automation.tier
			// column's own CHECK constraint (migrations/core/0035) and the
			// wire contract's tier enum (crm.yaml) both close tier to
			// auto_execute|confirmation_required, and auto_execute is the honest default: no shipped
			// automation's own params carry a scale signal yet
			// (assign_owner_tier.go's own doc), so every real firing today
			// resolves single-entity — resolveAssignOwnerTier's own
			// zero-value case.
			Key:          assignLeadOwnerName,
			Name:         "Assign new leads an owner",
			Description:  "Assigns every new lead an owner: matching rules first, else round-robin across the pool, never exceeding capacity caps.",
			Trigger:      eventLeadCreated,
			Action:       string(ActionTypeAssignOwner),
			Tier:         tierAutoExecute,
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
			Tier:         tierAutoExecute,
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
