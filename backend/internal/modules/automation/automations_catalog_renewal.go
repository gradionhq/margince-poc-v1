// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// renewal_reminder's own slice of the catalog surface
// (automations_catalog.go): its schema and validator, split into their
// own file purely to keep automations_catalog.go under the 500-line
// product ceiling (go-file-length) — the identical size-not-concept
// reason automations_preview_renewal.go was already split out for the
// preview side of the same catalog key.

import (
	"fmt"
	"math"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// renewalReminderObjects is the closed set of record types renewal_reminder
// may watch — the automation-side twin of customfields.FieldObjects
// (customfields/engine.go), spelled from the SAME datasource.EntityType
// constants so the two lists can never drift in spelling even though this
// module does not import customfields directly (ADR-0054 §9, no sibling
// imports). Hand-duplicated here for the identical reason
// catalog_triggers.go's own event-type constants are: "kept as constants
// here because more than one automation surface... references the same
// string" — this validator is that surface for the object vocabulary.
var renewalReminderObjects = []string{
	string(datasource.EntityPerson),
	string(datasource.EntityOrganization),
	string(datasource.EntityDeal),
	string(datasource.EntityLead),
	string(datasource.EntityProject),
}

// renewalReminderObjectSet is renewalReminderObjects as a lookup set —
// the same precompute-once-at-package-init idiom customfields/engine.go's
// allowedObjects already uses for the identical shape of problem.
var renewalReminderObjectSet = func() map[string]bool {
	m := make(map[string]bool, len(renewalReminderObjects))
	for _, o := range renewalReminderObjects {
		m[o] = true
	}
	return m
}()

// paramKeyDateField/paramFieldObject name the two strings goconst flags
// once automations_preview_renewal.go's dynamic previewDef (Task 3)
// starts referencing them too — one spelling shared across both files
// rather than a literal repeated at every call site.
const (
	paramKeyDateField = "date_field"
	paramFieldObject  = "params.object"
)

// renewalReminderSchema is renewal_reminder's shape: days_before
// (renewalDaysBefore's own reader, handlers_clock.go) plus the three keys
// #706 adds — object and date_field name the workspace's own cf_* date
// column to watch, recurs_yearly opts a stored value into yearly
// re-arming (a birthday, not a one-time deadline) rather than firing once.
func renewalReminderSchema() map[string]any {
	return map[string]any{
		schemaKeyType:            schemaTypeObject,
		schemaKeyAdditionalProps: false,
		schemaKeyProperties: map[string]any{
			"days_before": intParamProperty(defaultRenewalDaysBefore, 365,
				"How many days ahead of the renewal date to remind."),
			"object": map[string]any{
				schemaKeyType:        schemaTypeString,
				schemaKeyDescription: "Which record type owns the watched date field: one of " + strings.Join(renewalReminderObjects, ", ") + ".",
			},
			paramKeyDateField: map[string]any{
				schemaKeyType:        schemaTypeString,
				schemaKeyDescription: "The workspace's own custom date-field name to watch.",
			},
			"recurs_yearly": map[string]any{
				schemaKeyType:        "boolean",
				"default":            false,
				schemaKeyDescription: "Whether the watched date recurs every year (e.g. a birthday) rather than firing once.",
			},
		},
	}
}

// validateRenewalDaysBeforeParam checks the one existing knob's bounds —
// split out of validateRenewalReminderParams so the three new keys #706
// adds do not push that function over the cyclomatic-complexity ceiling.
func validateRenewalDaysBeforeParam(params map[string]any) error {
	v, ok := params["days_before"]
	if !ok {
		return nil
	}
	n, ok := v.(float64) // decoded JSON numbers arrive as float64
	if !ok || n != math.Trunc(n) {
		return &ParamError{Field: "params.days_before", Reason: reasonMustBeInteger}
	}
	if n < float64(minParamDays) || n > 365 {
		return &ParamError{Field: "params.days_before", Reason: fmt.Sprintf("must be between %d and %d", minParamDays, 365)}
	}
	return nil
}

// validateRenewalObjectParam checks object against the closed
// renewalReminderObjectSet — split out for the same reason
// validateRenewalDaysBeforeParam is.
func validateRenewalObjectParam(params map[string]any) error {
	v, ok := params["object"]
	if !ok {
		return nil
	}
	s, isString := v.(string)
	if !isString || !renewalReminderObjectSet[s] {
		return &ParamError{Field: paramFieldObject, Reason: "must be one of " + strings.Join(renewalReminderObjects, ", ")}
	}
	return nil
}

// validateRenewalDateFieldParam checks date_field for non-emptiness only —
// whether it actually names a REAL, date-typed column on the chosen
// object is a save-time check against the workspace's own
// customfields.Service.ActiveColumns, and this function has no DB handle
// to run it with (Validate's signature takes only the decoded params
// map). AutomationStore.Create/Update (automations.go) is where such a
// check would run if a later ticket grows one; today an instance can be
// saved naming a column the workspace hasn't created yet, and
// TimeScanner's own honest no-op for a misconfigured instance
// (scanDateFieldInstanceCandidates's doc, timescan.go) is the same
// posture: never a fabricated read, just no candidates until the field
// exists.
func validateRenewalDateFieldParam(params map[string]any) error {
	v, ok := params[paramKeyDateField]
	if !ok {
		return nil
	}
	s, isString := v.(string)
	if !isString || s == "" {
		return &ParamError{Field: "params.date_field", Reason: "must not be empty"}
	}
	return nil
}

// validateRenewalRecursYearlyParam checks recurs_yearly is a real
// boolean when present — split out for the same reason
// validateRenewalDaysBeforeParam is.
func validateRenewalRecursYearlyParam(params map[string]any) error {
	v, ok := params["recurs_yearly"]
	if !ok {
		return nil
	}
	if _, isBool := v.(bool); !isBool {
		return &ParamError{Field: "params.recurs_yearly", Reason: "must be a boolean"}
	}
	return nil
}

// validateRenewalReminderParams is renewalReminderSchema's validator:
// refuse any key outside the closed set, then run each key's own
// validator (each a no-op when its key is absent, since a stored
// instance may not have all four set — TimeScanner's honest no-op for
// that case, timescan.go).
func validateRenewalReminderParams(params map[string]any) error {
	for k := range params {
		switch k {
		case "days_before", "object", paramKeyDateField, "recurs_yearly":
		default:
			return &ParamError{Field: "params." + k, Reason: errNotAParameter}
		}
	}
	for _, validate := range []func(map[string]any) error{
		validateRenewalDaysBeforeParam,
		validateRenewalObjectParam,
		validateRenewalDateFieldParam,
		validateRenewalRecursYearlyParam,
	} {
		if err := validate(params); err != nil {
			return err
		}
	}
	return nil
}
