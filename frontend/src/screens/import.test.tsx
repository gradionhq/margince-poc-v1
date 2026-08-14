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
          meFixture({
            roles: ["admin"],
            allow: { import_run: ["create", "update", "read"] },
          }),
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
    // The whole body, so an extra column could not slip in unseen either.
    expect(created.body).toEqual({
      connector: "csv",
      object: "lead",
      source_ref: "ws/import/abc",
      mapping: { Email: "email", "Full Name": "full_name" },
    });

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

  // A run that stops part-way answers with a PROBLEM, not with a run — the
  // truth is on the run itself, which the server recorded before answering.
  // Reading it back is what turns "something went wrong" into the resumable
  // state the contract promises. The earlier version of this test stubbed a 202
  // carrying status:"failed", a shape the server cannot produce.
  it("reads the run back when the commit stops part-way, and offers to resume", async () => {
    stubRoutes({
      "POST /imports/019ff-run/approve": () =>
        jsonResponse(
          { status: 500, code: "internal", detail: "the import stopped" },
          500,
        ),
      "GET /imports/019ff-run/report": () =>
        jsonResponse({
          ...dryRun,
          status: "failed",
          disposition: { created: 2, updated: 0, unchanged: 0, skipped: 1 },
        }),
      "GET /imports/019ff-run": () =>
        jsonResponse({ ...run, status: "failed", checkpoint: 2 }),
    });
    render(<ImportCard />);
    await upload();
    await screen.findByRole("row", { name: /Notes/ });
    await userEvent.click(
      screen.getByRole("button", { name: "Check what this will do" }),
    );
    await screen.findByText("What this import will do");
    await userEvent.click(
      screen.getByRole("button", { name: "Import 2 rows" }),
    );

    expect(await screen.findByText(/stopped after 2 rows/)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Resume the import" }),
    ).toBeInTheDocument();
  });

  // A column the human explicitly sets back to "don't import" must leave the
  // wire, not merely be absent from the suggestion that seeded it.
  it("drops a column the human clears, and keeps the ones they kept", async () => {
    const sent = stubRoutes();
    render(<ImportCard />);
    await upload();
    await screen.findByRole("row", { name: /Notes/ });

    await userEvent.click(
      screen.getByRole("combobox", { name: "Where Full Name goes" }),
    );
    await userEvent.click(
      await screen.findByRole("option", { name: "Don't import" }),
    );
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
    expect(created.body).toMatchObject({ mapping: { Email: "email" } });
    expect(created.body).not.toMatchObject({
      mapping: { "Full Name": "full_name" },
    });
  });

  // A header the file spells with a regexp-replacement token must reach the
  // screen as itself. String.replace would read "$&" as "the whole match".
  it("shows a column name the file spells oddly, verbatim", async () => {
    stubRoutes({
      "POST /imports/sources": () =>
        jsonResponse({
          ...profile,
          columns: [
            { header: "Amount ($&)", fill_rate: 1, samples: ["10"] },
            ...profile.columns,
          ],
        }),
    });
    render(<ImportCard />);
    await upload();

    expect(
      await screen.findByRole("combobox", { name: "Where Amount ($&) goes" }),
    ).toBeInTheDocument();
  });

  // A second file that cannot be read must not leave the FIRST file's report —
  // and its armed commit button — on screen. Nothing on this card names which
  // file a report belongs to, so the button would approve the wrong estate.
  it("clears the previous file's answers as a new upload starts", async () => {
    let uploads = 0;
    stubRoutes({
      "POST /imports/sources": () => {
        uploads += 1;
        return uploads === 1
          ? jsonResponse(profile)
          : jsonResponse(
              {
                status: 422,
                code: "validation_error",
                detail: "The uploaded file has no content.",
              },
              422,
            );
      },
    });
    render(<ImportCard />);
    await upload();
    await screen.findByRole("row", { name: /Notes/ });
    await userEvent.click(
      screen.getByRole("button", { name: "Check what this will do" }),
    );
    await screen.findByText("What this import will do");

    await upload(new File([""], "broken.csv"));

    expect(
      await screen.findByText("The uploaded file has no content."),
    ).toBeInTheDocument();
    expect(screen.queryByText("What this import will do")).toBeNull();
    expect(screen.queryByRole("button", { name: /^Import / })).toBeNull();
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
          meFixture({
            roles: ["ops"],
            allow: { import_run: ["create", "update", "read"] },
          }),
        ),
    });
    render(<ImportCard />);

    // The grant is what decides, not the admin role — an ops seat holds
    // import_run and would be accepted by the store.
    expect(await screen.findByText("Import a file")).toBeInTheDocument();
  });

  // The flow creates a run, parks it, and approves it — three actions, not one.
  // A role edited to create-without-update would see the card and be refused at
  // the first button it presses.
  it("is not offered to a role that could not finish the flow", async () => {
    stubRoutes({
      "GET /me": () =>
        jsonResponse(
          meFixture({ roles: ["ops"], allow: { import_run: ["create"] } }),
        ),
    });
    const { container } = render(<ImportCard />);

    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it("is not offered to a seat that may not run one", async () => {
    stubRoutes({
      "GET /me": () => jsonResponse(meFixture({ roles: ["rep"], allow: {} })),
    });
    const { container } = render(<ImportCard />);

    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  // Undo (IEM-WIRE-9): offered only once the run is complete, reverses what
  // nobody edited, and names what it kept — A93's "kept — you edited these".
  describe("undo", () => {
    it("offers undo once the run is complete, and reverses what nobody edited", async () => {
      const sent = stubRoutes({
        "POST /imports/019ff-run/undo": () =>
          jsonResponse({ ...run, status: "undone" }, 202),
        "GET /imports/019ff-run/report": () =>
          jsonResponse({
            ...dryRun,
            status: "undone",
            undo: {
              run_id: run.id,
              status: "undone",
              reversed_count: 3,
              kept: [],
            },
          }),
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
      await screen.findByText("The import finished.");

      const undoButton = screen.getByRole("button", {
        name: "Undo this import",
      });
      await userEvent.click(undoButton);

      await waitFor(() =>
        expect(sent.some((s) => s.path.includes("/undo"))).toBe(true),
      );
      expect(
        await screen.findByText("The import was undone."),
      ).toBeInTheDocument();
      expect(screen.getByText("3 rows reversed.")).toBeInTheDocument();
      expect(screen.queryByText("import.undoKeptLead")).toBeNull();
    });

    it("names a human-edited row as kept rather than reversing it", async () => {
      stubRoutes({
        "POST /imports/019ff-run/undo": () =>
          jsonResponse({ ...run, status: "undone" }, 202),
        "GET /imports/019ff-run/report": () =>
          jsonResponse({
            ...dryRun,
            status: "undone",
            undo: {
              run_id: run.id,
              status: "undone",
              reversed_count: 2,
              kept: [{ object: "lead", id: "019ff-kept-lead" }],
            },
          }),
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
      await screen.findByText("The import finished.");
      await userEvent.click(
        screen.getByRole("button", { name: "Undo this import" }),
      );

      expect(await screen.findByText("2 rows reversed.")).toBeInTheDocument();
      expect(
        screen.getByText("Kept — you edited these since the import:"),
      ).toBeInTheDocument();
      expect(screen.getByText(/019ff-kept-lead/)).toBeInTheDocument();
    });

    it("offers to continue an undo that was interrupted, not to restart it", async () => {
      stubRoutes({
        "POST /imports/019ff-run/undo": () =>
          jsonResponse({ ...run, status: "undoing" }, 202),
        "GET /imports/019ff-run/report": () =>
          jsonResponse({ ...dryRun, status: "undoing" }),
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
      await screen.findByText("The import finished.");
      await userEvent.click(
        screen.getByRole("button", { name: "Undo this import" }),
      );

      expect(
        await screen.findByText(/undo was interrupted partway through/),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Continue the undo" }),
      ).toBeInTheDocument();
    });
  });
});
