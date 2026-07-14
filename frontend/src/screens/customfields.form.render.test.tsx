/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import type { CreateField } from "./create";
import {
  type CustomField,
  customFieldsToBody,
  customFieldToFormField,
} from "./customfields.form";
import { EditAction } from "./edit";

// The custom-field form controls compose with the generic record-edit machinery:
// a workspace field renders under a divider, prefills from the record (currency
// converts minor→major), and its edited value coerces back to the stored type on
// save. Proven here through EditAction (the real create/edit path).

afterEach(() => {
  cleanup();
});

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

const ceilingCf: CustomField = {
  id: "cf-1",
  workspace_id: "ws-1",
  object: "deal",
  label: "Budget ceiling",
  slug: "budget_ceiling",
  type: "currency",
  status: "active",
  column_name: "cf_budget_ceiling",
  currency: "EUR",
  created_by: "u1",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const divider: CreateField = {
  key: "__cf_divider__",
  labelText: "Custom fields",
  divider: true,
};

const coreField: CreateField = {
  key: "name",
  label: "create.dealName",
  required: true,
};

describe("custom fields on the record edit form", () => {
  it("renders the divider + field, prefills major units, and coerces on save", async () => {
    const update = vi.fn(async (_values: Record<string, unknown>) => ({
      id: "d1",
    }));
    render(
      <EditAction
        label="Edit"
        fields={[
          coreField,
          divider,
          customFieldToFormField(ceilingCf, { yes: "Yes", no: "No" }),
        ]}
        // bigint minor units on the wire; the form shows major units.
        record={{
          id: "d1",
          version: 2,
          name: "Globex",
          cf_budget_ceiling: 1250,
        }}
        update={update}
        invalidate="deals"
        recordKey="deal"
      />,
    );
    await userEvent.click(screen.getByTestId("edit-record"));

    // The divider heading sets the custom fields apart.
    expect(screen.getByText("Custom fields")).toBeTruthy();
    // Currency prefilled from 1250 minor units → "12.5" major units.
    const input = screen.getByLabelText("Budget ceiling") as HTMLInputElement;
    expect(input.value).toBe("12.5");

    await userEvent.clear(input);
    await userEvent.type(input, "20");
    await userEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(update).toHaveBeenCalled());
    // The screen coerces the edited major amount back to minor units.
    const submitted = update.mock.calls[0][0];
    expect(customFieldsToBody(submitted, [ceilingCf])).toEqual({
      cf_budget_ceiling: 2000,
    });
  });

  it("omits the field when no active custom fields exist", async () => {
    render(
      <EditAction
        label="Edit"
        fields={[coreField]}
        record={{ id: "d1", version: 2, name: "Globex" }}
        update={vi.fn(async () => ({ id: "d1" }))}
        invalidate="deals"
        recordKey="deal"
      />,
    );
    await userEvent.click(screen.getByTestId("edit-record"));
    expect(screen.queryByText("Custom fields")).toBeNull();
  });
});
