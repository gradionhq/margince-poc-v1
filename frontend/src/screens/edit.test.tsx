/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useLayoutEffect, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "../design-system/atoms";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
import { throwProblem } from "./common";
import { EditAction, EditRecordModal } from "./edit";

// The shared edit-record form (the mirror of create): a record prefills the
// form, submit carries only the typed values (the screen attaches ifMatch),
// and a rejected update renders the server's own detail — while a failure that
// is not a server refusal never puts its own words on screen.

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

  // The prefill's timing, not just its result: an edit that silently drops
  // what the user typed is the bug this pins.
  const twoFields = [
    { key: "full_name", label: "create.fullName" as const, required: true },
    { key: "title", label: "create.personTitle" as const },
  ];
  const twoFieldRecord = {
    id: "p1",
    version: 3,
    full_name: "Alice",
    title: "CTO",
  };

  it("is prefilled in the very commit that puts the form on screen", async () => {
    // What the Full name input holds the moment the open modal reaches the
    // DOM. A layout effect runs inside that commit — after the DOM is
    // updated, before the browser paints and before any passive effect — so
    // it sees precisely the first frame a user could see and type into.
    const firstFrame: string[] = [];
    function OpenHarness() {
      const [open, setOpen] = useState(false);
      useLayoutEffect(() => {
        const input = screen.queryByLabelText(
          "Full name *",
        ) as HTMLInputElement | null;
        if (input) {
          firstFrame.push(input.value);
        }
      });
      return (
        <>
          <Button small onClick={() => setOpen(true)}>
            Open
          </Button>
          <EditRecordModal
            open={open}
            onClose={() => setOpen(false)}
            title="Edit"
            fields={twoFields}
            record={twoFieldRecord}
            pending={false}
            error={null}
            onSubmit={vi.fn()}
          />
        </>
      );
    }
    render(<OpenHarness />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    // Prefilling in a passive effect puts the form on screen blank and fills
    // it a commit later; that gap is the window a keystroke lands in and gets
    // written through empty form state.
    expect(firstFrame).toEqual(["Alice"]);
  });

  it("renders the server's own detail for a rejected update", async () => {
    const update = vi.fn(async () => {
      throwProblem({ status: 422, detail: "name too long" });
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

  it("never shows the words of a failure the server did not send", async () => {
    const update = vi.fn(async () => {
      throw new TypeError("Cannot read properties of undefined (reading 'id')");
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
    await waitFor(() =>
      expect(screen.getByText(en["common.errorNoCause"])).toBeTruthy(),
    );
    expect(screen.queryByText(/Cannot read properties/)).toBeNull();
  });
});
