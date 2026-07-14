import { describe, expect, it } from "vitest";
import type { CustomField } from "./customfields.form";
import {
  customFieldDisplay,
  customFieldsRecordSlice,
  customFieldsToBody,
  customFieldToFormField,
} from "./customfields.form";

const BOOL_LABELS = { yes: "Yes", no: "No" };

// A minimal active CustomField for one object; only the fields the form
// derivation reads are set — the rest is filler the helpers never touch.
function cf(overrides: Partial<CustomField>): CustomField {
  return {
    id: "cf-1",
    workspace_id: "ws-1",
    object: "deal",
    label: "Field",
    slug: "field",
    type: "text",
    status: "active",
    column_name: "cf_field",
    created_by: "u1",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("customFieldToFormField", () => {
  it("keys the control on the immutable column_name and shows the raw label", () => {
    const field = customFieldToFormField(
      cf({
        label: "Renewal date",
        column_name: "cf_renewal_date",
        type: "date",
      }),
      BOOL_LABELS,
    );
    expect(field.key).toBe("cf_renewal_date");
    expect(field.labelText).toBe("Renewal date");
    expect(field.type).toBe("date");
    // custom fields are nullable (backfilled NULL) — never required.
    expect(field.required).toBeFalsy();
  });

  it("maps number to a number control", () => {
    expect(
      customFieldToFormField(cf({ type: "number" }), BOOL_LABELS).type,
    ).toBe("number");
  });

  it("renders a picklist as a select of its options", () => {
    const field = customFieldToFormField(
      cf({ type: "picklist", options: ["Direct", "Reseller", "Tender"] }),
      BOOL_LABELS,
    );
    expect(field.type).toBe("select");
    expect(field.options).toEqual([
      { value: "Direct", label: "Direct" },
      { value: "Reseller", label: "Reseller" },
      { value: "Tender", label: "Tender" },
    ]);
  });

  it("renders a boolean as a Yes/No select using the supplied labels", () => {
    const field = customFieldToFormField(cf({ type: "boolean" }), BOOL_LABELS);
    expect(field.type).toBe("select");
    expect(field.options).toEqual([
      { value: "true", label: "Yes" },
      { value: "false", label: "No" },
    ]);
  });

  it("renders currency as a number control whose toInput shows major units", () => {
    const field = customFieldToFormField(
      cf({ type: "currency", currency: "EUR" }),
      BOOL_LABELS,
    );
    expect(field.type).toBe("number");
    // stored as bigint minor units; the form edits major units.
    expect(field.toInput?.(1250)).toBe("12.5");
    expect(field.toInput?.(null)).toBe("");
    expect(field.toInput?.(undefined)).toBe("");
  });
});

describe("customFieldsToBody", () => {
  const fields = [
    cf({ type: "text", column_name: "cf_note" }),
    cf({ type: "number", column_name: "cf_score" }),
    cf({ type: "currency", column_name: "cf_ceiling" }),
    cf({ type: "boolean", column_name: "cf_active" }),
    cf({ type: "date", column_name: "cf_due" }),
  ];

  it("coerces each value to its stored type, keyed by column_name", () => {
    const body = customFieldsToBody(
      {
        cf_note: "hello",
        cf_score: "42.5",
        cf_ceiling: "12.50",
        cf_active: "true",
        cf_due: "2026-01-01",
      },
      fields,
    );
    expect(body).toEqual({
      cf_note: "hello",
      cf_score: "42.5", // numeric round-trips as a string (no float)
      cf_ceiling: 1250, // major → bigint minor units
      cf_active: true,
      cf_due: "2026-01-01",
    });
  });

  it("sends null for a cleared field so the write actually clears the column", () => {
    const body = customFieldsToBody(
      {
        cf_note: "",
        cf_score: "  ",
        cf_ceiling: "",
        cf_active: "",
        cf_due: "",
      },
      fields,
    );
    expect(body).toEqual({
      cf_note: null,
      cf_score: null,
      cf_ceiling: null,
      cf_active: null,
      cf_due: null,
    });
  });
});

describe("customFieldDisplay", () => {
  const opts = { locale: "en" as const, boolLabels: BOOL_LABELS };

  it("omits an absent value (evidence-or-omit)", () => {
    expect(customFieldDisplay(cf({ type: "text" }), null, opts)).toBeNull();
    expect(
      customFieldDisplay(cf({ type: "text" }), undefined, opts),
    ).toBeNull();
    expect(customFieldDisplay(cf({ type: "text" }), "", opts)).toBeNull();
  });

  it("shows text / number / date / picklist as their stored value", () => {
    expect(customFieldDisplay(cf({ type: "text" }), "hello", opts)).toBe(
      "hello",
    );
    expect(customFieldDisplay(cf({ type: "number" }), "42.5", opts)).toBe(
      "42.5",
    );
    expect(customFieldDisplay(cf({ type: "date" }), "2026-03-01", opts)).toBe(
      "2026-03-01",
    );
    expect(customFieldDisplay(cf({ type: "picklist" }), "Reseller", opts)).toBe(
      "Reseller",
    );
  });

  it("formats currency minor units with the field's currency code", () => {
    const field = cf({ type: "currency", currency: "EUR" });
    expect(customFieldDisplay(field, 500000, opts)).toBe("€5,000.00");
  });

  it("shows a boolean as its Yes/No label", () => {
    expect(customFieldDisplay(cf({ type: "boolean" }), true, opts)).toBe("Yes");
    expect(customFieldDisplay(cf({ type: "boolean" }), false, opts)).toBe("No");
  });
});

describe("customFieldsRecordSlice", () => {
  it("picks the raw cf column values off a fetched record", () => {
    const record = {
      id: "d1",
      name: "Globex",
      cf_renewal_date: "2026-03-01",
      cf_ceiling: 1250,
    };
    const fields = [
      cf({ column_name: "cf_renewal_date", type: "date" }),
      cf({ column_name: "cf_ceiling", type: "currency" }),
    ];
    expect(customFieldsRecordSlice(record, fields)).toEqual({
      cf_renewal_date: "2026-03-01",
      cf_ceiling: 1250,
    });
  });
});
