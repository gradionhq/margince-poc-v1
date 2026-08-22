// Catalog-driven custom fields on the record create/edit forms. A workspace's
// active custom fields are real `cf_*` columns the record API already round-
// trips (CF-T05); this turns each one into a form control and coerces its value
// to/from the stored type, so a field defined on the admin screen (CF-T06) is
// editable on the record like any core field.
//
// The pure derivations live here (unit-tested); the hook wires them to the
// live catalog and this workspace's i18n Yes/No labels.

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { formatMoneyOrAbsent } from "../format/format";
import { toMajorUnits, toMinorUnits } from "../format/minorunits";
import { webUrl } from "../format/weburl";
import { type Locale, useT } from "../i18n";
import type { CreateField } from "./create";
import type { CfObject } from "./customfields.logic";

export type CustomField = components["schemas"]["CustomField"];

export type BooleanLabels = { yes: string; no: string };

// One active custom field → a form control keyed on its immutable column_name.
// Custom fields are always nullable (backfilled NULL), so never required.
export function customFieldToFormField(
  field: CustomField,
  boolLabels: BooleanLabels,
): CreateField {
  const base = { key: field.column_name, labelText: field.label };
  switch (field.type) {
    case "number":
      return { ...base, type: "number" };
    case "date":
      return { ...base, type: "date" };
    case "currency":
      // Stored as bigint minor units; the form edits major units, at the scale
      // THIS field's own currency carries. The catalog holds that code
      // (CF-T06) — the same one the renderer below formats with — so a
      // hard-coded hundred here made the form and the display disagree for
      // every currency that is not two-decimal.
      return {
        ...base,
        type: "number",
        toInput: (raw) =>
          raw == null || raw === ""
            ? ""
            : String(toMajorUnits(Number(raw), field.currency ?? "")),
      };
    case "picklist":
      return {
        ...base,
        type: "select",
        options: (field.options ?? []).map((option) => ({
          value: option,
          label: option,
        })),
      };
    case "boolean":
      return {
        ...base,
        type: "select",
        options: [
          { value: "true", label: boolLabels.yes },
          { value: "false", label: boolLabels.no },
        ],
      };
    default:
      return { ...base, type: "text" };
  }
}

// Coerce one field's form string to its stored value. Empty → null so a cleared
// field actually clears the column.
function coerceWrite(field: CustomField, raw: string): unknown {
  const value = raw.trim();
  if (value === "") {
    return null;
  }
  switch (field.type) {
    case "currency": {
      // An unusable amount is OMITTED, not sent. toMinorUnits answers NaN for a
      // figure finer than its currency or too large to scale exactly, and NaN
      // serialises to null — which on a PATCH means "clear this column". A
      // typo would then DELETE a stored price instead of being refused, which
      // is the one outcome worse than a wrong figure.
      const minor = toMinorUnits(Number(value), field.currency ?? "");
      if (Number.isNaN(minor)) {
        // Sent as the typed TEXT rather than omitted. Omitting protected the
        // stored value but said nothing: the reader watched their edit vanish
        // on save with no error. The column is a bigint, so the server refuses
        // a non-integer by name and the reader is told which field and why —
        // the same posture as documentextraction, which sends an unconvertible
        // figure as typed "so the server refuses it by name, rather than being
        // silently rounded into something plausible".
        return value;
      }
      return minor;
    }
    case "boolean":
      return value === "true" ? true : value === "false" ? false : null;
    default:
      // text / picklist / date / number (numeric round-trips as a string).
      return value;
  }
}

// The write-body slice: every active field's coerced value, keyed by
// column_name. Merged into the create/update request (which allow extra keys).
// Values arrive as form strings; the edit boundary types them as `unknown`, so
// each is stringified before coercion.
export function customFieldsToBody(
  values: Record<string, unknown>,
  fields: CustomField[],
): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  for (const field of fields) {
    const raw = values[field.column_name];
    body[field.column_name] = coerceWrite(
      field,
      raw == null ? "" : String(raw),
    );
  }
  return body;
}

// One field's display string for the read-only 360, or null when the record
// carries no value (evidence-or-omit: an empty field is absent, never guessed).
export function customFieldDisplay(
  field: CustomField,
  raw: unknown,
  opts: { locale: Locale; boolLabels: BooleanLabels },
): string | null {
  if (raw == null || raw === "") {
    return null;
  }
  switch (field.type) {
    case "currency":
      // The catalog carries the code for a currency field (CF-T06), so one that
      // arrives without it is a broken definition rather than a shape to guess
      // at: the reader is told the figure cannot be read, instead of being
      // handed a euro sign over a unit nobody stated.
      return formatMoneyOrAbsent(Number(raw), field.currency, opts.locale);
    case "boolean":
      return raw === true || raw === "true"
        ? opts.boolLabels.yes
        : opts.boolLabels.no;
    default:
      // text / picklist / number / date (a plain YYYY-MM-DD, shown verbatim to
      // avoid a timezone shift a datetime formatter would introduce).
      return String(raw);
  }
}

// Where a displayed value may be followed to, or null when it is only text.
//
// The type set is closed and carries no `url` type, so a link is recognised
// from the VALUE, which makes the scheme check the load-bearing half rather
// than a formality. Which schemes those are is not this file's to decide —
// `format/weburl` is the one place that answers it for every surface.
export function customFieldHref(value: string): string | null {
  return webUrl(value)?.href ?? null;
}

// The read slice: the raw cf column values off a fetched record, so the edit
// modal can prefill them (currency major/minor conversion happens in the
// field's toInput at prefill time).
export function customFieldsRecordSlice(
  record: Record<string, unknown>,
  fields: CustomField[],
): Record<string, unknown> {
  const slice: Record<string, unknown> = {};
  for (const field of fields) {
    slice[field.column_name] = record[field.column_name];
  }
  return slice;
}

// What a screen needs to render + persist custom fields for one object:
// the raw active field list, the form controls (with a leading divider when
// non-empty), a record→prefill slice, and a values→request-body slice.
export type ObjectCustomFields = {
  fields: CustomField[];
  formFields: CreateField[];
  recordSlice: (record: Record<string, unknown>) => Record<string, unknown>;
  toBody: (values: Record<string, unknown>) => Record<string, unknown>;
};

const EMPTY_CUSTOM_FIELDS: CustomField[] = [];

// Live active custom fields for one object, shaped for the record forms. A read
// failure (or the schema-pool-less deployment) degrades to no custom fields —
// the core form still works — rather than blocking the record's own edit.
export function useObjectCustomFields(object: CfObject): ObjectCustomFields {
  const t = useT();
  const query = useQuery({
    queryKey: ["custom-fields", object],
    queryFn: async () => {
      const { data, error } = await api.GET("/custom-fields", {
        params: { query: { object } },
      });
      if (error) {
        throw error;
      }
      return data;
    },
  });

  const fields = (query.data?.data ?? EMPTY_CUSTOM_FIELDS).filter(
    (field) => field.status === "active",
  );
  const boolLabels = { yes: t("field.yes"), no: t("field.no") };

  const controls = fields.map((field) =>
    customFieldToFormField(field, boolLabels),
  );
  const formFields: CreateField[] =
    controls.length === 0
      ? controls
      : [
          {
            key: "__cf_divider__",
            labelText: t("cf.formSection"),
            divider: true,
          },
          ...controls,
        ];

  return {
    fields,
    formFields,
    recordSlice: (record) => customFieldsRecordSlice(record, fields),
    toBody: (values) => customFieldsToBody(values, fields),
  };
}
