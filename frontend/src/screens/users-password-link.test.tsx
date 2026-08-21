/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { UsersAdminCard } from "./users-admin";

// The admin-issued set-password link. What matters here is WHEN the action is
// offered: an installation that mails the link, or a member who could not
// redeem one, must not show a control whose only outcome is a refusal — and a
// link that fails to mint must not leave the admin believing the invite
// finished.

const LINK_URL = "https://crm.example.test/#/reset-password?token=raw-token";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const ROSTER = {
  data: [
    {
      id: "u-active",
      email: "ada@acme.test",
      display_name: "Ada Active",
      status: "active",
      is_agent: false,
    },
    {
      id: "u-off",
      email: "otto@acme.test",
      display_name: "Otto Off",
      status: "deactivated",
      is_agent: false,
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// backend serves an admin whose installation may or may not advertise the link
// action, and answers the mint with either a link or a failure.
function backend(opts: {
  adminPasswordLink: boolean;
  mintFails?: boolean;
  calls?: string[];
}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req =
      input instanceof Request ? input : new Request(String(input), init);
    if (req.url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "admin@acme.test" },
        roles: ["admin"],
        teams: [],
        admin_password_link: opts.adminPasswordLink,
      });
    }
    if (req.url.includes("/password-link")) {
      opts.calls?.push(req.url);
      if (opts.mintFails) {
        return jsonResponse(
          { title: "Refused", detail: "no public base URL is configured" },
          409,
        );
      }
      return jsonResponse(
        { set_password_url: LINK_URL, expires_at: "2026-08-12T09:00:00Z" },
        201,
      );
    }
    if (req.url.includes("/users") && req.method === "GET") {
      return jsonResponse(ROSTER);
    }
    return jsonResponse({ ...ROSTER.data[0], id: "u-new" }, 201);
  });
}

// StrictMode is not decoration here. An earlier cut of this screen fired the
// mint from a mount effect; StrictMode's double mount tore the request's
// observer down and the dialog hung on "Creating the link…" forever — broken on
// `make dev`, invisible to a suite that rendered without it. Rendering as the
// dev server does is what makes that class of defect reachable from a test.
const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return rtlRender(
    <StrictMode>
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">{ui}</LocaleProvider>
      </QueryClientProvider>
    </StrictMode>,
  );
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The invite form is a dialog the roster header's verb opens, so a case about
// what happens AFTER an invite has to open it first. The header verb names the
// whole act ("Invite a member") and the dialog's submit the bare one
// ("Invite"), which is what keeps the two tellable apart.
async function openInvite() {
  await userEvent.click(
    screen.getByRole("button", { name: /invite a member/i }),
  );
  return screen.findByRole("dialog");
}

// A member's verbs live behind their row's OverflowMenu, whose panel is
// portalled to the body and whose items are not rendered until it is first
// opened. So the link action is reached by opening that menu — and its ABSENCE
// has to be asserted with the menu open too, or the assertion passes on a menu
// nobody looked in.
async function rowMenu(name: string) {
  const row = screen.getByText(name).closest('[data-testid^="member-"]');
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no member row rendered for ${name}`);
  }
  const trigger = within(row).getByRole("button", {
    name: new RegExp(`actions for ${name}`, "i"),
  });
  // The trigger TOGGLES, so a helper that always clicks would shut a menu a
  // previous step left open — and the assertion after it would then be about a
  // panel with `hidden` on it rather than about what the row offers.
  if (trigger.getAttribute("aria-expanded") !== "true") {
    await userEvent.click(trigger);
  }
  const panelId = trigger.getAttribute("aria-controls");
  const panel = panelId === null ? null : document.getElementById(panelId);
  if (!(panel instanceof HTMLElement)) {
    throw new Error(`no actions menu rendered for ${name}`);
  }
  return within(panel);
}

// The one verb every case below reaches for, on the one member who can redeem
// it.
async function clickLinkAction(name = "Ada Active") {
  await userEvent.click(
    (await rowMenu(name)).getByRole("button", {
      name: /get set-password link/i,
    }),
  );
}

describe("admin-issued set-password link", () => {
  it("offers no link action where the installation mails the link", async () => {
    vi.stubGlobal("fetch", backend({ adminPasswordLink: false }));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());
    // Where email works the invite carries the link, so this control would only
    // ever 409 — an admin must not be shown it at all. Asserted with Ada's menu
    // OPEN: a closed menu renders none of its items, so the same query against a
    // shut one would pass whatever the installation can do.
    const verbs = await rowMenu("Ada Active");
    expect(
      verbs.queryByRole("button", { name: /set-password link/i }),
    ).toBeNull();
    expect(verbs.getByRole("button", { name: /deactivate/i })).toBeTruthy();
  });

  it("offers the action only on active members", async () => {
    vi.stubGlobal("fetch", backend({ adminPasswordLink: true }));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Otto Off")).toBeTruthy());
    // Ada's menu carries it and Otto's does not — he is deactivated and could
    // not redeem a link, so offering him one would hand over a link that is
    // dead on arrival.
    expect(
      (await rowMenu("Ada Active")).getByRole("button", {
        name: /get set-password link/i,
      }),
    ).toBeTruthy();
    expect(
      (await rowMenu("Otto Off")).queryByRole("button", {
        name: /get set-password link/i,
      }),
    ).toBeNull();
  });

  it("shows the minted link with its expiry", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", backend({ adminPasswordLink: true, calls }));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await clickLinkAction();
    const field =
      await screen.findByLabelText<HTMLInputElement>("Set-password link");
    expect(field.value).toBe(LINK_URL);
    expect(
      calls.some((url) => url.includes("/users/u-active/password-link")),
    ).toBe(true);
    // The expiry is shown, so the admin can say how long the member has.
    expect(screen.getByText(/expires/i)).toBeTruthy();
  });

  it("hands the admin a link as soon as an invite succeeds", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", backend({ adminPasswordLink: true, calls }));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await openInvite();
    await userEvent.type(
      screen.getByLabelText(/new member's email/i),
      "newbie@acme.test",
    );
    await userEvent.type(
      screen.getByLabelText(/new member's full name/i),
      "New Bie",
    );
    await userEvent.click(screen.getByRole("button", { name: /^invite$/i }));

    // Without this the admin walks away from a successful invite holding
    // nothing, and the member can never sign in — the whole defect.
    const field =
      await screen.findByLabelText<HTMLInputElement>("Set-password link");
    expect(field.value).toBe(LINK_URL);
    expect(
      calls.some((url) => url.includes("/users/u-new/password-link")),
    ).toBe(true);
  });

  it("leaves a post-invite mint failure visible with a retry", async () => {
    vi.stubGlobal(
      "fetch",
      backend({ adminPasswordLink: true, mintFails: true }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await openInvite();
    await userEvent.type(
      screen.getByLabelText(/new member's email/i),
      "newbie@acme.test",
    );
    await userEvent.type(
      screen.getByLabelText(/new member's full name/i),
      "New Bie",
    );
    await userEvent.click(screen.getByRole("button", { name: /^invite$/i }));

    // The member exists but has no way in. Reporting a clean success here is
    // the exact silent failure this feature was built to remove.
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("button", { name: /try again/i })).toBeTruthy();
  });

  it("reports a copy failure instead of throwing where the clipboard API is absent", async () => {
    vi.stubGlobal("fetch", backend({ adminPasswordLink: true }));
    // navigator.clipboard is undefined outside a secure context — and an
    // email-less installation served over plain http is exactly that.
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await clickLinkAction();
    await screen.findByLabelText("Set-password link");
    await userEvent.click(screen.getByRole("button", { name: /copy link/i }));
    // The admin is told to copy by hand rather than left with a dead button.
    expect(
      await screen.findByText(/could not copy automatically/i),
    ).toBeTruthy();
  });

  it("recovers from a transport failure instead of hanging on pending", async () => {
    // An HTTP refusal arrives as `error`; only a network failure rejects. An
    // uncaught rejection leaves the dialog on "Creating the link…" forever,
    // with no way to tell a dead connection from a slow server.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
            admin_password_link: true,
          });
        }
        if (req.url.includes("/password-link")) {
          throw new TypeError("Failed to fetch");
        }
        return jsonResponse(ROSTER);
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await clickLinkAction();
    expect(await screen.findByText(/could not reach the server/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: /try again/i })).toBeTruthy();
    expect(screen.queryByText(/creating the link/i)).toBeNull();
  });

  it("does not let an earlier request for the same member clobber a later one", async () => {
    // Reopening the SAME member makes two requests whose member id is
    // identical, so keying acceptance on the id alone would let the first one's
    // outcome land on the second's dialog — clearing a valid link, or reporting
    // an offline error that never happened to the request being shown.
    let call = 0;
    let releaseFirst: () => void = () => {};
    const firstFailed = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
            admin_password_link: true,
          });
        }
        if (req.url.includes("/password-link")) {
          call += 1;
          if (call === 1) {
            // Loses the race: rejects only after the second request is issued.
            await firstFailed;
            throw new TypeError("Failed to fetch");
          }
          return jsonResponse(
            { set_password_url: LINK_URL, expires_at: "2026-08-12T09:00:00Z" },
            201,
          );
        }
        return jsonResponse(ROSTER);
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const open = () => clickLinkAction();
    await open();
    await userEvent.click(screen.getByRole("button", { name: /done/i }));
    await open();
    const field =
      await screen.findByLabelText<HTMLInputElement>("Set-password link");
    expect(field.value).toBe(LINK_URL);

    // The stale failure lands now. It must change nothing.
    releaseFirst();
    await waitFor(() => expect(call).toBe(2));
    expect(screen.queryByText(/could not reach the server/i)).toBeNull();
    expect(
      screen.getByLabelText<HTMLInputElement>("Set-password link").value,
    ).toBe(LINK_URL);
  });

  it("keeps a failed mint visible with a retry rather than reporting success", async () => {
    vi.stubGlobal(
      "fetch",
      backend({ adminPasswordLink: true, mintFails: true }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await clickLinkAction();
    // The failure is announced, and the way out is offered. Silently closing
    // here would leave an account nobody can sign into and no visible sign of it.
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("button", { name: /try again/i })).toBeTruthy();
    expect(screen.queryByLabelText("Set-password link")).toBeNull();
  });
});
