/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
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
      workspace_id: "ws-1",
      email: "ada@acme.test",
      display_name: "Ada Active",
      status: "active",
      is_agent: false,
    },
    {
      id: "u-off",
      workspace_id: "ws-1",
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

describe("admin-issued set-password link", () => {
  it("offers no link action where the installation mails the link", async () => {
    vi.stubGlobal("fetch", backend({ adminPasswordLink: false }));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());
    // Where email works the invite carries the link, so this control would only
    // ever 409 — an admin must not be shown it at all.
    expect(
      screen.queryByRole("button", { name: /set-password link/i }),
    ).toBeNull();
  });

  it("offers the action only on active members", async () => {
    vi.stubGlobal("fetch", backend({ adminPasswordLink: true }));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Otto Off")).toBeTruthy());
    // One button, for Ada — Otto is deactivated and could not redeem a link,
    // so offering him one would hand over a link that is dead on arrival.
    const actions = screen.getAllByRole("button", {
      name: /get set-password link/i,
    });
    expect(actions).toHaveLength(1);
  });

  it("shows the minted link with its expiry", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", backend({ adminPasswordLink: true, calls }));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await userEvent.click(
      screen.getByRole("button", { name: /get set-password link/i }),
    );
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

    await userEvent.type(
      screen.getByLabelText("New member's email"),
      "newbie@acme.test",
    );
    await userEvent.type(
      screen.getByLabelText("New member's full name"),
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

    await userEvent.type(
      screen.getByLabelText("New member's email"),
      "newbie@acme.test",
    );
    await userEvent.type(
      screen.getByLabelText("New member's full name"),
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

    await userEvent.click(
      screen.getByRole("button", { name: /get set-password link/i }),
    );
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

    await userEvent.click(
      screen.getByRole("button", { name: /get set-password link/i }),
    );
    expect(await screen.findByText(/could not reach the server/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: /try again/i })).toBeTruthy();
    expect(screen.queryByText(/creating the link/i)).toBeNull();
  });

  it("keeps a failed mint visible with a retry rather than reporting success", async () => {
    vi.stubGlobal(
      "fetch",
      backend({ adminPasswordLink: true, mintFails: true }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await userEvent.click(
      screen.getByRole("button", { name: /get set-password link/i }),
    );
    // The failure is announced, and the way out is offered. Silently closing
    // here would leave an account nobody can sign into and no visible sign of it.
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("button", { name: /try again/i })).toBeTruthy();
    expect(screen.queryByLabelText("Set-password link")).toBeNull();
  });
});
