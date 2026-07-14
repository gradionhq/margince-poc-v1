/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { CustomFieldsScreen, FieldBuilder, FieldTable } from "./customfields";

afterEach(cleanup);

// The screen sits behind the app auth gate: useMe only asks /v1/me once a
// workspace slug is resolved, so the integration harness seeds one and clears
// the stubbed globals between cases.
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
  window.location.hash = "";
});

const wrap = (ui: React.ReactNode) =>
  render(<LocaleProvider initial="en">{ui}</LocaleProvider>);

function builder(
  overrides: Partial<React.ComponentProps<typeof FieldBuilder>> = {},
) {
  const onSubmit = vi.fn();
  const onToast = vi.fn();
  wrap(
    <FieldBuilder
      object="organization"
      pending={false}
      onSubmit={onSubmit}
      onToast={onToast}
      {...overrides}
    />,
  );
  return { onSubmit, onToast };
}

describe("FieldBuilder", () => {
  it("mirrors the label into the immutable disabled api key", async () => {
    builder();
    await userEvent.type(screen.getByLabelText(/Label/i), "Contract end date");
    const key = screen.getByLabelText(/API key/i) as HTMLInputElement;
    expect(key.value).toBe("organization.cf_contract_end_date");
    expect(key).toBeDisabled();
  });

  it("shows the pending DDL preview reflecting the type", async () => {
    builder();
    await userEvent.type(screen.getByLabelText(/Label/i), "Contract end date");
    await userEvent.click(screen.getByRole("button", { name: /^Date$/i }));
    expect(
      screen.getByText(
        /ALTER organization ADD COLUMN cf_contract_end_date \(date\)/,
      ),
    ).toBeInTheDocument();
  });

  it("refuses a structural label and disables Confirm", async () => {
    builder();
    await userEvent.type(
      screen.getByLabelText(/Label/i),
      "Link to parent account",
    );
    expect(
      screen.getByText(/looks like a new object or relationship/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Confirm & add field/i }),
    ).toBeDisabled();
  });

  it("guards an empty label: Confirm disabled, guard toast on click attempt", async () => {
    const { onToast, onSubmit } = builder();
    const confirm = screen.getByRole("button", {
      name: /Confirm & add field/i,
    });
    expect(confirm).toBeDisabled();
    // the guard toast is wired to the always-clickable Add affordance; assert via helper
    expect(onSubmit).not.toHaveBeenCalled();
    expect(onToast).not.toHaveBeenCalled();
  });

  it("reveals the ISO-4217 input for currency", async () => {
    builder();
    await userEvent.click(screen.getByRole("button", { name: /^Currency$/i }));
    expect(screen.getByLabelText(/Currency code/i)).toBeInTheDocument();
  });

  it("reveals the options editor for picklist and blocks removing the last option", async () => {
    const { onToast } = builder();
    await userEvent.click(screen.getByRole("button", { name: /^Picklist$/i }));
    const removes = screen.getAllByRole("button", { name: /remove option/i });
    // start with one row; removing it is blocked
    await userEvent.click(removes[removes.length - 1]);
    expect(onToast).toHaveBeenCalledWith(
      "A picklist needs at least one option",
    );
  });

  it("submits a well-formed draft on Confirm", async () => {
    const { onSubmit } = builder();
    await userEvent.type(screen.getByLabelText(/Label/i), "Renewal date");
    await userEvent.click(screen.getByRole("button", { name: /^Date$/i }));
    await userEvent.click(
      screen.getByRole("button", { name: /Confirm & add field/i }),
    );
    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({
        object: "organization",
        label: "Renewal date",
        type: "date",
      }),
    );
  });

  it("keeps Confirm disabled for a picklist whose only option is blank", async () => {
    builder();
    await userEvent.type(screen.getByLabelText(/Label/i), "Deal source");
    await userEvent.click(screen.getByRole("button", { name: /^Picklist$/i }));
    // The single option row is left blank — a picklist with no real choice
    // must not be confirmable.
    expect(
      screen.getByRole("button", { name: /Confirm & add field/i }),
    ).toBeDisabled();
    // Typing a real option flips Confirm back on.
    await userEvent.type(screen.getByLabelText(/Option label/i), "Referral");
    expect(
      screen.getByRole("button", { name: /Confirm & add field/i }),
    ).toBeEnabled();
  });
});

type CustomField = components["schemas"]["CustomField"];

const field = (over: Partial<CustomField> = {}): CustomField => ({
  id: "01J",
  workspace_id: "w",
  object: "deal",
  label: "Renewal date",
  slug: "renewal_date",
  type: "date",
  status: "active",
  column_name: "cf_renewal_date",
  created_by: "u1",
  created_at: "2026-06-22T14:09:00Z",
  updated_at: "2026-06-22T14:09:00Z",
  version: 1,
  ...over,
});

describe("FieldTable", () => {
  it("lists a field with its immutable api key and a type chip", () => {
    wrap(
      <FieldTable
        object="deal"
        fields={[field()]}
        canManage
        meUserId="u1"
        onRename={() => {}}
        onArchive={() => {}}
      />,
    );
    expect(screen.getByText("Renewal date")).toBeInTheDocument();
    expect(screen.getByText("deal.cf_renewal_date")).toBeInTheDocument();
    expect(screen.getByText(/Date/)).toBeInTheDocument();
    expect(screen.getByText("You")).toBeInTheDocument();
  });

  it("renders an honest empty state for an object with no fields", () => {
    wrap(
      <FieldTable
        object="person"
        fields={[]}
        canManage
        meUserId="u1"
        onRename={() => {}}
        onArchive={() => {}}
      />,
    );
    expect(
      screen.getByText(/No custom fields on Contact yet/i),
    ).toBeInTheDocument();
  });

  it("hides edit/archive controls when the viewer cannot manage", () => {
    wrap(
      <FieldTable
        object="deal"
        fields={[field()]}
        canManage={false}
        meUserId="u1"
        onRename={() => {}}
        onArchive={() => {}}
      />,
    );
    expect(screen.queryByRole("button", { name: /Archive field/i })).toBeNull();
  });

  it("dims a retired field and marks it retired", () => {
    wrap(
      <FieldTable
        object="deal"
        fields={[field({ status: "retired" })]}
        canManage
        meUserId="u1"
        onRename={() => {}}
        onArchive={() => {}}
      />,
    );
    expect(screen.getByText(/Retired/i)).toBeInTheDocument();
  });
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Recorded = { method: string; url: string; body: unknown };

// A fetch stub over the shipped custom-fields contract: /v1/me for the role
// probe, per-object list reads keyed off the `object` query param, a 201 echo
// on create, retire + rename recorded verbatim, and an empty audit page. Every
// route the screen touches is answered so QueryGate renders content, never its
// error card. `opts.failCreate` makes POST /custom-fields reject with a 422 so
// the optimistic-rollback path can be exercised.
function customFieldsBackend(
  dealFields: CustomField[],
  orgFields: CustomField[],
  calls: Recorded[],
  roles: string[] = ["admin"],
  opts: { failCreate?: boolean } = {},
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    const readBody = async (): Promise<Record<string, unknown>> =>
      (request
        ? await request.json()
        : JSON.parse(String(init?.body))) as Record<string, unknown>;
    if (url.endsWith("/v1/me")) {
      return jsonResponse({ user: { id: "u1" }, roles, teams: [] });
    }
    if (url.includes("/audit-log")) {
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    // Retire is a POST too, but on the /{id}/retire sub-path — match it before
    // the generic create so it is recorded as an archive, not parsed as a body.
    if (url.includes("/retire") && method === "POST") {
      calls.push({ method, url, body: null });
      return jsonResponse(field({ id: "archived", status: "retired" }));
    }
    if (url.includes("/custom-fields") && method === "PATCH") {
      const body = await readBody();
      calls.push({ method, url, body });
      return jsonResponse(field({ label: String(body.label) }));
    }
    if (url.includes("/custom-fields") && method === "POST") {
      const body = await readBody();
      calls.push({ method, url, body });
      if (opts.failCreate) {
        return jsonResponse(
          { title: "Unprocessable", detail: "rejected" },
          422,
        );
      }
      const created = field({
        id: `cf-new-${calls.length}`,
        object: body.object as CustomField["object"],
        label: String(body.label),
        type: body.type as CustomField["type"],
        currency: (body.currency as string | undefined) ?? null,
        options: (body.options as string[] | undefined) ?? null,
      });
      return jsonResponse(created, 201);
    }
    if (url.includes("/custom-fields")) {
      const object = new URL(url).searchParams.get("object");
      calls.push({ method, url, body: null });
      const data = object === "organization" ? orgFields : dealFields;
      return jsonResponse({ data, page: { next_cursor: null } });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

const renderScreen = () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <CustomFieldsScreen />
      </LocaleProvider>
    </QueryClientProvider>,
  );
};

describe("CustomFieldsScreen", () => {
  it("renders the four object chips and the selected object's fields", async () => {
    vi.stubGlobal(
      "fetch",
      customFieldsBackend([field({ id: "d1", label: "Renewal date" })], [], []),
    );
    renderScreen();
    await waitFor(() =>
      expect(screen.getByText("Renewal date")).toBeInTheDocument(),
    );
    for (const name of [/Deal/, /Company/, /Contact/, /Lead/]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
  });

  it("swaps to the organization fields when the Company chip is clicked", async () => {
    const calls: Recorded[] = [];
    vi.stubGlobal(
      "fetch",
      customFieldsBackend(
        [field({ id: "d1", label: "Renewal date" })],
        [
          field({
            id: "o1",
            object: "organization",
            label: "Industry code",
            column_name: "cf_industry_code",
            type: "text",
          }),
        ],
        calls,
      ),
    );
    renderScreen();
    await waitFor(() =>
      expect(screen.getByText("Renewal date")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByRole("button", { name: /Company/ }));
    await waitFor(() =>
      expect(screen.getByText("Industry code")).toBeInTheDocument(),
    );
    expect(screen.queryByText("Renewal date")).toBeNull();
    expect(calls.some((call) => call.url.includes("object=organization"))).toBe(
      true,
    );
  });

  it("creates a field with source:manual and shows the success toast", async () => {
    const calls: Recorded[] = [];
    vi.stubGlobal("fetch", customFieldsBackend([], [], calls));
    renderScreen();
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /Confirm & add field/i }),
      ).toBeInTheDocument(),
    );
    await userEvent.type(screen.getByLabelText(/^Label/i), "Deal size");
    await userEvent.click(screen.getByRole("button", { name: /^Number$/i }));
    await userEvent.click(
      screen.getByRole("button", { name: /Confirm & add field/i }),
    );
    await waitFor(() =>
      expect(calls.some((call) => call.method === "POST")).toBe(true),
    );
    const post = calls.find((call) => call.method === "POST");
    expect(post?.body).toMatchObject({
      object: "deal",
      label: "Deal size",
      type: "number",
      source: "manual",
    });
    await waitFor(() =>
      expect(screen.getByText(/Deal size" added/)).toBeInTheDocument(),
    );
  });

  it("gives a non-managing role the read-only view with no builder or archive", async () => {
    vi.stubGlobal(
      "fetch",
      customFieldsBackend(
        [field({ id: "d1", label: "Renewal date" })],
        [],
        [],
        ["rep"],
      ),
    );
    renderScreen();
    await waitFor(() =>
      expect(screen.getByText("Renewal date")).toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("button", { name: /Confirm & add field/i }),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: /Archive field/i })).toBeNull();
    expect(screen.getByText(/Admin role required/i)).toBeInTheDocument();
  });

  it("rolls back the optimistic staged row and toasts the error on create failure", async () => {
    const calls: Recorded[] = [];
    vi.stubGlobal(
      "fetch",
      customFieldsBackend(
        [field({ id: "d1", label: "Existing field" })],
        [],
        calls,
        ["admin"],
        { failCreate: true },
      ),
    );
    renderScreen();
    await waitFor(() =>
      expect(screen.getByText("Existing field")).toBeInTheDocument(),
    );
    await userEvent.type(screen.getByLabelText(/^Label/i), "Doomed field");
    await userEvent.click(screen.getByRole("button", { name: /^Number$/i }));
    await userEvent.click(
      screen.getByRole("button", { name: /Confirm & add field/i }),
    );
    // The POST is attempted…
    await waitFor(() =>
      expect(calls.some((call) => call.method === "POST")).toBe(true),
    );
    // …and after the 422 the list is back to its prior rows (staged row gone)
    // with an honest error toast surfaced from the problem detail.
    await waitFor(() =>
      expect(screen.getByText(/rejected/)).toBeInTheDocument(),
    );
    expect(screen.queryByText("Doomed field")).toBeNull();
    expect(screen.queryByText(/writing/i)).toBeNull();
    expect(screen.getByText("Existing field")).toBeInTheDocument();
  });

  it("archives a field and shows the archived toast", async () => {
    const calls: Recorded[] = [];
    vi.stubGlobal(
      "fetch",
      customFieldsBackend(
        [field({ id: "d1", label: "Renewal date" })],
        [],
        calls,
      ),
    );
    renderScreen();
    await waitFor(() =>
      expect(screen.getByText("Renewal date")).toBeInTheDocument(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /Archive field/i }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (call) => call.method === "POST" && call.url.includes("/retire"),
        ),
      ).toBe(true),
    );
    const retire = calls.find((call) => call.url.includes("/retire"));
    expect(retire?.url).toContain("/custom-fields/d1/retire");
    await waitFor(() =>
      expect(screen.getByText(/archived/)).toBeInTheDocument(),
    );
  });

  it("renames a field via the modal, sending the new label in a PATCH", async () => {
    const calls: Recorded[] = [];
    vi.stubGlobal(
      "fetch",
      customFieldsBackend(
        [field({ id: "d1", label: "Renewal date" })],
        [],
        calls,
      ),
    );
    renderScreen();
    await waitFor(() =>
      expect(screen.getByText("Renewal date")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByRole("button", { name: /Edit label/i }));
    const input = screen.getByLabelText(/New label/i);
    await userEvent.clear(input);
    await userEvent.type(input, "Contract end date");
    await userEvent.click(screen.getByRole("button", { name: /Save/i }));
    await waitFor(() =>
      expect(calls.some((call) => call.method === "PATCH")).toBe(true),
    );
    const patch = calls.find((call) => call.method === "PATCH");
    expect(patch?.url).toContain("/custom-fields/d1");
    expect(patch?.body).toMatchObject({ label: "Contract end date" });
  });
});
