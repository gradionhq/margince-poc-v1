/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import "@testing-library/jest-dom/vitest";

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
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { ImportCard } from "./import";

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

const profile = {
  source_ref: "ws/import/abc",
  object: "lead",
  rows_profiled: 4,
  columns: [
    { header: "Email", fill_rate: 1, samples: ["ada@x.test", "grace@x.test"] },
    { header: "Full Name", fill_rate: 0.75, samples: ["Ada Lovelace"] },
    { header: "Notes", fill_rate: 0.25, samples: [] },
  ],
  suggested_mapping: { Email: "email", "Full Name": "full_name" },
  targets: ["full_name", "email", "title", "company_name"],
};

const run = {
  id: "019ff-run",
  connector: "csv",
  object: "lead",
  status: "awaiting_approval",
  checkpoint: 0,
  source: "import_api",
  created_at: "2026-08-13T10:00:00Z",
  updated_at: "2026-08-13T10:00:00Z",
};

const dryRun = {
  run_id: run.id,
  status: "awaiting_approval",
  rows_read: 4,
  disposition: { created: 3, updated: 0, unchanged: 0, skipped: 1 },
  issues: [{ line: 3, reason: 'the "Email" column is empty' }],
  source_key_used: "Email",
};

type Sent = { method: string; path: string; body?: unknown };

// Every request the card could make, recorded, so a test can assert what
// actually went to the server — including the mapping, which is the one thing
// the screen composes rather than echoes.
function stubRoutes(overrides: Record<string, () => Response> = {}) {
  const sent: Sent[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // The typed client calls fetch with a Request; the hand-rolled multipart
      // upload calls it with a url + init. Both shapes reach here.
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;

      let body: unknown;
      const raw = request ? await request.clone().text() : init?.body;
      if (typeof raw === "string" && raw.length > 0) {
        body = JSON.parse(raw);
      } else if (init?.body instanceof FormData) {
        body = Object.fromEntries(
          [...init.body.entries()].map(([k, v]) => [
            k,
            v instanceof File ? v.name : v,
          ]),
        );
      }
      sent.push({ method, path: key, body });

      for (const [prefix, make] of Object.entries(overrides)) {
        if (key.startsWith(prefix)) {
          return make();
        }
      }
      if (key === "GET /me") {
        return jsonResponse(
          meFixture({ roles: ["admin"], allow: { import_run: ["create"] } }),
        );
      }
      if (key === "POST /imports/sources") {
        return jsonResponse(profile);
      }
      if (key.startsWith("POST /imports/") && key.endsWith("/approve")) {
        return jsonResponse({ ...run, status: "complete" }, 202);
      }
      if (key === "POST /imports") {
        return jsonResponse(run, 202);
      }
      if (key.endsWith("/report")) {
        return jsonResponse(dryRun);
      }
      return jsonResponse({});
    }),
  );
  return sent;
}

async function upload(file = new File(["Email\na@x.test\n"], "estate.csv")) {
  // The card draws nothing until /me has answered whether this seat may import.
  const input = await screen.findByLabelText("The CSV to import");
  await userEvent.upload(input, file);
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("the import card", () => {
  it("shows each column's fill rate and values before anyone maps it", async () => {
    stubRoutes();
    render(<ImportCard />);

    await upload();

    // The fill rate is what separates a column worth mapping from one that is
    // a mapping mistake waiting to happen; without it a name is all you have.
    const notes = await screen.findByRole("row", { name: /Notes/ });
    expect(within(notes).getByText("25%")).toBeInTheDocument();
    const email = screen.getByRole("row", { name: /Email/ });
    expect(within(email).getByText(/ada@x.test/)).toBeInTheDocument();
    expect(within(notes).getByText("empty")).toBeInTheDocument();
  });

  it("sends only the columns with a destination, and reports what it will do", async () => {
    const sent = stubRoutes();
    render(<ImportCard />);
    await upload();
    await screen.findByRole("row", { name: /Notes/ });

    await userEvent.click(
      screen.getByRole("button", { name: "Check what this will do" }),
    );

    const created = await waitFor(() => {
      const found = sent.find((s) => s.path === "POST /imports");
      if (!found) {
        throw new Error("the run was never created");
      }
      return found;
    });
    // "Notes" was suggested nothing, so it stays out — an unmapped column is
    // not a column mapped to nothing.
    expect(created.body).toMatchObject({
      connector: "csv",
      object: "lead",
      source_ref: "ws/import/abc",
      mapping: { Email: "email", "Full Name": "full_name" },
    });
    expect((created.body as { mapping: object }).mapping).not.toHaveProperty(
      "Notes",
    );

    // The prediction, and the row it cannot take, named by its line.
    expect(
      await screen.findByText("What this import will do"),
    ).toBeInTheDocument();
    // The disclosure names the line to open in the file AND why, in one
    // sentence a human can act on.
    const issue = screen.getByRole("listitem");
    expect(issue).toHaveTextContent("Line 3:");
    expect(issue).toHaveTextContent('the "Email" column is empty');
  });

  it("writes nothing until the human presses the second button", async () => {
    const sent = stubRoutes();
    render(<ImportCard />);
    await upload();
    await screen.findByRole("row", { name: /Notes/ });
    await userEvent.click(
      screen.getByRole("button", { name: "Check what this will do" }),
    );
    await screen.findByText("What this import will do");

    // The whole promise of the screen: validating has not approved anything.
    expect(sent.some((s) => s.path.includes("/approve"))).toBe(false);

    await userEvent.click(
      screen.getByRole("button", { name: "Import 3 rows" }),
    );

    await waitFor(() =>
      expect(sent.some((s) => s.path.includes("/approve"))).toBe(true),
    );
    expect(await screen.findByText("The import finished.")).toBeInTheDocument();
  });

  it("counts the rows it will write in words that read as English", async () => {
    stubRoutes({
      "GET /imports": () =>
        jsonResponse({
          ...dryRun,
          disposition: { created: 1, updated: 0, unchanged: 2, skipped: 1 },
        }),
    });
    render(<ImportCard />);
    await upload();
    await screen.findByRole("row", { name: /Notes/ });
    await userEvent.click(
      screen.getByRole("button", { name: "Check what this will do" }),
    );

    // "1 rows" is how a machine counts. This button is the last thing a human
    // reads before the least reversible write in the product.
    expect(
      await screen.findByRole("button", { name: "Import 1 row" }),
    ).toBeInTheDocument();
  });

  it("refuses to validate a mapping that identifies no row", async () => {
    stubRoutes({
      "POST /imports/sources": () =>
        jsonResponse({
          ...profile,
          // Nothing lands on email: no row could be recognized on a second
          // upload, or undone.
          suggested_mapping: { "Full Name": "full_name" },
        }),
    });
    render(<ImportCard />);
    await upload();

    expect(
      await screen.findByText(
        /Map a column to email\. Without it no row can be recognized/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Check what this will do" }),
    ).toBeDisabled();
  });

  it("offers to resume a run that stopped part-way, rather than starting again", async () => {
    stubRoutes({
      "POST /imports/019ff-run/approve": () =>
        jsonResponse({ ...run, status: "failed", checkpoint: 200 }, 202),
      "GET /imports": () => jsonResponse({ ...dryRun, status: "failed" }),
    });
    render(<ImportCard />);
    await upload();
    await screen.findByRole("row", { name: /Notes/ });
    await userEvent.click(
      screen.getByRole("button", { name: "Check what this will do" }),
    );
    await screen.findByText("What this import will do");
    await userEvent.click(
      screen.getByRole("button", { name: "Import 3 rows" }),
    );

    expect(
      await screen.findByText(/stopped after 200 rows/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Resume the import" }),
    ).toBeInTheDocument();
  });

  it("says what went wrong with a file it cannot read", async () => {
    stubRoutes({
      "POST /imports/sources": () =>
        jsonResponse(
          {
            status: 422,
            code: "validation_error",
            detail: "The uploaded file has no content.",
          },
          422,
        ),
    });
    render(<ImportCard />);

    await upload(new File([""], "empty.csv"));

    expect(
      await screen.findByText("The uploaded file has no content."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("is offered to an ops seat, which the server also admits", async () => {
    stubRoutes({
      "GET /me": () =>
        jsonResponse(
          meFixture({ roles: ["ops"], allow: { import_run: ["create"] } }),
        ),
    });
    render(<ImportCard />);

    // The grant is what decides, not the admin role — an ops seat holds
    // import_run and would be accepted by the store.
    expect(await screen.findByText("Import a file")).toBeInTheDocument();
  });

  it("is not offered to a seat that may not run one", async () => {
    stubRoutes({
      "GET /me": () => jsonResponse(meFixture({ roles: ["rep"], allow: {} })),
    });
    const { container } = render(<ImportCard />);

    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });
});
