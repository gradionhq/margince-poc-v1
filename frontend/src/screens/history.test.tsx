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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  FieldHistoryTimeline,
  RecordHistory,
  RecordHistoryTab,
} from "./history";

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
beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

// The wire spells a human actor "human:<uuid>", and the read path resolves the
// display name beside it — both as the real writer and reader produce them.
const created = {
  id: "h1",
  actor_type: "human",
  actor_id: "human:u1",
  actor_name: "Demo Admin",
  action: "create",
  occurred_at: "2026-07-13T10:00:00Z",
  summary: "Demo Admin created the record",
};
// A machine acting under a human's authority. `summary` is server-composed and
// already NAMES that human as its subject (PD-002), so the row must not also
// append an on-behalf-of suffix — that would say Anna twice.
const updated = {
  id: "h2",
  actor_type: "agent",
  actor_id: "agent:sdr",
  on_behalf_of_name: "Anna Weber",
  action: "update",
  occurred_at: "2026-07-14T10:00:00Z",
  summary: "Anna Weber, via an agent, updated the record",
};

// A passport id, and the member id a connector principal carries in its tail:
// identifiers with no name in them, which no tag may print at a reader.
const OPAQUE = "0191c3a2-7f4b-4c19-9a5e-6d2f8b1e40aa";

// The installation's own processing, naming the job that ran.
const sweptBySystem = {
  id: "h3",
  actor_type: "system",
  actor_id: "system:retention-sweep",
  action: "delete",
  occurred_at: "2026-07-15T10:00:00Z",
  summary: "A retention sweep cleared the note",
};
// A connector, under the grant of a member whose uuid rides in the same string.
const writtenByConnector = {
  id: "h4",
  actor_type: "connector",
  actor_id: `connector:ext:zalo-oa:${OPAQUE}`,
  action: "update",
  occurred_at: "2026-07-16T10:00:00Z",
  summary: "A message was filed against the record",
};
// An agent whose id is its passport, which names nothing on this side.
const writtenByPassport = {
  id: "h5",
  actor_type: "agent",
  actor_id: `agent:${OPAQUE}`,
  action: "update",
  occurred_at: "2026-07-17T10:00:00Z",
  summary: "An agent updated the record",
};

describe("RecordHistory", () => {
  it("renders plain-language summaries with attribution", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ data: [created, updated], page: { next_cursor: null } }),
      ),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    await waitFor(() =>
      expect(screen.getByText("Demo Admin created the record")).toBeTruthy(),
    );
    // The person is the subject of the sentence, and named exactly once: the
    // suffix that used to complete the old machine-first phrasing would now be
    // a second copy of the same name.
    expect(
      screen.getByText("Anna Weber, via an agent, updated the record"),
    ).toBeTruthy();
    expect(screen.getAllByText(/Anna Weber/)).toHaveLength(1);
    // A human row names the person on the provenance chip too, which without a
    // resolved name says a person acted without saying which one. Twice here is
    // correct and not the doubling above: once as the sentence's subject, once
    // as the "typed by" chip in the meta row.
    expect(screen.getAllByText(/Demo Admin/)).toHaveLength(2);
  });

  it("tells a system task and a connector apart from an agent", async () => {
    // Three different facts about a record, and they used to read as one: every
    // non-human actor said "Automated by …", so a scheduled sweep and a mailbox
    // connector both named an agent that had not acted.
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [sweptBySystem, writtenByConnector, writtenByPassport],
          page: { next_cursor: null },
        }),
      ),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    expect(await screen.findByText("System task retention-sweep")).toBeTruthy();
    expect(screen.getByText("via zalo-oa")).toBeTruthy();
    // Only the row that IS an agent reads as one.
    expect(screen.getAllByText(/Automated by/)).toHaveLength(1);
    expect(screen.getByText("Automated by an agent")).toBeTruthy();
  });

  it("prints no identifier it cannot turn into a name", async () => {
    // A passport id and a connector's member-uuid tail are opaque: a reader
    // cannot look either one up, so a tag that shows them attributes the change
    // to a string instead of to something. The tag says the kind and stops.
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [writtenByConnector, writtenByPassport],
          page: { next_cursor: null },
        }),
      ),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    expect(await screen.findByText("Automated by an agent")).toBeTruthy();
    expect(screen.queryByText(new RegExp(OPAQUE))).toBeNull();
  });

  it("reads an id stamped without its kind the same as a whole principal", async () => {
    // actor_id is a plain string on this projection, and both spellings turn up
    // in it. actor_type already says which kind acted, so a bare id costs the
    // row nothing: a named one still names, and an id that is only the kind
    // names nothing rather than being promoted into the name.
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            { ...writtenByPassport, actor_id: "sdr" },
            { ...sweptBySystem, actor_id: "system" },
          ],
          page: { next_cursor: null },
        }),
      ),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    expect(await screen.findByText("Automated by sdr")).toBeTruthy();
    expect(screen.getByText("System task")).toBeTruthy();
  });

  it("shows an honest empty state", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ data: [], page: { next_cursor: null } }),
      ),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    await waitFor(() =>
      expect(screen.getByText(/No changes recorded/i)).toBeTruthy(),
    );
  });

  it("shows an error with retry", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ title: "boom" }, 500)),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    await waitFor(() => expect(screen.getByText(/Retry/i)).toBeTruthy());
  });
});

const fhCreated = {
  id: "f0",
  entity_type: "deal",
  entity_id: "d1",
  field: "name",
  old_value: null,
  new_value: "Globex Renewal",
  changed_at: "2026-07-13T10:00:00Z",
  actor_type: "human",
  actor_id: "u1",
};
const fhUpdated = {
  id: "f1",
  entity_type: "deal",
  entity_id: "d1",
  field: "name",
  old_value: "Globex Renewal",
  new_value: "Globex Renewal (updated)",
  changed_at: "2026-07-14T10:00:00Z",
  actor_type: "agent",
  actor_id: "sdr",
  passport_id: "psp_7Q3fa91",
  evidence: { snippet: "renewal signed", source: "email#42" },
};

describe("FieldHistoryTimeline", () => {
  it("groups by field and shows old→new diffs with agent passport", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [fhUpdated, fhCreated],
          page: { next_cursor: null },
        }),
      ),
    );
    render(<FieldHistoryTimeline kind="deal" id="d1" />);
    await waitFor(() =>
      expect(screen.getByText("Globex Renewal (updated)")).toBeTruthy(),
    );
    expect(screen.getByText("— created —")).toBeTruthy(); // empty-origin diff
    expect(screen.getByText(/psp_7Q3fa91/)).toBeTruthy(); // PassportChip
  });

  it("filters to human-only changes via the actor control", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      const data = url.includes("actor_type=human")
        ? [fhCreated]
        : [fhUpdated, fhCreated];
      return jsonResponse({ data, page: { next_cursor: null } });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<FieldHistoryTimeline kind="deal" id="d1" />);
    await waitFor(() => expect(screen.getByText(/psp_7Q3fa91/)).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: /human/i }));
    await waitFor(() => expect(screen.queryByText(/psp_7Q3fa91/)).toBeNull());
  });
});

describe("RecordHistoryTab", () => {
  it("toggles between the changes list and the field-diff timeline", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.includes("/field-history")) {
          return jsonResponse({
            data: [fhUpdated],
            page: { next_cursor: null },
          });
        }
        return jsonResponse({ data: [created], page: { next_cursor: null } });
      }),
    );
    render(<RecordHistoryTab kind="deal" id="d1" />);
    await waitFor(() =>
      expect(screen.getByText("Demo Admin created the record")).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /field history/i }),
    );
    await waitFor(() =>
      expect(screen.getByText("Globex Renewal (updated)")).toBeTruthy(),
    );
  });
});
