/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { AskFab } from "./fab";
import { ASK_QUERY_KEY, type Command, CommandPalette } from "./palette";

// B-EP09.5 (AC-shell-3..7) and B-EP09.6 (AC-shell-8) acceptance.

afterEach(() => {
  cleanup();
  window.location.hash = "";
  sessionStorage.clear();
});

const render = (ui: ReactNode) =>
  rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);

const commands: Command[] = [
  {
    id: "screen:deals",
    label: "Deals",
    type: "screen",
    route: { screen: "deals" },
  },
  {
    id: "action:new-deal",
    label: "New deal",
    type: "action",
    route: { screen: "deals", id: "new" },
  },
  {
    id: "record:brandt",
    label: "Brandt Automotive",
    subtitle: "Company",
    type: "record",
    route: { screen: "companies", id: "brandt" },
  },
];

describe("CommandPalette (AC-shell-3/4/5/6)", () => {
  it("shows the default command list with type tags, focuses the input", () => {
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    expect(document.activeElement).toBe(screen.getByRole("textbox"));
    expect(screen.getByText("Deals")).toBeTruthy();
    expect(screen.getByText("Record")).toBeTruthy(); // type tag rendered
  });

  it("filters by label+subtitle case-insensitively and appends the Ask-AI row last", async () => {
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    await userEvent.type(screen.getByRole("textbox"), "COMPANY");
    const rows = screen.getAllByRole("button");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("Brandt Automotive");
    expect(rows[1].textContent).toContain("Ask AI");
  });

  it("Enter runs the selection; arrows move and clamp (AC-shell-5)", async () => {
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    const input = screen.getByRole("textbox");
    await userEvent.keyboard("{ArrowUp}"); // clamps at 0
    await userEvent.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}{ArrowDown}"); // clamps at end
    await userEvent.keyboard("{ArrowUp}{ArrowUp}"); // back to index 0
    expect(input).toBeTruthy();
    await userEvent.keyboard("{Enter}");
    expect(window.location.hash).toBe("#/deals");
  });

  it("the Ask-AI row stores the query and lands on the AI surface (AC-shell-4)", async () => {
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    await userEvent.type(screen.getByRole("textbox"), "zzz nothing matches");
    await userEvent.keyboard("{Enter}");
    expect(window.location.hash).toBe("#/ai");
    expect(sessionStorage.getItem(ASK_QUERY_KEY)).toBe("zzz nothing matches");
  });

  it("Esc closes; opening clears the previous query (AC-shell-3)", async () => {
    const onClose = vi.fn();
    const view = render(
      <CommandPalette open onClose={onClose} commands={commands} />,
    );
    await userEvent.type(screen.getByRole("textbox"), "deal");
    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalled();
    view.rerender(
      <LocaleProvider initial="en">
        <CommandPalette open={false} onClose={onClose} commands={commands} />
      </LocaleProvider>,
    );
    view.rerender(
      <LocaleProvider initial="en">
        <CommandPalette open onClose={onClose} commands={commands} />
      </LocaleProvider>,
    );
    expect((screen.getByRole("textbox") as HTMLInputElement).value).toBe("");
  });
});

describe("AskFab (AC-shell-8)", () => {
  it("mounts on core screens with the context label tracking the screen", async () => {
    render(<AskFab route={{ screen: "deals" }} />);
    await userEvent.click(
      screen.getByRole("button", { name: "Ask about this" }),
    );
    expect(screen.getByText("Ask about Deals")).toBeTruthy();
  });

  it("tracks the active record id when present", async () => {
    render(<AskFab route={{ screen: "companies", id: "brandt" }} />);
    await userEvent.click(
      screen.getByRole("button", { name: "Ask about this" }),
    );
    expect(screen.getByText("Ask about brandt")).toBeTruthy();
  });

  it("renders the load-bearing scope copy", async () => {
    render(<AskFab route={{ screen: "home" }} />);
    await userEvent.click(
      screen.getByRole("button", { name: "Ask about this" }),
    );
    expect(
      screen.getByText("Your agent reads only what you can see."),
    ).toBeTruthy();
  });

  it("is absent on the ai screen and on rail-less surfaces", () => {
    const { container } = render(<AskFab route={{ screen: "ai" }} />);
    expect(container.querySelector(".askfab")).toBeNull();
    const { container: book } = render(<AskFab route={{ screen: "book" }} />);
    expect(book.querySelector(".askfab")).toBeNull();
  });
});
