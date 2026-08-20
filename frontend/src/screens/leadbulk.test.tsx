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
import type { components } from "../api/schema";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import type { BulkAction, BulkOutcome } from "./leadbulk";
import { LeadBulkBar } from "./leadbulk";

// The bulk bar's disqualify verb. A lead is closed WITH a reason — the
// single-lead dialog requires one, so the fan-out over a selection requires
// the same one, and it rides on every row's own DELETE. A bulk path that
// skipped it would leave `disqualify_reason_id` null on whole batches, which
// is the reporting hole this suite exists to keep shut.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// Typed against the generated schema rather than asserted into it: the enum
// members and the required fields are then checked here, so a fixture that has
// drifted from the contract fails at compile time instead of being coerced
// past it.
const leads: components["schemas"]["Lead"][] = [
  {
    id: "l-1",
    full_name: "Jonas Petersen",
    email: "jonas@nordwind.example",
    status: "contacted",
    score: 72,
    source: "manual",
    captured_by: "human:u1",
    version: 3,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "l-2",
    full_name: "Otto Fischer",
    email: "otto@fischer.example",
    status: "new",
    score: 40,
    source: "webform",
    captured_by: "human:u1",
    version: 7,
    created_at: "2026-01-02T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
  },
];

// Three administered reasons, one of them retired: an inactive reason may not
// be applied to a new close, so the picker must not offer it.
const REASONS = {
  data: [
    { id: "r-1", label: "Not a fit", active: true },
    { id: "r-2", label: "No budget", active: true },
    { id: "r-3", label: "Wrong region", active: false },
  ].map((reason, i) => ({
    ...reason,
    sort_order: (i + 1) * 10,
    system: true,
    lead_count: 0,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  })),
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function problemResponse(status: number, code: string, detail: string) {
  return new Response(JSON.stringify({ title: code, status, code, detail }), {
    status,
    headers: { "content-type": "application/problem+json" },
  });
}

type Deletion = { id: string; body: unknown };

/**
 * The routes the bar reads (the user roster, the reason list) plus a DELETE
 * recorder. `refuse` names the rows the server turns down, so a test can put a
 * partial failure in front of the reader without hand-writing an outcome the
 * component never produced.
 */
function stubFetch(refuse: Readonly<Record<string, () => Response>> = {}): {
  deletions: Deletion[];
} {
  const deletions: Deletion[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const url = new URL(request.url);
      if (request.method === "DELETE") {
        const id = url.pathname.split("/leads/")[1] ?? "";
        deletions.push({ id, body: JSON.parse(await request.text()) });
        const denial = refuse[id];
        return denial
          ? denial()
          : jsonResponse({
              ...leads[0],
              id,
              status: "disqualified",
              archived_at: "2026-02-01T00:00:00Z",
            });
      }
      if (url.pathname.endsWith("/lead-disqualify-reasons")) {
        return jsonResponse(REASONS);
      }
      if (url.pathname.endsWith("/users")) {
        return jsonResponse({
          data: [{ id: "u-9", email: "lena@x.test", display_name: "Lena F." }],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({
        data: [],
        page: { next_cursor: null, has_more: false },
      });
    }),
  );
  return { deletions };
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

const disqualifyButton = () =>
  screen.getByRole("button", { name: "Disqualify" });

describe("LeadBulkBar — disqualify", () => {
  it("refuses the verb until a reason is chosen, and says why", async () => {
    const { deletions } = stubFetch();
    render(<LeadBulkBar leads={leads} onDone={() => undefined} />);

    // The sentence is the refusal: the reader is told what to answer, not
    // handed a dimmed button with no explanation.
    const sentence = await screen.findByText("Pick a reason first.");
    const verb = disqualifyButton();
    expect(verb.hasAttribute("disabled")).toBe(true);
    // And the sentence is attached to the control, not merely near it — a
    // disabled button cannot be focused, so a reason only in `title` or in a
    // neighbouring span reaches nobody using a screen reader.
    expect(verb.getAttribute("aria-describedby")).toBe(sentence.id);

    await userEvent.setup().click(verb);
    expect(deletions).toEqual([]);
  });

  it("offers only the ACTIVE reasons, and sends the chosen one on every row's own DELETE", async () => {
    const { deletions } = stubFetch();
    const done: Array<{
      outcomes: readonly BulkOutcome[];
      action: BulkAction;
    }> = [];
    const user = userEvent.setup();
    render(
      <LeadBulkBar
        leads={leads}
        onDone={(outcomes, action) => done.push({ outcomes, action })}
      />,
    );

    // Opened by hand rather than through pickOption: the offered set is what
    // this case is about, and pickOption's first act is the trigger click that
    // would close the popup the assertion needs open.
    const picker = await screen.findByRole("combobox", { name: "Reason" });
    await user.click(picker);
    expect(
      screen.getAllByRole("option").map((option) => option.textContent),
    ).toEqual(["Not a fit", "No budget"]);
    await user.click(screen.getByRole("option", { name: "No budget" }));

    await waitFor(() =>
      expect(disqualifyButton().hasAttribute("disabled")).toBe(false),
    );
    await user.click(disqualifyButton());

    await waitFor(() => expect(deletions).toHaveLength(2));
    expect(deletions).toEqual([
      { id: "l-1", body: { reason_id: "r-2" } },
      { id: "l-2", body: { reason_id: "r-2" } },
    ]);
    await waitFor(() => expect(done).toHaveLength(1));
    // The verb reports the reason it applied, because that reason is the
    // mutation's own variable rather than a value read back out of the render
    // the reader has since left.
    expect(done[0].action).toEqual({ kind: "disqualify", reasonId: "r-2" });
    expect(done[0].outcomes.every((outcome) => !outcome.error)).toBe(true);
  });

  it("names the rows that refused instead of claiming the batch closed", async () => {
    const { deletions } = stubFetch({
      "l-2": () =>
        problemResponse(409, "already_disqualified", "Already closed."),
    });
    const done: Array<readonly BulkOutcome[]> = [];
    const user = userEvent.setup();
    render(
      <LeadBulkBar leads={leads} onDone={(outcomes) => done.push(outcomes)} />,
    );

    await pickOption(
      user,
      await screen.findByRole("combobox", { name: "Reason" }),
      "Not a fit",
    );
    await waitFor(() =>
      expect(disqualifyButton().hasAttribute("disabled")).toBe(false),
    );
    await user.click(disqualifyButton());

    await waitFor(() => expect(deletions).toHaveLength(2));
    // Both rows were attempted, one refused, and the bar says so by name with
    // the server's own reason — the caller then keeps that row selected.
    expect(await screen.findByText(/1 not applied/)).toBeTruthy();
    expect(screen.getByText(/Otto Fischer: /)).toBeTruthy();
    await waitFor(() => expect(done).toHaveLength(1));
    expect(
      done[0].map((outcome) => [outcome.id, Boolean(outcome.error)]),
    ).toEqual([
      ["l-1", false],
      ["l-2", true],
    ]);
  });
});
