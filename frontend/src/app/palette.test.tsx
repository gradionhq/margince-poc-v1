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
import { meFixture } from "./mefixture";
import {
  ASK_QUERY_KEY,
  type Command,
  CommandPalette,
  paletteHotkeyCaps,
  useBuiltinCommands,
} from "./palette";

// B-EP09.5 (AC-shell-3..7) and RS-1 (live /search records + see-all)
// acceptance. B-EP09.6 (AC-shell-8) covered the record-scoped Ask composer,
// which the agent surfaces that carried it no longer offer.

afterEach(() => {
  cleanup();
  window.location.hash = "";
  sessionStorage.clear();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function wrap(ui: ReactNode, client: QueryClient) {
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>
  );
}

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const view = rtlRender(wrap(ui, client));
  return {
    ...view,
    rerender: (next: ReactNode) => view.rerender(wrap(next, client)),
  };
};

const commands: Command[] = [
  {
    id: "screen:deals",
    label: "Pipeline",
    keywords: ["deals"],
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

// The palette answers to both modifiers, but the affordance may advertise only
// one, and ⌘ names a key a Windows keyboard does not have.
describe("paletteHotkeyCaps", () => {
  it("names the modifier the platform actually has", () => {
    expect(paletteHotkeyCaps("MacIntel")).toEqual(["⌘", "K"]);
    expect(paletteHotkeyCaps("iPhone")).toEqual(["⌘", "K"]);
    expect(paletteHotkeyCaps("Win32")).toEqual(["Ctrl", "K"]);
    expect(paletteHotkeyCaps("Linux x86_64")).toEqual(["Ctrl", "K"]);
  });

  // An unreported platform is far more likely to be Windows or Linux than a Mac,
  // and Ctrl is the modifier that works on both.
  it("falls back to Ctrl when the platform is unknown", () => {
    expect(paletteHotkeyCaps("")).toEqual(["Ctrl", "K"]);
  });

  // One cap per key, and never a string a caller has to take apart again: the
  // surface that draws these drew them by splitting "⌘K" with a lookbehind regex,
  // which does not parse at all on an engine without lookbehind.
  it("hands back one entry per key, so no caller has to split a string", () => {
    for (const platform of ["MacIntel", "Win32", ""]) {
      expect(paletteHotkeyCaps(platform)).toHaveLength(2);
    }
  });
});

describe("CommandPalette (AC-shell-3/4/5/6)", () => {
  it("shows the default command list with type tags, focuses the input", () => {
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    expect(document.activeElement).toBe(screen.getByRole("textbox"));
    expect(screen.getByText("Pipeline")).toBeTruthy();
    expect(screen.getByText("Record")).toBeTruthy(); // type tag rendered
  });

  // A nav label is a presentation choice; the domain word outlives it. Typing
  // "deals" has to reach Pipeline, or renaming a destination quietly removes it
  // from the palette for everyone who knows it by its older name.
  it("matches a keyword the row does not display, without showing it", async () => {
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    await userEvent.type(screen.getByRole("textbox"), "deals");
    const rows = screen.getAllByRole("button");
    expect(rows[0].textContent).toContain("Pipeline");
    expect(rows[0].textContent).not.toContain("deals");
    await userEvent.keyboard("{Enter}");
    expect(window.location.hash).toBe("#/deals");
  });

  it("filters by label+subtitle case-insensitively and appends the see-all + Ask-AI rows last", async () => {
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    await userEvent.type(screen.getByRole("textbox"), "COMPANY");
    const rows = screen.getAllByRole("button");
    expect(rows).toHaveLength(3);
    expect(rows[0].textContent).toContain("Brandt Automotive");
    expect(rows[1].textContent).toContain("See all results");
    expect(rows[2].textContent).toContain("Ask AI");
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
    // rows are [see-all, ask-ai] here (no builtin/record matches): step past
    // the see-all row to reach Ask-AI.
    await userEvent.keyboard("{ArrowDown}");
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
      <CommandPalette open={false} onClose={onClose} commands={commands} />,
    );
    view.rerender(
      <CommandPalette open onClose={onClose} commands={commands} />,
    );
    expect((screen.getByRole("textbox") as HTMLInputElement).value).toBe("");
  });

  it("surfaces live record hits from /search plus a see-all row (RS-1)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [{ type: "person", id: "p1", title: "Dana Buyer at Acme" }],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    await userEvent.type(screen.getByRole("textbox"), "acme");
    await waitFor(() =>
      expect(screen.getByText("Dana Buyer at Acme")).toBeTruthy(),
    );
    expect(screen.getByText("See all results for “acme”")).toBeTruthy();

    await userEvent.click(screen.getByText("Dana Buyer at Acme"));
    expect(window.location.hash).toBe("#/contacts/p1");
  });

  // A project hit carries no snippet, and "project" under two projects both
  // called Rollout tells them apart by nothing. The row reads the project
  // itself for its key, and falls back to the company for a project without
  // one; the hit routes to the project page either way.
  it("routes a project hit to its page, with the key or the company as its line", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.includes("/search")) {
          return jsonResponse({
            data: [
              { type: "project", id: "pr-1", title: "Rollout", snippet: null },
              { type: "project", id: "pr-2", title: "Rollout", snippet: null },
            ],
            page: { next_cursor: null, has_more: false },
          });
        }
        if (url.endsWith("/projects/pr-1")) {
          return jsonResponse({ id: "pr-1", name: "Rollout", key: "ACME-CRM" });
        }
        if (url.endsWith("/projects/pr-2")) {
          return jsonResponse({
            id: "pr-2",
            name: "Rollout",
            key: null,
            organization_id: "o-9",
          });
        }
        if (url.endsWith("/organizations/o-9")) {
          return jsonResponse({ id: "o-9", display_name: "Brandt Automotive" });
        }
        return jsonResponse({ data: [], page: { next_cursor: null } });
      }),
    );
    render(<CommandPalette open onClose={() => {}} commands={commands} />);
    await userEvent.type(screen.getByRole("textbox"), "roll");
    expect(await screen.findByText("ACME-CRM")).toBeTruthy();
    expect(await screen.findByText("Brandt Automotive")).toBeTruthy();
    expect(screen.queryByText("project")).toBeNull();

    await userEvent.click(screen.getByText("ACME-CRM"));
    expect(window.location.hash).toBe("#/projects/pr-1");
  });
});

// The builtin set is DERIVED from the rail's destinations, so a screen with a
// rail row is a ⌘K command with no registration of its own — and a screen with
// neither is reachable only by typing its hash. That derivation is what these
// assert, end to end: the word a reader types, and the address they land on.
describe("useBuiltinCommands", () => {
  function Probe() {
    return (
      <CommandPalette open onClose={() => {}} commands={useBuiltinCommands()} />
    );
  }

  // A /me with no grant at all, which is the harder case: the two settings
  // shortcuts drop out, so anything still offered here is offered because the
  // rail names it rather than because the principal holds something.
  function renderProbe() {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(meFixture({ roles: [], allow: {} }))),
    );
    return render(<Probe />);
  }

  it("reaches the filter builder by the name the screen prints", async () => {
    const user = userEvent.setup();
    renderProbe();
    // Its own title, not a fourth spelling of it: "views" appears in no other
    // command, so matching on it proves the row carries the screen's own words.
    await user.type(screen.getByRole("textbox"), "views");
    const rows = screen.getAllByRole("button");
    expect(rows[0].textContent).toContain("Filters & views");
    await user.keyboard("{Enter}");
    expect(window.location.hash).toBe("#/filters");
  });

  it("reaches it by its route id too, so the domain word finds it", async () => {
    const user = userEvent.setup();
    renderProbe();
    await user.type(screen.getByRole("textbox"), "filters");
    await user.keyboard("{Enter}");
    expect(window.location.hash).toBe("#/filters");
  });
});
