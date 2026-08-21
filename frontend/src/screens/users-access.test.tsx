/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
import { AccessPreviewPanel, TeamsCard } from "./users-access";

// The access preview and the teams roster are one file because they answer the
// same question from two sides: what a seat will reach, and the membership that
// decides it. Both are asserted against what the SERVER said — the screen never
// re-derives a role.

type Preview = {
  row_scope: "own" | "team" | "all";
  teams?: { id: string; name: string }[];
  objects?: Record<
    string,
    { read?: boolean; create?: boolean; update?: boolean; delete?: boolean }
  >;
  field_masks?: { object: string; field: string; condition: string }[];
};

type Team = { id: string; name: string; member_count?: number };

// Held in a constant rather than written inline: the prop is the seat's ROLE,
// and a literal here reads to the a11y lint as an ARIA role on an element.
const REP: Parameters<typeof AccessPreviewPanel>[0]["role"] = "rep";

type Call = { method: string; path: string; body: unknown };

function backend(opts: Readonly<{ preview?: Preview; teams?: Team[] }>) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      // Built as a Request rather than read off `init`: openapi-fetch may pass a
      // Request with no init at all, and a mock reading the method from init
      // would answer every write as if it were a read.
      const request =
        input instanceof Request ? input : new Request(String(input), init);
      const path = new URL(request.url, "http://localhost").pathname;
      calls.push({
        method: request.method,
        path,
        body:
          request.method === "GET" ? undefined : await request.clone().json(),
      });
      // The teams list answers as the contract answers: a page, with the
      // cursor of the next one. Without `page` the roster walk has nothing to
      // read the end of the list from.
      const body = path.endsWith("/users/access-preview")
        ? (opts.preview ?? { row_scope: "own" })
        : {
            data: opts.teams ?? [],
            page: { next_cursor: null, has_more: false },
          };
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      });
    },
  );
  return { fetchMock, calls };
}

function Providers({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("AccessPreviewPanel", () => {
  it("states the row scope, the verbs per object and every field mask the server named", async () => {
    const { fetchMock, calls } = backend({
      preview: {
        row_scope: "team",
        teams: [{ id: "t-1", name: "Nord" }],
        objects: {
          person: { read: true },
          deal: { read: true, update: true, delete: true },
        },
        field_masks: [
          { object: "deal", field: "amount", condition: "outside_scope" },
        ],
      },
    });
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <AccessPreviewPanel role={REP} teamIds={["t-1"]} />
      </Providers>,
    );

    await screen.findByText(en["users.access.identity"]);
    // The team the scope names, not just the word "team": a preview that lost
    // the roster would still read as a team scope.
    expect(
      screen.getByText(
        en["users.access.writesTeam"].replace("{teams}", "Nord"),
      ),
    ).toBeTruthy();
    // Read alone, and read·write·delete — the verbs are derived from the grant
    // the server returned rather than from the role's name.
    expect(
      screen.getByText(`${en["users.access.object.person"]}: read`),
    ).toBeTruthy();
    expect(
      screen.getByText(
        `${en["users.access.object.deal"]}: read · write · delete`,
      ),
    ).toBeTruthy();
    // An object the server said nothing about is "no access", not silence.
    expect(
      screen.getByText(
        `${en["users.access.object.project"]}: ${en["users.access.none"]}`,
      ),
    ).toBeTruthy();
    expect(
      screen.getByText(
        en["users.access.mask"]
          .replace("{field}", "deal.amount")
          .replace("{when}", en["users.access.maskOutside"]),
      ),
    ).toBeTruthy();
    // The role and the teams are what the server evaluates, so they have to
    // reach it.
    expect(calls[0]?.body).toEqual({ role: "rep", team_ids: ["t-1"] });
  });

  // A team scope with no team is a real posture — the seat is on no team yet —
  // and it must not read as if it edited every record.
  it("says a team scope with no team edits only their own records", async () => {
    const { fetchMock } = backend({
      preview: { row_scope: "team", teams: [] },
    });
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <AccessPreviewPanel role={REP} teamIds={[]} />
      </Providers>,
    );

    expect(
      await screen.findByText(en["users.access.writesTeamNone"]),
    ).toBeTruthy();
  });
});

describe("TeamsCard", () => {
  it("counts a team of one in the singular", async () => {
    const { fetchMock } = backend({
      teams: [
        { id: "t-1", name: "Nord", member_count: 1 },
        { id: "t-2", name: "Süd", member_count: 4 },
      ],
    });
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <TeamsCard />
      </Providers>,
    );

    await screen.findByText("Nord");
    expect(screen.getByText("1 member")).toBeTruthy();
    expect(screen.getByText("4 members")).toBeTruthy();
  });

  it("reads as empty rather than as a failed read when no team exists", async () => {
    const { fetchMock } = backend({ teams: [] });
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <TeamsCard />
      </Providers>,
    );

    expect(await screen.findByText(en["users.noTeamsYet"])).toBeTruthy();
  });

  it("archives the team the verb names", async () => {
    const user = userEvent.setup();
    const { fetchMock, calls } = backend({
      teams: [{ id: "t-1", name: "Nord", member_count: 2 }],
    });
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <TeamsCard />
      </Providers>,
    );

    await screen.findByText("Nord");
    await user.click(
      screen.getByRole("button", {
        name: en["users.archiveTeam"].replace("{name}", "Nord"),
      }),
    );

    await waitFor(() =>
      expect(calls.some((call) => call.method === "PATCH")).toBe(true),
    );
    const patch = calls.find((call) => call.method === "PATCH");
    expect(patch?.path).toContain("/teams/t-1");
    expect(patch?.body).toEqual({ archived: true });
  });

  it("creates a team through the dialog its title verb opens, trimming the name", async () => {
    const user = userEvent.setup();
    const { fetchMock, calls } = backend({ teams: [] });
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <TeamsCard />
      </Providers>,
    );

    await screen.findByText(en["users.noTeamsYet"]);
    await user.click(
      screen.getByRole("button", { name: en["users.newTeamOpen"] }),
    );
    const dialog = screen.getByRole("dialog");
    const submit = within(dialog).getByRole("button", {
      name: en["users.createTeam"],
    }) as HTMLButtonElement;
    // Nothing typed is nothing to create: the submit is inert until the field
    // holds a name.
    expect(submit.disabled).toBe(true);
    await user.type(
      within(dialog).getByLabelText(en["users.teamNameLabel"], {
        exact: false,
      }),
      "  Nord  ",
    );
    await user.click(submit);

    await waitFor(() =>
      expect(calls.some((call) => call.method === "POST")).toBe(true),
    );
    // Trimmed, because a team named with a trailing space is a team nobody can
    // find by typing its name.
    expect(calls.find((call) => call.method === "POST")?.body).toEqual({
      name: "Nord",
    });
    // The dialog closes on the write that landed.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
