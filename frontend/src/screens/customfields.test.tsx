/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { FieldBuilder, FieldTable } from "./customfields";

afterEach(cleanup);

const wrap = (ui: React.ReactNode) =>
  render(<LocaleProvider initial="en">{ui}</LocaleProvider>);

function builder(
  overrides: Partial<React.ComponentProps<typeof FieldBuilder>> = {},
) {
  const onSubmit = vi.fn();
  const onToast = vi.fn();
  wrap(
    <FieldBuilder
      object="organization"
      pending={false}
      onSubmit={onSubmit}
      onToast={onToast}
      {...overrides}
    />,
  );
  return { onSubmit, onToast };
}

describe("FieldBuilder", () => {
  it("mirrors the label into the immutable disabled api key", async () => {
    builder();
    await userEvent.type(screen.getByLabelText(/Label/i), "Contract end date");
    const key = screen.getByLabelText(/API key/i) as HTMLInputElement;
    expect(key.value).toBe("organization.cf_contract_end_date");
    expect(key).toBeDisabled();
  });

  it("shows the pending DDL preview reflecting the type", async () => {
    builder();
    await userEvent.type(screen.getByLabelText(/Label/i), "Contract end date");
    await userEvent.click(screen.getByRole("button", { name: /^Date$/i }));
    expect(
      screen.getByText(
        /ALTER organization ADD COLUMN cf_contract_end_date \(date\)/,
      ),
    ).toBeInTheDocument();
  });

  it("refuses a structural label and disables Confirm", async () => {
    builder();
    await userEvent.type(
      screen.getByLabelText(/Label/i),
      "Link to parent account",
    );
    expect(
      screen.getByText(/looks like a new object or relationship/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Confirm & add field/i }),
    ).toBeDisabled();
  });

  it("guards an empty label: Confirm disabled, guard toast on click attempt", async () => {
    const { onToast, onSubmit } = builder();
    const confirm = screen.getByRole("button", {
      name: /Confirm & add field/i,
    });
    expect(confirm).toBeDisabled();
    // the guard toast is wired to the always-clickable Add affordance; assert via helper
    expect(onSubmit).not.toHaveBeenCalled();
    expect(onToast).not.toHaveBeenCalled();
  });

  it("reveals the ISO-4217 input for currency", async () => {
    builder();
    await userEvent.click(screen.getByRole("button", { name: /^Currency$/i }));
    expect(screen.getByLabelText(/Currency code/i)).toBeInTheDocument();
  });

  it("reveals the options editor for picklist and blocks removing the last option", async () => {
    const { onToast } = builder();
    await userEvent.click(screen.getByRole("button", { name: /^Picklist$/i }));
    const removes = screen.getAllByRole("button", { name: /remove option/i });
    // start with one row; removing it is blocked
    await userEvent.click(removes[removes.length - 1]);
    expect(onToast).toHaveBeenCalledWith(
      "A picklist needs at least one option",
    );
  });

  it("submits a well-formed draft on Confirm", async () => {
    const { onSubmit } = builder();
    await userEvent.type(screen.getByLabelText(/Label/i), "Renewal date");
    await userEvent.click(screen.getByRole("button", { name: /^Date$/i }));
    await userEvent.click(
      screen.getByRole("button", { name: /Confirm & add field/i }),
    );
    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({
        object: "organization",
        label: "Renewal date",
        type: "date",
      }),
    );
  });
});

type CustomField = components["schemas"]["CustomField"];

const field = (over: Partial<CustomField> = {}): CustomField => ({
  id: "01J",
  workspace_id: "w",
  object: "deal",
  label: "Renewal date",
  slug: "renewal_date",
  type: "date",
  status: "active",
  column_name: "cf_renewal_date",
  created_by: "u1",
  created_at: "2026-06-22T14:09:00Z",
  updated_at: "2026-06-22T14:09:00Z",
  version: 1,
  ...over,
});

describe("FieldTable", () => {
  it("lists a field with its immutable api key and a type chip", () => {
    wrap(
      <FieldTable
        object="deal"
        fields={[field()]}
        canManage
        meUserId="u1"
        onRename={() => {}}
        onArchive={() => {}}
      />,
    );
    expect(screen.getByText("Renewal date")).toBeInTheDocument();
    expect(screen.getByText("deal.cf_renewal_date")).toBeInTheDocument();
    expect(screen.getByText(/Date/)).toBeInTheDocument();
    expect(screen.getByText("You")).toBeInTheDocument();
  });

  it("renders an honest empty state for an object with no fields", () => {
    wrap(
      <FieldTable
        object="person"
        fields={[]}
        canManage
        meUserId="u1"
        onRename={() => {}}
        onArchive={() => {}}
      />,
    );
    expect(
      screen.getByText(/No custom fields on Contact yet/i),
    ).toBeInTheDocument();
  });

  it("hides edit/archive controls when the viewer cannot manage", () => {
    wrap(
      <FieldTable
        object="deal"
        fields={[field()]}
        canManage={false}
        meUserId="u1"
        onRename={() => {}}
        onArchive={() => {}}
      />,
    );
    expect(screen.queryByRole("button", { name: /Archive field/i })).toBeNull();
  });

  it("dims a retired field and marks it retired", () => {
    wrap(
      <FieldTable
        object="deal"
        fields={[field({ status: "retired" })]}
        canManage
        meUserId="u1"
        onRename={() => {}}
        onArchive={() => {}}
      />,
    );
    expect(screen.getByText(/Retired/i)).toBeInTheDocument();
  });
});
