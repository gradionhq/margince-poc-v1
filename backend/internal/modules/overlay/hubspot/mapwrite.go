// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
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
// OVA-MAP-W (full_name, occurred_at, lead email/company_name, deal
// pipeline_id/stage_id, org size_band) never appears. An empty Props means
// the write touched only read-only fields: a no-op the Provider surfaces to
// the caller, never a POST/PATCH of a guessed value.
type writeMapping struct {
	ObjectClass string
	Props       map[string]string
}

// mapWrite projects a canonical write (the entity type + the canonical field
// bag, the JSON-decoded contract the frozen datasource seam carries) onto the
// HubSpot object class and property set per OVA-MAP-W1..6. It is the inverse
// of mapRecord and pure: no I/O, no transport — the golden write-mapping suite
// (AC-OV-12) exercises it directly.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag from the frozen datasource seam; the any is inherent to the decoded shape
func mapWrite(canonicalClass string, fields map[string]any) (writeMapping, error) {
	switch canonicalClass {
	case "person":
		return writeMapping{ObjectClass: objectClassContacts, Props: copyDirect(fields, personWriteFields)}, nil
	case "organization":
		return writeMapping{ObjectClass: objectClassCompanies, Props: copyDirect(fields, organizationWriteFields)}, nil
	case "lead":
		return writeMapping{ObjectClass: objectClassLeads, Props: copyDirect(fields, leadWriteFields)}, nil
	case "deal":
		return mapWriteDeal(fields)
	case activityTarget:
		return mapWriteActivity(fields)
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

// copyDirect projects the direct 1:1 fields present in the canonical bag onto
// their HubSpot property names. A canonical field that is absent, JSON-null,
// or empty-string is not written (nothing to set); a field not in the table is
// read-only and silently skipped (its read-only-ness is the point).
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func copyDirect(fields map[string]any, table []directWriteField) map[string]string {
	props := make(map[string]string)
	for _, f := range table {
		if s, ok := writableString(fields[f.Canonical]); ok {
			props[f.HSProp] = s
		}
	}
	return props
}

// mapWriteDeal — OVA-MAP-W2. name/expected_close_date project directly;
// amount_minor + currency scale BACK to the decimal amount string by the
// ISO-4217 minor-unit exponent of currency (never a blanket ÷100), and
// currency sets deal_currency_code. pipeline_id/stage_id are read-only in
// overlay (null per OVA-MAP-6/W4) and never written.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func mapWriteDeal(fields map[string]any) (writeMapping, error) {
	props := copyDirect(fields, dealWriteFields)
	currency := strings.ToUpper(strings.TrimSpace(stringField(fields, "currency")))
	if currency != "" {
		props["deal_currency_code"] = currency
	}
	// amount_minor + currency → decimal amount string. A null/absent
	// amount_minor, or no currency to choose an exponent, writes no amount
	// (never a guessed zero) — the inverse of transformAmountMinorByCurrency's
	// null-not-guess rule.
	if minor, ok := numericField(fields, "amount_minor"); ok && currency != "" {
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
// its own subject/body/direction property names (mirroring the five read
// mappings). duration_seconds → hs_call_duration ×1000 (ms, call only); a
// task's due_at → hs_timestamp (task only). occurred_at is NOT written for any
// class: for a task the read sources it from hs_createdate (a genuine creation
// stamp, OVA-MAP-8), and for calls/emails/meetings write-back of the
// occurrence timestamp is a V1 deferral rather than risk moving an event's
// time on the incumbent from a partial patch — a tracked follow-up, not a
// nature-of-the-field claim.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag; the any is inherent to the decoded shape
func mapWriteActivity(fields map[string]any) (writeMapping, error) {
	kind := strings.TrimSpace(stringField(fields, "kind"))
	if kind == "" {
		return writeMapping{}, fmt.Errorf("overlay: an activity write carries no kind — cannot choose a HubSpot engagement object class")
	}
	spec, ok := activityWriteSpecs[kind]
	if !ok {
		return writeMapping{}, fmt.Errorf("overlay: activity kind %q has no HubSpot engagement object class", kind)
	}
	props := make(map[string]string)
	// The class's writable string props (subject/body/direction/
	// meeting_status vary per class) project directly; a canonical field the
	// class does not carry simply has no spec entry.
	for canonical, hsProp := range spec.stringProps {
		if s, ok := writableString(fields[canonical]); ok {
			props[hsProp] = s
		}
	}
	// duration_seconds → hs_call_duration ×1000 (call only).
	if spec.hasDuration {
		if secs, ok := numericField(fields, "duration_seconds"); ok {
			props["hs_call_duration"] = strconv.FormatInt(secs*1000, 10)
		}
	}
	// due_at → hs_timestamp as epoch millis (task only). occurred_at is never
	// written for any class.
	if spec.hasDueAt {
		ms, ok, err := rfc3339ToMillis(fields["due_at"])
		if err != nil {
			return writeMapping{}, err
		}
		if ok {
			props[propHSTimestamp] = ms
		}
	}
	return writeMapping{ObjectClass: spec.objectClass, Props: props}, nil
}

// activityWriteSpec is one engagement class's write projection — the inverse
// of the class's read ObjectMapping. stringProps maps each writable
// canonical activity field to this class's HubSpot property; the two
// booleans carry the special-cased duration (×1000) and due_at (epoch
// millis) projections that are not plain string copies.
type activityWriteSpec struct {
	objectClass string
	stringProps map[string]string
	hasDuration bool
	hasDueAt    bool
}

// activityWriteSpecs maps each canonical activity kind to its engagement
// class's write projection, matching the read property names in
// mapping_hs.go (calls/meetings/emails/notes/tasks).
var activityWriteSpecs = map[string]activityWriteSpec{
	kindCall:    {objectClass: objectClassCalls, stringProps: map[string]string{targetSubject: "hs_call_title", targetBody: "hs_call_body", targetDirection: "hs_call_direction"}, hasDuration: true},
	kindMeeting: {objectClass: objectClassMeetings, stringProps: map[string]string{targetSubject: "hs_meeting_title", targetBody: "hs_meeting_body", "meeting_status": "hs_meeting_outcome"}},
	kindEmail:   {objectClass: objectClassEmails, stringProps: map[string]string{targetSubject: "hs_email_subject", targetBody: "hs_email_text", targetDirection: "hs_email_direction"}},
	kindNote:    {objectClass: objectClassNotes, stringProps: map[string]string{targetBody: "hs_note_body"}},
	kindTask:    {objectClass: objectClassTasks, stringProps: map[string]string{targetSubject: "hs_task_subject", targetBody: "hs_task_body"}, hasDueAt: true},
}

// writableString reports a canonical field's string form when it carries a
// non-empty writable value. An absent, JSON-null, or empty-string value is
// "nothing to write" (ok=false), so it is never PATCHed. A numeric value is
// rendered without a trailing ".0" so an integer stays an integer on the wire.
//
//craft:ignore naked-any v is a JSON-decoded canonical value; the any is inherent to the decoded shape
func writableString(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		if strings.TrimSpace(t) == "" {
			return "", false
		}
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return "", false
	}
}

// numericField reads an integer-valued canonical field. The canonical bag is
// always JSON-decoded (canonicalFields → json.Unmarshal), so a number arrives
// as float64; a null/absent/non-numeric value is ok=false.
//
//craft:ignore naked-any v is a JSON-decoded canonical value; the any is inherent to the decoded shape
func numericField(fields map[string]any, key string) (int64, bool) {
	f, ok := fields[key].(float64)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

// rfc3339ToMillis parses a canonical RFC3339 timestamp (how a contract
// time.Time marshals to JSON) into a HubSpot epoch-millis property string. A
// null/absent value is (._, false, nil) — nothing to write. A present but
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

// minorToDecimalString is the inverse of decimalStringToMinor: it renders an
// integer minor-unit amount back to the currency-decimal string HubSpot's
// `amount` property expects, placing the decimal point by the ISO-4217
// minor-unit exponent (exponent 0 → no point; 2 → "10.00"; 3 → "1.500"), never
// a blanket ÷100.
func minorToDecimalString(minor int64, exponent int) string {
	if exponent <= 0 {
		return strconv.FormatInt(minor, 10)
	}
	neg := minor < 0
	if neg {
		minor = -minor
	}
	digits := strconv.FormatInt(minor, 10)
	// Left-pad so there are at least exponent+1 digits (a leading integer part).
	for len(digits) <= exponent {
		digits = "0" + digits
	}
	point := len(digits) - exponent
	out := digits[:point] + "." + digits[point:]
	if neg {
		out = "-" + out
	}
	return out
}
