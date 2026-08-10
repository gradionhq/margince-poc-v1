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
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { ShareScreen } from "./share";

// AS-3/4/5 — the record-share screen (A52/ADR-0039): list who has manual
// access to this one record, grant a new user/team subject, revoke an
// existing grant. Mirrors relationships.test.tsx's fetch-stub convention.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

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

const usersPage = {
  data: [
    { id: "u-1", display_name: "Priya Shah", email: "priya@example.com" },
    { id: "u-2", display_name: "Mor Adler", email: "mor@example.com" },
    // An agent seat — must NOT be offered as a share subject (spec §2.1).
    {
      id: "u-agent",
      display_name: "SDR Bot",
      email: "bot@example.com",
      is_agent: true,
    },
  ],
  page: { next_cursor: null, has_more: false },
};

const teamsPage = {
  data: [{ id: "t-1", name: "Deal Desk", member_count: 4 }],
  page: { next_cursor: null, has_more: false },
};

const existingGrant = {
  id: "g-1",
  record_type: "deal",
  record_id: "d-1",
  subject_type: "user" as const,
  subject_id: "u-2",
  access: "read" as const,
  granted_by: "u-1",
  reason: "compliance review",
  expires_at: null,
  created_at: "2026-06-22T14:08:00Z",
  version: 1,
};

function installBaseFetch(
  overrides: Record<
    string,
    (req: Request) => Response | Promise<Response>
  > = {},
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request =
        input instanceof Request ? input : new Request(String(input), init);
      for (const [match, handler] of Object.entries(overrides)) {
        if (request.url.includes(match)) {
          return handler(request);
        }
      }
      if (request.url.includes("/users")) return jsonResponse(usersPage);
      if (request.url.includes("/teams")) return jsonResponse(teamsPage);
      if (request.url.includes("/record-grants")) {
        return jsonResponse({
          data: [existingGrant],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({}, 404);
    }),
  );
}

describe("ShareScreen", () => {
  it("renders who-has-access, resolving subject and granter names via the roster", async () => {
    installBaseFetch();
    render(<ShareScreen recordType="deal" recordId="d-1" />);

    const aclList = await screen.findByTestId("share-acl-list");
    await waitFor(() =>
      expect(within(aclList).getByText("Mor Adler")).toBeTruthy(),
    );
    expect(within(aclList).getByText("compliance review")).toBeTruthy();
    await waitFor(() =>
      expect(within(aclList).getByText("Priya Shah")).toBeTruthy(),
    );
  });

  it("picking a seeded user + Read + submitting POSTs the grant body", async () => {
    let posted: unknown = null;
    installBaseFetch({
      "/record-grants": (request) => {
        if (request.method === "POST") {
          return request.json().then((body) => {
            posted = body;
            return jsonResponse(
              {
                id: "g-2",
                record_type: "deal",
                record_id: "d-1",
                subject_type: "user",
                subject_id: "u-1",
                access: "read",
                granted_by: "u-1",
                reason: body.reason ?? null,
                expires_at: body.expires_at ?? null,
                created_at: "2026-07-14T00:00:00Z",
                version: 1,
              },
              201,
            );
          });
        }
        return jsonResponse({
          data: [existingGrant],
          page: { next_cursor: null, has_more: false },
        });
      },
    });
    render(<ShareScreen recordType="deal" recordId="d-1" />);

    const pick = await screen.findByRole("button", { name: /Priya Shah/ });
    await userEvent.click(pick);

    const reasonBox = screen.getByLabelText(/reason/i);
    await userEvent.type(reasonBox, "Deal-desk review");

    const submit = screen.getByTestId("share-grant-submit");
    await userEvent.click(submit);

    await waitFor(() => expect(posted).not.toBeNull());
    expect(posted).toMatchObject({
      record_type: "deal",
      record_id: "d-1",
      subject_type: "user",
      subject_id: "u-1",
      access: "read",
      reason: "Deal-desk review",
    });
    expect((posted as Record<string, unknown>).expires_at).toBeDefined();
  });

  // The expiry control converts between a DAY COUNT (the screen's state) and the
  // strings a listbox speaks, and the wire carries neither: it carries a timestamp,
  // or nothing at all for a grant that lasts until it is revoked. Both directions
  // of that conversion are asserted, because the two failures look identical on
  // screen — a grant that silently never expires, and one that expires today.
  it("turns the chosen expiry into a timestamp, and no expiry into none", async () => {
    // Collected in an ARRAY rather than into a `let`: a variable only ever
    // assigned inside a `.then()` still reads as its initial `null` to the
    // compiler, so every field access off it narrows to `never`. An array keeps
    // its element type, and the two grants are two entries.
    const posts: Record<string, unknown>[] = [];
    const grant = (body: Record<string, unknown>) =>
      jsonResponse(
        {
          id: "g-3",
          record_type: "deal",
          record_id: "d-1",
          subject_type: "user",
          subject_id: "u-1",
          access: "read",
          granted_by: "u-1",
          reason: body.reason ?? null,
          expires_at: body.expires_at ?? null,
          created_at: "2026-07-14T00:00:00Z",
          version: 1,
        },
        201,
      );
    installBaseFetch({
      "/record-grants": (request) => {
        if (request.method === "POST") {
          return request.json().then((body) => {
            posts.push(body);
            return grant(body);
          });
        }
        return jsonResponse({
          data: [existingGrant],
          page: { next_cursor: null, has_more: false },
        });
      },
    });
    render(<ShareScreen recordType="deal" recordId="d-1" />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: /Priya Shah/ }));
    await pickOption(
      user,
      screen.getByLabelText("Expiry"),
      "Expires in 7 days",
    );
    await user.click(screen.getByTestId("share-grant-submit"));

    await waitFor(() => expect(posts).toHaveLength(1));
    // A real instant, seven days out — asserted as a distance rather than as a
    // literal, because the clock is the one thing a test cannot pin.
    const days =
      (Date.parse(String(posts[0].expires_at)) - Date.now()) / 86_400_000;
    expect(days).toBeGreaterThan(6.9);
    expect(days).toBeLessThan(7.1);

    // A granted subject is picked again for the second half: a successful grant
    // clears the form, which is what the reader sees, so the case has to walk the
    // same path they would rather than reusing a form that is no longer filled in.
    await user.click(await screen.findByRole("button", { name: /Priya Shah/ }));
    await pickOption(
      user,
      screen.getByLabelText("Expiry"),
      "No expiry (until revoked)",
    );
    await user.click(screen.getByTestId("share-grant-submit"));

    await waitFor(() => expect(posts).toHaveLength(2));
    // Absent, not an empty string and not epoch zero: a grant with no expiry is
    // one the wire says nothing about.
    expect(posts[1].expires_at).toBeUndefined();
  });

  it("excludes agent seats from the subject picker (spec §2.1)", async () => {
    installBaseFetch();
    render(<ShareScreen recordType="deal" recordId="d-1" />);

    // A human roster member is a selectable subject…
    await screen.findByRole("button", { name: /Priya Shah/ });
    // …but the agent seat (is_agent) is never offered as one.
    expect(screen.queryByRole("button", { name: /SDR Bot/ })).toBeNull();
    expect(screen.queryByText("SDR Bot")).toBeNull();
  });

  it("disables a subject who already has a grant on this record", async () => {
    installBaseFetch();
    render(<ShareScreen recordType="deal" recordId="d-1" />);

    const alreadyGranted = await screen.findByRole("button", {
      name: /Mor Adler/,
    });
    expect((alreadyGranted as HTMLButtonElement).disabled).toBe(true);
  });

  it("revoke on a row, confirmed, fires DELETE /record-grants/{id}", async () => {
    let deletedId: string | null = null;
    installBaseFetch({
      "/record-grants/g-1": (request) => {
        if (request.method === "DELETE") {
          deletedId = "g-1";
          return new Response(null, { status: 204 });
        }
        return jsonResponse({}, 404);
      },
    });
    render(<ShareScreen recordType="deal" recordId="d-1" />);

    const revokeBtn = await screen.findByTestId("revoke-grant");
    await userEvent.click(revokeBtn);

    const dialog = await screen.findByRole("dialog");
    const confirmBtn = within(dialog).getByRole("button", {
      name: "Revoke",
    });
    await userEvent.click(confirmBtn);

    await waitFor(() => expect(deletedId).toBe("g-1"));
  });

  it("renders honest copy (not a raw string) for a 403 approval_required grant response", async () => {
    installBaseFetch({
      "/record-grants": (request) => {
        if (request.method === "POST") {
          return jsonResponse(
            {
              type: "about:blank",
              title: "Forbidden",
              status: 403,
              code: "approval_required",
              detail: "queued behind the approval gate",
            },
            403,
          );
        }
        return jsonResponse({
          data: [existingGrant],
          page: { next_cursor: null, has_more: false },
        });
      },
    });
    render(<ShareScreen recordType="deal" recordId="d-1" />);

    const pick = await screen.findByRole("button", { name: /Priya Shah/ });
    await userEvent.click(pick);
    await userEvent.click(screen.getByTestId("share-grant-submit"));

    await waitFor(() =>
      expect(screen.queryByText(/approval_required/)).toBeNull(),
    );
    expect(await screen.findByText(/held for approval|approval/i)).toBeTruthy();
  });

  it("renders honest copy (not a raw string) for a 422 validation error", async () => {
    installBaseFetch({
      "/record-grants": (request) => {
        if (request.method === "POST") {
          return jsonResponse(
            {
              type: "about:blank",
              title: "Unprocessable",
              status: 422,
              code: "validation_error",
              detail: "expires_at must be in the future",
            },
            422,
          );
        }
        return jsonResponse({
          data: [existingGrant],
          page: { next_cursor: null, has_more: false },
        });
      },
    });
    render(<ShareScreen recordType="deal" recordId="d-1" />);

    const pick = await screen.findByRole("button", { name: /Priya Shah/ });
    await userEvent.click(pick);
    await userEvent.click(screen.getByTestId("share-grant-submit"));

    expect(
      await screen.findByText("expires_at must be in the future"),
    ).toBeTruthy();
    expect(screen.queryByText("[object Object]")).toBeNull();
  });
});
