// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// This file is the write direction of the HubSpot mapping-as-contract
// (OVA-MAP-W): the canonical→incumbent projection the write-back engine
// PATCHes/POSTs to HubSpot. It is pinned SEPARATELY from the read mapping
// (mapping_hs.go) rather than inferred from it, because the read projection
// (OVA-MAP-1..8) is not a bijection — several rules are derived or lossy
// one-way (assembled full_name, currency-scaled amounts, class-derived
// activity kind, association-derived lead fields, lossy size_band). A
// canonical field with no writable incumbent counterpart is READ-ONLY: it is
// never written back, and a write carrying only read-only fields is a no-op
// the caller is told about (empty Props), never a fabricated incumbent
// property.

// writeMapping is one canonical→HubSpot write projection: the incumbent
// object class the write targets and the HubSpot properties to set. Props
// carries only WRITABLE properties — a canonical field flagged read-only by
// OVA-MAP-W (full_name, occurred_at, lead email/company_name/status, deal
// pipeline_id/stage_id, org size_band, activity meeting_status) never
// appears. An empty Props on a CREATE means the write touched only read-only
// fields (the caller is told); on an UPDATE it means the patch changed
// nothing writable.
type writeMapping struct {
	ObjectClass string
	Props       map[string]string
}

// mapWrite projects a canonical write (the entity type + the canonical field
// bag, the JSON-decoded contract the frozen datasource seam carries) onto the
// HubSpot object class and property set per OVA-MAP-W1..6. forUpdate selects
// clear-semantics: on an update an explicit "" clears the incumbent property
// (HubSpot's documented clear), while on a create an empty value is simply
// nothing to set. It is pure: no I/O, no transport — the golden write-mapping
// suite (AC-OV-12) exercises it directly.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag from the frozen datasource seam; the any is inherent to the decoded shape
func mapWrite(canonicalClass string, fields map[string]any, forUpdate bool) (writeMapping, error) {
	switch canonicalClass {
	case personTarget:
		props, err := copyDirect(fields, personWriteFields, forUpdate)
		return writeMapping{ObjectClass: objectClassContacts, Props: props}, err
	case organizationTarget:
		props, err := copyDirect(fields, organizationWriteFields, forUpdate)
		return writeMapping{ObjectClass: objectClassCompanies, Props: props}, err
	case leadTarget:
		props, err := copyDirect(fields, leadWriteFields, forUpdate)
		return writeMapping{ObjectClass: objectClassLeads, Props: props}, err
	case dealTarget:
		return mapWriteDeal(fields, forUpdate)
	case activityTarget:
		return mapWriteActivity(fields, forUpdate)
	default:
		return writeMapping{}, fmt.Errorf("overlay: no HubSpot write mapping for canonical class %q", canonicalClass)
	}
}

// directWriteField is one canonical field that projects 1:1 onto a HubSpot
// property by a straight string copy. The read-only canonical fields simply
// have no entry — that absence is what makes them never written.
type directWriteField struct {
	Canonical string
	HSProp    string
}

// personWriteFields — OVA-MAP-W1. full_name is the assembled display field
// (OVA-MAP-3) and is NOT here: splitting a display string into first/last is
// ambiguous and lossy, so it is read-only. emails, address, and owner_id are
// V1 write-back deferrals (the emails child, the structured-address
// assembler, and the reverse owner user-map resolution have no simple 1:1
// inverse yet) — read-only for now, surfaced honestly rather than guessed,
// the same "flag, don't invent" posture the read mapping takes for phone/social.
var personWriteFields = []directWriteField{
	{Canonical: "first_name", HSProp: propFirstname},
	{Canonical: "last_name", HSProp: propLastname},
	{Canonical: "title", HSProp: "jobtitle"},
}

// organizationWriteFields — the inverse of companiesMapping's 1:1 columns.
// size_band is read-only: numberofemployees→size_band is a lossy band bucketing
// (employees_to_size_band) with no unambiguous inverse. address, domains, and
// owner_id are the same V1 deferrals as person's.
var organizationWriteFields = []directWriteField{
	{Canonical: "display_name", HSProp: propName},
	{Canonical: industryField, HSProp: industryField},
}

// leadWriteFields — OVA-MAP-W5's writable Leads-object property that has a
// clean, transform-free projection: full_name → hs_lead_name.
//
// The status ↔ hs_lead_label projection is DEFERRED in both directions, on
// purpose: the read side (OVA-MAP-5, leadsMapping) keeps hs_lead_label a RAW
// passthrough and explicitly defers the typed status-enum remap "until a
// documented transform + a real capture". Writing a canonical status enum
// token straight into HubSpot's hs_lead_label would be exactly that
// untransformed projection the read declined as unsafe — and it would not be
// the inverse of the read (which produces hs_lead_label, never status). So
// lead status/label write-back waits on a pinned, bidirectional value map;
// this is a contract-first reconciliation item against OVA-MAP-W5, which
// currently assumes a transform OVA-MAP-5 has not defined.
//
// email and company_name are DERIVED through the required contact association
// (OVA-MAP-5), not Leads-object properties Margince can write, so they are
// read-only and absent here too.
var leadWriteFields = []directWriteField{
	{Canonical: targetFullName, HSProp: "hs_lead_name"},
}

// stringProp reads a canonical STRING field's writable value. present reports
// whether the key is present with a non-null value (an explicit "" is present
// — the clear-field signal on updates). A present non-string value is a type
// error, matching the native provider's StrictDecode 422 rather than
// coercing a number/bool into a HubSpot string property.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func stringProp(fields map[string]any, key string) (val string, present bool, err error) {
	raw, ok := fields[key]
	if !ok || raw == nil {
		return "", false, nil
	}
	s, isStr := raw.(string)
	if !isStr {
		return "", true, fmt.Errorf("overlay: field %q must be a string, got %T", key, raw)
	}
	return s, true, nil
}

// putString sets props[hsProp] from the canonical string field, honoring the
// create/update clear-field rule: an explicit "" clears on update (sent) and
// is skipped on create (nothing to set). Returns an error on a type violation.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func putString(props map[string]string, fields map[string]any, canonical, hsProp string, forUpdate bool) error {
	// An empty hsProp means the object class has no such writable property
	// (e.g. a note has no subject) — the canonical field is read-only for this
	// class and skipped, even though it is a valid contract field.
	if hsProp == "" {
		return nil
	}
	val, present, err := stringProp(fields, canonical)
	if err != nil {
		return err
	}
	if !present || (val == "" && !forUpdate) {
		return nil
	}
	props[hsProp] = val
	return nil
}

// copyDirect projects the direct 1:1 fields onto their HubSpot property names.
// A canonical field that is absent or JSON-null is not written; an explicit
// "" clears on update; a field not in the table is read-only and skipped.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func copyDirect(fields map[string]any, table []directWriteField, forUpdate bool) (map[string]string, error) {
	props := make(map[string]string)
	for _, f := range table {
		if err := putString(props, fields, f.Canonical, f.HSProp, forUpdate); err != nil {
			return nil, err
		}
	}
	return props, nil
}

// mapWriteDeal — OVA-MAP-W2. name/expected_close_date project directly;
// amount_minor + currency scale BACK to the decimal amount string by the
// ISO-4217 minor-unit exponent of currency (never a blanket ÷100), and
// currency sets deal_currency_code. pipeline_id/stage_id are read-only in
// overlay (null per OVA-MAP-6/W4) and never written.
//
// amount and currency are a PAIR: a caller must supply currency to write
// amount (the exponent chooses the decimal point), so the write-back engine
// combines a partial deal patch with the mirror's current amount_minor/
// currency BEFORE calling mapWrite (Provider.Update) — here, an amount_minor
// with no currency writes no amount rather than guess an exponent.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func mapWriteDeal(fields map[string]any, forUpdate bool) (writeMapping, error) {
	props, err := copyDirect(fields, dealWriteFields, forUpdate)
	if err != nil {
		return writeMapping{}, err
	}
	currency := strings.ToUpper(strings.TrimSpace(stringField(fields, "currency")))
	if currency != "" {
		props["deal_currency_code"] = currency
	}
	minor, ok, err := numericField(fields, "amount_minor")
	if err != nil {
		return writeMapping{}, err
	}
	if ok && currency != "" {
		props["amount"] = minorToDecimalString(minor, overlay.ISO4217MinorUnitExponent(currency))
	}
	return writeMapping{ObjectClass: objectClassDeals, Props: props}, nil
}

var dealWriteFields = []directWriteField{
	{Canonical: "name", HSProp: "dealname"},
	{Canonical: "expected_close_date", HSProp: "closedate"},
}

// mapWriteActivity — OVA-MAP-W3. kind selects the v3 engagement object class
// the write targets (the inverse of OVA-MAP-1's class→kind); each class has
// its own subject/body/direction property names. duration_seconds →
// hs_call_duration ×1000 (ms, call only); a task's due_at → hs_timestamp
// (task only). occurred_at is read-only (OVA-MAP-8) and never written.
// meeting_status is read-only: HubSpot's hs_meeting_outcome is a pinned
// enum vocabulary, not the canonical status string, so — like lead status —
// it awaits a documented bidirectional value map rather than sending a raw
// canonical token.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func mapWriteActivity(fields map[string]any, forUpdate bool) (writeMapping, error) {
	kind := strings.TrimSpace(stringField(fields, "kind"))
	if kind == "" {
		return writeMapping{}, fmt.Errorf("overlay: an activity write carries no kind — cannot choose a HubSpot engagement object class")
	}
	spec, ok := activityWriteSpecs[kind]
	if !ok {
		return writeMapping{}, fmt.Errorf("overlay: activity kind %q has no HubSpot engagement object class", kind)
	}
	props := make(map[string]string)
	if err := putString(props, fields, targetSubject, spec.subjectProp, forUpdate); err != nil {
		return writeMapping{}, err
	}
	if err := putString(props, fields, targetBody, spec.bodyProp, forUpdate); err != nil {
		return writeMapping{}, err
	}
	if err := applyActivitySpecials(props, fields, spec, forUpdate); err != nil {
		return writeMapping{}, err
	}
	return writeMapping{ObjectClass: spec.objectClass, Props: props}, nil
}

// applyActivitySpecials projects the per-class activity fields that are not a
// plain string copy: direction (uppercased to HubSpot's pinned INBOUND/
// OUTBOUND enum, the inverse of the lowercase canonical token), duration
// (seconds → milliseconds), and a task's due_at (RFC3339 → epoch millis). A
// class that does not carry a field has an empty/false spec entry and is
// skipped.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func applyActivitySpecials(props map[string]string, fields map[string]any, spec activityWriteSpec, forUpdate bool) error {
	if spec.directionProp != "" {
		val, present, err := stringProp(fields, targetDirection)
		if err != nil {
			return err
		}
		if present && (val != "" || forUpdate) {
			props[spec.directionProp] = strings.ToUpper(val)
		}
	}
	if spec.hasDuration {
		secs, ok, err := numericField(fields, "duration_seconds")
		if err != nil {
			return err
		}
		if ok {
			props["hs_call_duration"] = strconv.FormatInt(secs*1000, 10)
		}
	}
	if spec.hasDueAt {
		ms, ok, err := rfc3339ToMillis(fields["due_at"])
		if err != nil {
			return err
		}
		if ok {
			props[propHSTimestamp] = ms
		}
	}
	return nil
}

// activityWriteSpec is one engagement class's write projection — the inverse
// of the class's read ObjectMapping. An empty property name means the class
// does not carry that field. meeting_status is deliberately absent (deferred,
// see mapWriteActivity).
type activityWriteSpec struct {
	objectClass   string
	subjectProp   string
	bodyProp      string
	directionProp string
	hasDuration   bool
	hasDueAt      bool
}

// activityWriteSpecs maps each canonical activity kind to its engagement
// class's write projection, matching the read property names in
// mapping_hs.go (calls/meetings/emails/notes/tasks).
var activityWriteSpecs = map[string]activityWriteSpec{
	kindCall:    {objectClass: objectClassCalls, subjectProp: "hs_call_title", bodyProp: "hs_call_body", directionProp: "hs_call_direction", hasDuration: true},
	kindMeeting: {objectClass: objectClassMeetings, subjectProp: "hs_meeting_title", bodyProp: "hs_meeting_body"},
	kindEmail:   {objectClass: objectClassEmails, subjectProp: "hs_email_subject", bodyProp: "hs_email_text", directionProp: "hs_email_direction"},
	kindNote:    {objectClass: objectClassNotes, bodyProp: "hs_note_body"},
	kindTask:    {objectClass: objectClassTasks, subjectProp: "hs_task_subject", bodyProp: "hs_task_body", hasDueAt: true},
}

// stringField reads a string-valued canonical property, answering "" for an
// absent, JSON-null, or non-string value — a local convenience for the plain
// selector fields (currency, kind) mapWrite branches on before deciding a
// projection.
//
//craft:ignore naked-any v is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func stringField(fields map[string]any, key string) string {
	if s, ok := fields[key].(string); ok {
		return s
	}
	return ""
}

// numericField reads an integer-valued canonical field WITHOUT the precision
// loss of a float64 round-trip: the write path decodes the bag with
// json.Decoder.UseNumber (canonicalFields), so a number arrives as a
// json.Number and is parsed as a strict, range-checked int64. A plain float64
// (the unit tests that build the bag directly) is accepted only when it is an
// exact integer in int64 range — a fractional or out-of-range value is a type
// error, never a silent truncation. A null/absent value is ok=false, nil error.
//
//craft:ignore naked-any v is the JSON-decoded canonical value; the any is inherent to the decoded shape
func numericField(fields map[string]any, key string) (int64, bool, error) {
	switch t := fields[key].(type) {
	case nil:
		return 0, false, nil
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, true, fmt.Errorf("overlay: field %q is not an int64: %w", key, err)
		}
		return n, true, nil
	case int64:
		return t, true, nil
	case int:
		return int64(t), true, nil
	case float64:
		if t != float64(int64(t)) {
			return 0, true, fmt.Errorf("overlay: field %q must be an integer, got %v", key, t)
		}
		return int64(t), true, nil
	default:
		return 0, true, fmt.Errorf("overlay: field %q must be a number, got %T", key, fields[key])
	}
}

// rfc3339ToMillis parses a canonical RFC3339 timestamp (how a contract
// time.Time marshals to JSON) into a HubSpot epoch-millis property string. A
// null/absent value is ("", false, nil) — nothing to write. A present but
// unparseable value is an error, never a silently dropped timestamp.
//
//craft:ignore naked-any v is a JSON-decoded canonical value; the any is inherent to the decoded shape
func rfc3339ToMillis(v any) (string, bool, error) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false, nil
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", false, fmt.Errorf("overlay: write timestamp %q is not RFC3339: %w", s, err)
	}
	return strconv.FormatInt(ts.UnixMilli(), 10), true, nil
}

// minorToDecimalString is the inverse of decimalStringToMinor: it renders an
// integer minor-unit amount back to the currency-decimal string HubSpot's
// `amount` property expects, placing the decimal point by the ISO-4217
// minor-unit exponent (exponent 0 → no point; 2 → "10.00"; 3 → "1.500"), never
// a blanket ÷100.
func minorToDecimalString(minor int64, exponent int) string {
	// FormatInt renders the full magnitude of any int64 — including
	// math.MinInt64, whose positive is not int64-representable — so the decimal
	// point is inserted into the digit string with no int64→uint64 conversion
	// to reason about.
	s := strconv.FormatInt(minor, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	if exponent > 0 {
		for len(s) <= exponent {
			s = "0" + s
		}
		point := len(s) - exponent
		s = s[:point] + "." + s[point:]
	}
	if neg {
		s = "-" + s
	}
	return s
}
