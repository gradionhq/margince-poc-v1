// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// decimalAmountPattern accepts only a plain or scientific DECIMAL amount —
// an optional sign, digits with an optional fraction (or a leading-dot
// fraction), and an optional base-10 exponent of at most three digits. It
// exists to fence big.Rat.SetString, which is far more permissive than a
// HubSpot amount ever is: SetString would otherwise accept rationals
// ("1/2"), hex/binary prefixes ("0x10"), and digit-group underscores
// ("1_000") and silently coin money from them. The three-digit exponent
// cap (with maxAmountLen below) also bounds the SetString allocation: a
// value like "1e-1000000" would otherwise make it build a million-digit
// denominator for a number that rounds to zero. Anything this does not
// match is rejected before conversion.
var decimalAmountPattern = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d{1,3})?$`)

// maxAmountLen bounds the mantissa length so a pathological all-digits input
// cannot force a huge big.Rat allocation. No genuine HubSpot money amount
// approaches this; the mirror's int64 minor-unit domain is ~19 digits.
const maxAmountLen = 40

// TargetKind names the shape of an incumbent-CRM property's landing spot
// in the mirror. A 1:1 flat projector can express none of these on its
// own — child rows and jsonb assemblies need their own dispatch
// (design.md §4.8).
type TargetKind int

const (
	// TargetColumn is a 1:1 property → mirror column mapping.
	TargetColumn TargetKind = iota
	// TargetChild is a 1:N mapping into a child row (e.g. person_email):
	// To is "<parent>.<child column>".
	TargetChild
	// TargetAssembler is an N:1 mapping: every From property is gathered
	// into one map[string]any and handed to Transform, which constructs
	// the single To column (typically jsonb, e.g. address/social).
	TargetAssembler
)

// String renders the TargetKind name for error messages; never leaks a
// raw int to a caller trying to diagnose a bad mapping.
func (k TargetKind) String() string {
	switch k {
	case TargetColumn:
		return "column"
	case TargetChild:
		return "child"
	case TargetAssembler:
		return "assembler"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}

// FieldMapping is one incumbent property (or, for TargetAssembler,
// property set) → mirror target declaration.
//
// Transform names a function in the closed transform registry, applied
// to the raw value(s) before landing on To. Resolve names an external
// lookup (e.g. owner-id → app_user via mirror_user_map) that Apply
// cannot perform on its own — it has no store access — so a Resolve
// field's raw value passes through unmodified and the actual lookup is
// the ingest layer's job.
type FieldMapping struct {
	From      []string
	To        string
	Kind      TargetKind
	Transform string
	Resolve   string
	// AlwaysEmit forces a TargetAssembler field to run its Transform even
	// when the incumbent record carried NONE of its From properties, so the
	// transform can synthesize a value from nothing — the shape a required,
	// always-present display field with a fallback needs (person.full_name,
	// OVA-MAP-3: never left empty). It is meaningless on the other kinds,
	// whose absence-is-a-no-op behavior is exactly right.
	AlwaysEmit bool
}

// ObjectMapping is the code-declared, test-guarded field map for one
// incumbent object class (e.g. HubSpot "contacts" → Margince "person").
//
// ExternalKey and Baseline are structural, not members of Fields: every
// mapped object carries them, so Apply handles them once rather than
// requiring every ObjectMapping to repeat a FieldMapping for them.
// ExternalKey is the raw property that becomes the mirror's external_id;
// Baseline is the raw property that drives both the incremental-sync
// watermark and the mirror's last_synced_at column.
type ObjectMapping struct {
	Source         string
	Target         string
	ExternalKey    string
	Baseline       string
	Fields         []FieldMapping
	UnmappedPolicy string
}

// transforms is the CLOSED registry (design.md §4.8): mapping.yaml/Go
// declarations only ever *select* a name from this set, never express
// inline logic. Apply rejects any Transform name absent here — never a
// silent passthrough.
// The transform registry seam operates on untyped incoming JSON property
// values: an incumbent record's property is any of string/number/object,
// so a transform takes `any` and asserts its concrete type in its first
// lines (returning a typed error on mismatch). The `any` is the data
// boundary itself, not a missing type — hence the per-transform waivers.
var transforms = map[string]func(any) (any, error){
	"lowercase":                transformLowercase,
	"uppercase":                transformUppercase,
	"ms_to_seconds":            transformMsToSeconds,
	"full_name":                transformFullName,
	"amount_minor_by_currency": transformAmountMinorByCurrency,
	"employees_to_size_band":   transformEmployeesToSizeBand,
	"address_json":             transformAddressJSON,
}

//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformLowercase(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("overlay: lowercase transform expects a string, got %T", v)
	}
	return strings.ToLower(s), nil
}

//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformUppercase(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("overlay: uppercase transform expects a string, got %T", v)
	}
	return strings.ToUpper(s), nil
}

// transformMsToSeconds divides a HubSpot millisecond duration string
// (hs_call_duration) into integer seconds (OVA-MAP-2). Storing the raw
// millisecond value inflates durations ×1000. valueFor already treats an
// empty string as an absent property, so this never sees "".
//
//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformMsToSeconds(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("overlay: ms_to_seconds transform expects a string, got %T", v)
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("overlay: ms_to_seconds could not parse %q as integer milliseconds: %w", s, err)
	}
	return ms / 1000, nil
}

// transformFullName assembles the required, always-present person.full_name
// display field (OVA-MAP-3) from a gathered {firstname, lastname, email}
// property set: firstname + ' ' + lastname trimmed of surrounding
// whitespace, falling back to the primary email's local part, and only then
// to a stable non-empty placeholder — a mirrored contact must never surface
// with an empty full_name. Declared AlwaysEmit so it produces the placeholder
// even for a contact that carried no name and no email at all.
//
//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformFullName(v any) (any, error) {
	fields, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("overlay: full_name transform expects a property map, got %T", v)
	}
	if name := strings.TrimSpace(stringField(fields, "firstname") + " " + stringField(fields, "lastname")); name != "" {
		return name, nil
	}
	if email := strings.TrimSpace(stringField(fields, "email")); email != "" {
		if local, _, found := strings.Cut(email, "@"); found && local != "" {
			return local, nil
		}
		return email, nil
	}
	return "(unnamed contact)", nil
}

// transformAmountMinorByCurrency scales a HubSpot decimal-string deal amount
// to integer minor units by the ISO-4217 minor-unit exponent of its
// deal_currency_code (OVA-MAP-4) — never a blanket ×100. A deal with no
// currency code (or no amount) maps amount_minor to null rather than guessing
// an exponent. The currency column itself is mapped separately (uppercased).
//
//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformAmountMinorByCurrency(v any) (any, error) {
	fields, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("overlay: amount_minor_by_currency transform expects a property map, got %T", v)
	}
	code := strings.ToUpper(strings.TrimSpace(stringField(fields, "deal_currency_code")))
	amountRaw, hasAmount := fields["amount"]
	if hasAmount && code != "" {
		amountStr, ok := amountRaw.(string)
		if !ok {
			return nil, fmt.Errorf("overlay: amount_minor_by_currency: amount expects a string, got %T", amountRaw)
		}
		if amount := strings.TrimSpace(amountStr); amount != "" {
			scale := int64(1)
			for i := 0; i < iso4217MinorUnitExponent(code); i++ {
				scale *= 10
			}
			minor, err := decimalStringToMinor(amount, scale)
			if err != nil {
				return nil, fmt.Errorf("overlay: amount_minor_by_currency could not convert %q (%s): %w", amount, code, err)
			}
			return minor, nil
		}
	}
	// No amount, no currency code to choose an exponent, or an empty amount:
	// map amount_minor to null rather than guess a scale (OVA-MAP-4). A nil
	// value with a nil error is exactly that "null, and not an error" —
	// applyTransform lands it as a JSON null on the column.
	return nil, nil //nolint:nilnil // deliberate: null amount_minor is a valid, non-error mapping result
}

// iso4217MinorUnitExponent returns a currency's minor-unit exponent. Two is
// ISO-4217's own default and covers the vast majority; the exceptions are the
// zero-decimal and three-decimal currencies enumerated here. Choosing the
// exponent from the code — not a blanket ×100 — is what the money model
// (data-semantics §1 / DM-CONV-9) requires.
func iso4217MinorUnitExponent(code string) int {
	switch code {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	default:
		return 2
	}
}

// stringField reads a string-valued property from a gathered assembler map,
// answering "" for an absent or non-string value — the transforms above
// treat "" as "not provided".
//
//craft:ignore naked-any gathered assembler values are decoded incumbent JSON, read by conventional key name
func stringField(fields map[string]any, key string) string {
	s, _ := fields[key].(string)
	return s
}

// decimalStringToMinor converts an exact decimal string into integer minor
// units at the given per-major-unit scale (100 for a two-decimal currency,
// 1 for a zero-decimal currency, 1000 for a three-decimal one — chosen by
// the caller from the deal's currency code), rounding half away from zero.
//
// The conversion is exact (math/big.Rat), NOT via float64: strconv.ParseFloat
// introduces binary rounding error before the scale — "1.005" parses to
// 1.00499999… and truncates to 100 minor units when 101 is correct — so a
// float path silently corrupts amounts that a decimal wire value states
// precisely. big.Rat.SetString also rejects non-finite tokens ("NaN",
// "Inf") that a float parse would accept, and IsInt64 fences an amount too
// large for the mirror column.
func decimalStringToMinor(s string, scale int64) (int64, error) {
	if len(s) > maxAmountLen {
		return 0, fmt.Errorf("amount too long")
	}
	if !decimalAmountPattern.MatchString(s) {
		return 0, fmt.Errorf("not a decimal amount")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, fmt.Errorf("not a finite decimal amount")
	}
	r.Mul(r, new(big.Rat).SetInt64(scale))
	// r is now the minor-unit value as an exact rational; round its
	// numerator/denominator half away from zero to the nearest integer.
	num, den := r.Num(), r.Denom() // den > 0, normalized
	quo := new(big.Int)
	rem := new(big.Int)
	quo.QuoRem(num, den, rem) // truncates toward zero; rem carries num's sign
	twiceRem := new(big.Int).Abs(rem)
	twiceRem.Lsh(twiceRem, 1) // 2*|rem|
	if twiceRem.Cmp(den) >= 0 {
		if num.Sign() < 0 {
			quo.Sub(quo, big.NewInt(1))
		} else {
			quo.Add(quo, big.NewInt(1))
		}
	}
	if !quo.IsInt64() {
		return 0, fmt.Errorf("amount out of range")
	}
	return quo.Int64(), nil
}

// transformEmployeesToSizeBand buckets a HubSpot numberofemployees
// decimal-string into the mirror's size_band enum. valueFor already
// treats an empty string as an absent property, so this never sees "".
//
//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformEmployeesToSizeBand(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("overlay: employees_to_size_band transform expects a string, got %T", v)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("overlay: employees_to_size_band could not parse %q as an integer", s)
	}
	switch {
	case n <= 10:
		return "1-10", nil
	case n <= 50:
		return "11-50", nil
	case n <= 200:
		return "51-200", nil
	case n <= 1000:
		return "201-1000", nil
	default:
		return "1001+", nil
	}
}

// transformAddressJSON assembles the address property set into the
// mirror's jsonb address column, dropping properties absent from the
// incumbent record.
//
//craft:ignore naked-any transform seam over untyped incoming JSON property values; asserts concrete type within
func transformAddressJSON(v any) (any, error) {
	fields, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("overlay: address_json transform expects a property map, got %T", v)
	}
	out := make(map[string]any, len(fields))
	for k, val := range fields {
		s, ok := val.(string)
		if ok && s == "" {
			continue
		}
		if val == nil {
			continue
		}
		out[k] = val
	}
	return out, nil
}

// Apply projects a raw incumbent record (a flat properties map, per the
// wire shapes observed in design.md §11) through an ObjectMapping,
// returning the mirror-shaped target map, the list of raw keys that
// matched no declared mapping (UnmappedPolicy governs what the caller
// does with them — "flag" surfaces them, never silently drops per
// UC-E18-01 F3), and an error if the mapping itself is malformed (an
// unknown Transform name, or an unrecognized TargetKind).
func Apply(m ObjectMapping, raw map[string]any) (map[string]any, []string, error) {
	out := map[string]any{}
	consumed := make(map[string]bool, len(raw))

	if m.ExternalKey != "" {
		consumed[m.ExternalKey] = true
		if v, ok := raw[m.ExternalKey]; ok {
			out["external_id"] = v
		}
	}
	if m.Baseline != "" {
		consumed[m.Baseline] = true
		if v, ok := raw[m.Baseline]; ok {
			out["last_synced_at"] = v
		}
	}

	for _, f := range m.Fields {
		for _, k := range f.From {
			consumed[k] = true
		}
		if err := applyField(out, f, raw); err != nil {
			return nil, nil, err
		}
	}

	var unmapped []string
	for k := range raw {
		if !consumed[k] {
			unmapped = append(unmapped, k)
		}
	}
	sort.Strings(unmapped)

	return out, unmapped, nil
}

// applyField computes one FieldMapping's projected value and writes it
// into out at the shape its TargetKind dictates. It is a no-op (not an
// error) when the incumbent record never sent any of the field's From
// properties — the mirror simply carries no value for that column yet.
func applyField(out map[string]any, f FieldMapping, raw map[string]any) error {
	val, present, err := valueFor(f, raw)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}

	switch f.Kind {
	case TargetColumn:
		out[f.To] = val
	case TargetChild:
		parent, child, ok := strings.Cut(f.To, ".")
		if !ok {
			return fmt.Errorf("overlay: child target %q must be \"<parent>.<child column>\"", f.To)
		}
		childRow, _ := out[parent].(map[string]any)
		if childRow == nil {
			childRow = map[string]any{}
		}
		childRow[child] = val
		out[parent] = childRow
	case TargetAssembler:
		out[f.To] = val
	default:
		return fmt.Errorf("overlay: unknown target kind %s for field %q", f.Kind, f.To)
	}
	return nil
}

// valueFor computes one FieldMapping's projected value. present is false
// when every one of the field's From keys is absent from raw (or, for a
// single-value field, present but the empty string HubSpot uses for an
// unset property), so Apply can skip emitting a target for data the
// incumbent record never sent.
//
//craft:ignore naked-any raw is a parsed-JSON incumbent record and the return is its mapped JSON value — the untyped data boundary
func valueFor(f FieldMapping, raw map[string]any) (any, bool, error) {
	if f.Kind == TargetAssembler {
		gathered := make(map[string]any, len(f.From))
		present := false
		for _, k := range f.From {
			if v, ok := raw[k]; ok {
				gathered[k] = v
				present = true
			}
		}
		if !present && !f.AlwaysEmit {
			return nil, false, nil
		}
		return applyTransform(f, gathered)
	}

	if len(f.From) != 1 {
		return nil, false, fmt.Errorf("overlay: %s target %q must declare exactly one From property, got %d", f.Kind, f.To, len(f.From))
	}
	v, ok := raw[f.From[0]]
	if !ok {
		return nil, false, nil
	}
	if s, isString := v.(string); isString && s == "" {
		return nil, false, nil
	}
	return applyTransform(f, v)
}

// applyTransform runs v through the field's Transform, if any. An
// unknown Transform name is a mapping-declaration error, never a silent
// passthrough. A Resolve field carries its raw value through as-is —
// Apply has no store access to perform the lookup itself.
//
//craft:ignore naked-any v/the return value are decoded incumbent values flowing through the closed transform registry, not a missed type
func applyTransform(f FieldMapping, v any) (any, bool, error) {
	if f.Transform == "" {
		return v, true, nil
	}
	fn, ok := transforms[f.Transform]
	if !ok {
		return nil, false, fmt.Errorf("overlay: unknown transform %q declared on field %q", f.Transform, f.To)
	}
	out, err := fn(v)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
