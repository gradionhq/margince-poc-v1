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
  overrides: Record<string, (req: Request) => Response | Promise<Response>> = {},
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(String(input), init);
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
          return jsonResponse({}, 204);
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
    expect(
      await screen.findByText(/held for approval|approval/i),
    ).toBeTruthy();
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
