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
import { type ReactNode, useLayoutEffect, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import {
  type CreateField,
  CreateRecordModal,
  splitMultiselectValue,
  submittedValues,
  visibleFields,
} from "./create";
import { DealsScreen } from "./deals";
import { ContactsScreen } from "./people";

// Create flows (the "you can actually add a record" acceptance): the list
// screens open a create modal, the POST body carries the server's shape
// (source stamped manual, emails as typed rows, major→minor amount), a
// success navigates to the fresh 360, and a 422 renders its RFC 7807 detail
// verbatim — the server's validation is the truth.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
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

const emptyPage = { data: [], page: { next_cursor: null } };

type Captured = { key: string; body: unknown };

function stubApi(
  routes: Record<string, (body: unknown) => Response>,
  captured?: Captured[],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
      let body: unknown = null;
      if (method !== "GET") {
        try {
          body = request
            ? await request.json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      captured?.push({ key, body });
      const handler = routes[key];
      return handler ? handler(body) : jsonResponse(emptyPage);
    }),
  );
}

const pipeline = {
  id: "pl",
  name: "Sales",
  is_default: true,
  position: 0,
  stages: [
    {
      id: "s1",
      pipeline_id: "pl",
      name: "Qualify",
      position: 1,
      semantic: "open",
      win_probability: 20,
    },
    {
      id: "s4",
      pipeline_id: "pl",
      name: "Won",
      position: 4,
      semantic: "won",
      win_probability: 100,
    },
  ],
};

describe("contact create flow", () => {
  it("posts the typed values with source=manual and navigates to the new 360", async () => {
    const captured: Captured[] = [];
    stubApi(
      {
        "POST /people": (body) =>
          jsonResponse(
            {
              id: "p-new",
              full_name: (body as { full_name: string }).full_name,
              captured_by: "human:u1",
              source: "manual",
              version: 1,
            },
            201,
          ),
      },
      captured,
    );
    render(<ContactsScreen />);
    await userEvent.click(screen.getByText("New contact"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Peter Neu");
    await userEvent.click(screen.getByText("Add email"));
    await userEvent.type(screen.getByLabelText("Email *"), "peter@neu.example");
    await userEvent.click(screen.getByRole("radio", { name: "Primary" }));
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(window.location.hash).toBe("#/contacts/p-new"));
    const post = captured.find((entry) => entry.key === "POST /people");
    expect(post?.body).toMatchObject({
      full_name: "Peter Neu",
      source: "manual",
      emails: [
        { email: "peter@neu.example", email_type: "work", is_primary: true },
      ],
    });
  });

  it("renders the server's 422 detail verbatim and stays open", async () => {
    stubApi({
      "POST /people": () =>
        jsonResponse(
          { title: "Unprocessable", detail: "full_name must not be blank" },
          422,
        ),
    });
    render(<ContactsScreen />);
    await userEvent.click(screen.getByText("New contact"));
    await userEvent.type(screen.getByLabelText("Full name *"), "x");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(screen.getByText("full_name must not be blank")).toBeTruthy(),
    );
    expect(screen.getByLabelText("Full name *")).toBeTruthy();
    // Announced, not merely rendered: nothing moves when a refusal appears, so
    // the reason is silent to a screen reader without a live region. The edit
    // dialog renders this same form body, so it is covered by the same node.
    expect(screen.getByRole("alert").textContent).toBe(
      "full_name must not be blank",
    );
  });
});

// The multiselect CreateField type (A10): a checkbox group over `options`
// that collects the toggled selection as a comma-joined string in the SAME
// `values: Record<string, string>` channel every scalar field already uses —
// `splitMultiselectValue`/`joinMultiselectValue` are the documented mapper a
// screen's transport uses to recover the `string[]`. This keeps every
// existing single-string field (text/email/number/date/select) untouched.
describe("multiselect CreateField", () => {
  const fields: CreateField[] = [
    { key: "name", labelText: "Name", type: "text", required: true },
    {
      key: "event_types",
      labelText: "Event types",
      type: "multiselect",
      options: [
        { value: "deal.created", label: "Deal created" },
        { value: "deal.won", label: "Deal won" },
        { value: "person.created", label: "Person created" },
      ],
    },
  ];

  it("renders each option as a toggleable checkbox", () => {
    render(
      <CreateRecordModal
        open
        onClose={() => {}}
        title="New webhook"
        fields={fields}
        pending={false}
        error={null}
        onSubmit={() => {}}
      />,
    );
    const dealCreated = screen.getByLabelText(
      "Deal created",
    ) as HTMLInputElement;
    expect(dealCreated.type).toBe("checkbox");
    expect(dealCreated.checked).toBe(false);
  });

  it("collects toggled options as a string[] on submit, leaving an existing text field's plain string untouched", async () => {
    const onSubmit = vi.fn();
    render(
      <CreateRecordModal
        open
        onClose={() => {}}
        title="New webhook"
        fields={fields}
        pending={false}
        error={null}
        onSubmit={onSubmit}
      />,
    );
    await userEvent.type(screen.getByLabelText("Name *"), "Peter");
    await userEvent.click(screen.getByLabelText("Deal created"));
    await userEvent.click(screen.getByLabelText("Person created"));
    // toggling back off removes it from the collected selection
    await userEvent.click(screen.getByLabelText("Deal created"));
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(onSubmit).toHaveBeenCalledTimes(1);
    const [values] = onSubmit.mock.calls[0] as [Record<string, string>];
    expect(values.name).toBe("Peter");
    expect(splitMultiselectValue(values.event_types)).toEqual([
      "person.created",
    ]);
  });
});

// The create modal's counterpart to the edit modal's prefill (edit.test.tsx):
// the reset has to land in the commit that puts the form back on screen, not
// one commit later — otherwise a reopened modal shows the abandoned attempt.
describe("create modal reset", () => {
  it("is blank in the very commit that puts a reopened form on screen", async () => {
    const seeded: CreateField[] = [
      { key: "name", labelText: "Name", type: "text", required: true },
    ];
    // What the Name input holds each time the open modal reaches the DOM. A
    // layout effect runs inside that commit — before the browser paints and
    // before any passive effect — so it sees exactly the first frame a user
    // could see and type into.
    const firstFrames: string[] = [];
    function OpenHarness() {
      const [open, setOpen] = useState(false);
      useLayoutEffect(() => {
        const name = screen.queryByLabelText(
          "Name *",
        ) as HTMLInputElement | null;
        if (name) {
          firstFrames.push(name.value);
        }
      });
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            Open
          </button>
          <CreateRecordModal
            open={open}
            onClose={() => setOpen(false)}
            title="New record"
            fields={seeded}
            pending={false}
            error={null}
            onSubmit={vi.fn()}
          />
        </>
      );
    }
    render(<OpenHarness />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    await userEvent.type(screen.getByLabelText("Name *"), "Abandoned");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    // Resetting in a passive effect leaves the abandoned attempt on screen for
    // the frame between the reopen and the reset.
    expect(firstFrames.at(-1)).toBe("");
  });
});

// An optional select needs a way back to unset, and the value gets exactly one
// entry: the generic "Not set" appears only where the field offers no clearing
// choice of its own, so a screen's own wording is never doubled up.
describe("an optional select's unset choice", () => {
  const owner: CreateField = {
    key: "owner_id",
    labelText: "Owner",
    type: "select",
    options: [
      { value: "u1", label: "Me" },
      { value: "", label: "Unassign" },
    ],
  };

  function openedOptionLabels(): (string | null)[] {
    return within(screen.getByRole("listbox"))
      .getAllByRole("option")
      .map((option) => option.textContent);
  }

  async function openOwnerList(fields: CreateField[]) {
    render(
      <CreateRecordModal
        open
        onClose={() => {}}
        title="New deal"
        fields={fields}
        pending={false}
        error={null}
        onSubmit={() => {}}
      />,
    );
    await userEvent.click(screen.getByLabelText("Owner"));
  }

  it("keeps the field's own clearing entry and adds no second one for the same value", async () => {
    await openOwnerList([owner]);
    expect(openedOptionLabels()).toEqual(["Me", "Unassign"]);
  });

  it("synthesizes one when the field offers no way back to unset", async () => {
    await openOwnerList([
      { ...owner, options: [{ value: "u1", label: "Me" }] },
    ]);
    expect(openedOptionLabels()).toEqual(["Not set", "Me"]);
  });
});

describe("deal create flow", () => {
  it("offers only open stages, converts major→minor, and posts the pipeline", async () => {
    const captured: Captured[] = [];
    stubApi(
      {
        "GET /pipelines": () =>
          jsonResponse({ data: [pipeline], page: { next_cursor: null } }),
        "POST /deals": (body) =>
          jsonResponse(
            {
              id: "d-new",
              name: (body as { name: string }).name,
              pipeline_id: "pl",
              stage_id: "s1",
              status: "open",
              source: "manual",
              captured_by: "human:u1",
              version: 1,
              created_at: "2026-07-06T08:00:00Z",
              updated_at: "2026-07-06T08:00:00Z",
            },
            201,
          ),
      },
      captured,
    );
    render(<DealsScreen startCreating />);
    await waitFor(() => expect(screen.getByLabelText("Stage *")).toBeTruthy());
    // won/lost stages are not creatable targets — deals are born open. The list
    // only exists while the control is open, so it is opened to be read and shut
    // again before the form is filled in.
    const stageSelect = screen.getByLabelText("Stage *");
    await userEvent.click(stageSelect);
    expect(
      within(screen.getByRole("listbox"))
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual(["Qualify"]);
    await userEvent.keyboard("{Escape}");
    await userEvent.type(screen.getByLabelText("Deal name *"), "Neuer Deal");
    await userEvent.type(screen.getByLabelText("Value"), "480");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(window.location.hash).toBe("#/deals/d-new"));
    const post = captured.find((entry) => entry.key === "POST /deals");
    expect(post?.body).toMatchObject({
      name: "Neuer Deal",
      amount_minor: 48000,
      currency: "EUR",
      pipeline_id: "pl",
      stage_id: "s1",
      source: "manual",
    });
  });
});

// A field that only makes sense once another is answered. What a partner did
// for a deal is a question ABOUT a partner, so it appears when one is named
// and goes away when the choice is cleared.
describe("a field that depends on another", () => {
  const fields: CreateField[] = [
    { key: "name", label: "create.dealName", required: true },
    { key: "partner_org_id", label: "deal.partnerOrg", type: "select" },
    {
      key: "partner_attribution",
      label: "deal.partnerAttribution",
      type: "select",
      showWhen: (values) => Boolean(values.partner_org_id),
    },
  ];

  it("stays hidden until the field it depends on is answered", () => {
    const shown = visibleFields(fields, { name: "x", partner_org_id: "" });
    expect(shown.map((f) => f.key)).toEqual(["name", "partner_org_id"]);
  });

  it("appears once that field is answered", () => {
    const shown = visibleFields(fields, { name: "x", partner_org_id: "p-1" });
    expect(shown.map((f) => f.key)).toContain("partner_attribution");
  });

  // Answering a question and then withdrawing the one it depended on must not
  // submit the orphaned answer: an attribution with no partner is refused by
  // the server, and the reader did not ask to send it.
  it("does not submit a value whose field went away", () => {
    const sent = submittedValues(fields, {
      name: "x",
      partner_org_id: "",
      partner_attribution: "influenced",
    });
    expect(sent.partner_attribution).toBe("");
    expect(sent.name).toBe("x");
  });

  it("submits the value while its field is showing", () => {
    const sent = submittedValues(fields, {
      name: "x",
      partner_org_id: "p-1",
      partner_attribution: "influenced",
    });
    expect(sent.partner_attribution).toBe("influenced");
  });

  // A key the form never declared is a custom field, and blanking one the
  // caller set would silently discard it.
  it("leaves an undeclared value alone", () => {
    const sent = submittedValues(fields, { name: "x", cf_region: "APAC" });
    expect(sent.cf_region).toBe("APAC");
  });
});

// The partner fields answer to whether this installation runs a partner
// programme at all. "Has anybody been made a partner" is what decides it —
// there is no separate switch — so an installation with no partners is not
// asked a partner question.
describe("the deal form's partner fields", () => {
  const partnerRoutes = {
    "GET /pipelines": () =>
      jsonResponse({ data: [pipeline], page: { next_cursor: null } }),
    "GET /organizations": () =>
      jsonResponse({
        data: [{ id: "o-1", display_name: "VietnamPartner JSC" }],
        page: { next_cursor: null },
      }),
    "GET /partners": () =>
      jsonResponse({
        data: [{ organization_id: "o-1", margin_tier: "tier2_20" }],
        page: { next_cursor: null },
      }),
  };

  it("shows no partner fields when no company is a partner", async () => {
    // Every unstubbed route answers an empty page, so /partners says none.
    stubApi({
      "GET /pipelines": () =>
        jsonResponse({ data: [pipeline], page: { next_cursor: null } }),
    });
    render(<DealsScreen startCreating />);
    await waitFor(() => expect(screen.getByLabelText("Stage *")).toBeTruthy());

    expect(screen.queryByLabelText("via Partner")).toBeNull();
    expect(screen.queryByLabelText("What the partner did")).toBeNull();
  });

  it("offers the partner once one exists, and asks what they did only after one is picked", async () => {
    stubApi(partnerRoutes);
    render(<DealsScreen startCreating />);
    await waitFor(() => expect(screen.getByLabelText("Stage *")).toBeTruthy());

    const user = userEvent.setup();
    const partner = await screen.findByLabelText("via Partner");
    // The claim is a question about a partner, so it is not asked before one
    // is named.
    expect(screen.queryByLabelText("What the partner did")).toBeNull();

    await pickOption(user, partner, "VietnamPartner JSC");

    expect(await screen.findByLabelText("What the partner did")).toBeTruthy();
  });

  // Only actual partners: the picker once listed every organization, which let
  // a deal be attributed to an ordinary customer and silently never pay.
  it("offers only companies that are partners", async () => {
    stubApi({
      ...partnerRoutes,
      "GET /organizations": () =>
        jsonResponse({
          data: [
            { id: "o-1", display_name: "VietnamPartner JSC" },
            { id: "o-2", display_name: "Just A Customer" },
          ],
          page: { next_cursor: null },
        }),
    });
    const user = userEvent.setup();
    render(<DealsScreen startCreating />);
    const partner = await screen.findByLabelText("via Partner");
    await user.click(partner);

    const offered = within(screen.getByRole("listbox"))
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(offered).toContain("VietnamPartner JSC");
    expect(offered).not.toContain("Just A Customer");
  });
});
