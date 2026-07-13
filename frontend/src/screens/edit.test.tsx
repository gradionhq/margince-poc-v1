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
import { EditAction } from "./edit";

// The shared edit-record form (the mirror of create): a record prefills the
// form, submit carries only the typed values (the screen attaches ifMatch),
// and a rejected update renders its detail verbatim — same contract as create.

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

const record = { id: "p1", version: 3, full_name: "Alice" };
const fields = [
  { key: "full_name", label: "create.fullName" as const, required: true },
];

describe("edit record flow", () => {
  it("prefills the form from the record", async () => {
    render(
      <EditAction
        label="Edit"
        fields={fields}
        record={record}
        update={vi.fn(async () => record)}
        invalidate="people"
        recordKey="person"
      />,
    );
    await userEvent.click(screen.getByTestId("edit-record"));
    expect(
      (screen.getByLabelText("Full name *") as HTMLInputElement).value,
    ).toBe("Alice");
  });

  it("submits only the typed values", async () => {
    const update = vi.fn(async (_values: Record<string, unknown>) => record);
    render(
      <EditAction
        label="Edit"
        fields={fields}
        record={record}
        update={update}
        invalidate="people"
        recordKey="person"
      />,
    );
    await userEvent.click(screen.getByTestId("edit-record"));
    const input = screen.getByLabelText("Full name *");
    await userEvent.clear(input);
    await userEvent.type(input, "Alice M");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalledTimes(1));
    expect(update.mock.calls[0][0]).toEqual({ full_name: "Alice M" });
  });

  it("renders the rejected update's detail verbatim", async () => {
    const update = vi.fn(async () => {
      throw new Error("name too long");
    });
    render(
      <EditAction
        label="Edit"
        fields={fields}
        record={record}
        update={update}
        invalidate="people"
        recordKey="person"
      />,
    );
    await userEvent.click(screen.getByTestId("edit-record"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText("name too long")).toBeTruthy());
  });
});
