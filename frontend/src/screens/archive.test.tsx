/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { ArchiveAction } from "./archive";
import { throwProblem } from "./common";

// The shared archive confirm. A refused archive leaves the dialog exactly as it
// was apart from one line of red text, so that line is the only thing that
// distinguishes "the server said no" from "it is still working".

afterEach(cleanup);

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

describe("ArchiveAction", () => {
  it("announces a refused archive with the server's own reason", async () => {
    render(
      <ArchiveAction
        label="Archive contact"
        confirmText="There is no undo control."
        archive={() =>
          throwProblem({ detail: "the record is under retention" })
        }
        invalidate="people"
        recordKey="person"
        onArchived={() => {
          throw new Error("a refused archive must not report success");
        }}
      />,
    );
    await userEvent.click(screen.getByTestId("archive-record"));
    await userEvent.click(screen.getByTestId("archive-confirm"));

    const announced = await screen.findByRole("alert");
    expect(announced.textContent).toBe("the record is under retention");
    // Still open, so the reader can read the reason and decide — a dialog that
    // closed on failure would take the only explanation with it.
    expect(screen.getByRole("dialog")).toBeTruthy();
  });
});
