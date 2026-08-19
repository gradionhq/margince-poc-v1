/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { CustomFieldsCard } from "./customfields.card";
import type { CustomField, ObjectCustomFields } from "./customfields.form";

// The card reads the live catalog through useObjectCustomFields; mock only that
// hook so the display/omit behavior is tested against controlled fields, while
// the real customFieldDisplay formatting runs.
const activeFields: CustomField[] = [];
vi.mock("./customfields.form", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./customfields.form")>();
  return {
    ...actual,
    useObjectCustomFields: (): ObjectCustomFields => ({
      fields: activeFields,
      formFields: [],
      recordSlice: () => ({}),
      toBody: () => ({}),
    }),
  };
});

function field(overrides: Partial<CustomField>): CustomField {
  return {
    id: "cf-1",
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

afterEach(() => {
  cleanup();
  activeFields.length = 0;
});

function render(ui: ReactNode) {
  return rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

describe("CustomFieldsCard", () => {
  it("shows each field's label and formatted value", () => {
    activeFields.push(
      field({
        label: "Budget ceiling",
        column_name: "cf_budget_ceiling",
        type: "currency",
        currency: "EUR",
      }),
      field({
        label: "Procurement route",
        column_name: "cf_procurement_route",
        type: "picklist",
      }),
    );
    render(
      <CustomFieldsCard
        object="deal"
        record={{ cf_budget_ceiling: 500000, cf_procurement_route: "Reseller" }}
      />,
    );
    expect(screen.getByText("Budget ceiling")).toBeTruthy();
    expect(screen.getByText("€5,000.00")).toBeTruthy();
    expect(screen.getByText("Procurement route")).toBeTruthy();
    expect(screen.getByText("Reseller")).toBeTruthy();
  });

  it("omits a field the record has no value for (evidence-or-omit)", () => {
    activeFields.push(
      field({
        label: "Budget ceiling",
        column_name: "cf_ceiling",
        type: "text",
      }),
      field({ label: "Empty note", column_name: "cf_note", type: "text" }),
    );
    render(
      <CustomFieldsCard
        object="deal"
        record={{ cf_ceiling: "set", cf_note: null }}
      />,
    );
    expect(screen.getByText("Budget ceiling")).toBeTruthy();
    expect(screen.queryByText("Empty note")).toBeNull();
  });

  it("renders nothing when no field has a value", () => {
    activeFields.push(
      field({ label: "Note", column_name: "cf_note", type: "text" }),
    );
    const { container } = render(
      <CustomFieldsCard object="deal" record={{ cf_note: "" }} />,
    );
    expect(container.querySelector(".card")).toBeNull();
  });

  it("makes a value that is a web address followable, away from the record", () => {
    activeFields.push(
      field({ label: "Wiki page", column_name: "cf_wiki", type: "text" }),
    );
    render(
      <CustomFieldsCard
        object="deal"
        record={{ cf_wiki: "https://wiki.example.com/globex" }}
      />,
    );
    const link = screen.getByRole("link", {
      name: "https://wiki.example.com/globex",
    });
    expect(link.getAttribute("href")).toBe("https://wiki.example.com/globex");
    // A foreign origin nobody here vouched for: a new tab, and neither the
    // referrer nor a handle on the window that opened it.
    expect(link.getAttribute("target")).toBe("_blank");
    const rel = link.getAttribute("rel") ?? "";
    expect(rel.split(" ")).toContain("noopener");
    expect(rel.split(" ")).toContain("noreferrer");
  });

  it("leaves a value that is not a web address as plain text", () => {
    activeFields.push(
      field({ label: "Route", column_name: "cf_route", type: "text" }),
      field({ label: "Host", column_name: "cf_host", type: "text" }),
      field({ label: "Script", column_name: "cf_script", type: "text" }),
    );
    render(
      <CustomFieldsCard
        object="deal"
        record={{
          cf_route: "Reseller",
          cf_host: "example.com",
          cf_script: "javascript:alert(1)",
        }}
      />,
    );
    // Each value is still SHOWN — the reader loses the link, never the value —
    // and none of the three became an href.
    expect(screen.getByText("Reseller")).toBeTruthy();
    expect(screen.getByText("example.com")).toBeTruthy();
    expect(screen.getByText("javascript:alert(1)")).toBeTruthy();
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });
});
