/** @vitest-environment jsdom */
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { pickOption } from "../design-system/select-testing";
import { LOCALES, localeNameKey, translate } from "../i18n";
import { SettingsScreen, settingsAddress } from "./settings";
import {
  auditEntry,
  IDLE_JOB_HEALTH,
  jsonResponse,
  readOn,
  render,
  renderSettings,
  settingsBackend,
} from "./settings.testkit";

// What a principal is SERVED once an entry opens: the identity and locale
// surfaces on Account, the rows that read as WITHHELD rather than absent, and
// the merged entries carrying the surfaces their parts brought with them.
//
// The rest of this screen's coverage sits beside it, one subject per file —
// `settings-nav` (which entries a principal is offered at all),
// `settings-agents`, `settings-integrations`, `settings-pipelines`,
// `settings-maintenance` and `settings-audit`. The shared fixtures are in
// `settings.testkit.tsx`.

// The settings identity + passport surfaces through the RBAC primitives:
// roles render as localized RoleBadges (a workspace-defined key stays raw),
// and the passport list's token slot reads as WITHHELD (FieldGuard mask) —
// the wire schema carries no token, and the row says so instead of omitting
// the field as if none existed.

beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  vi.stubGlobal("fetch", settingsBackend());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
});

describe("SettingsScreen RBAC surfaces", () => {
  it("renders the session roles as localized badges on the default Account tab; a custom key stays its raw self", async () => {
    render(<SettingsScreen route={settingsAddress()} />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    expect(screen.getByText("Admin")).toBeTruthy();
    expect(screen.getByText("field_marketing")).toBeTruthy();
    // the seeded key never leaks raw once a label exists
    expect(screen.queryByText("admin")).toBeNull();
  });

  // Appearance is chosen from the account menu, not from here: it is the
  // setting a reader changes most often and from wherever they are standing.
  // Language stays, so the claim is that the account card lost ONE control
  // rather than that the surface went away — and it is made against the
  // rendered page, because an import that no longer exists is not evidence
  // about what a reader sees.
  it("offers no theme control on the Account tab", async () => {
    render(<SettingsScreen route={settingsAddress()} />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());

    expect(screen.getByRole("heading", { name: "Your account" })).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Language" })).toBeTruthy();
    for (const name of ["Light", "Dark", "System", "Theme"]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
      expect(screen.queryByRole("group", { name })).toBeNull();
    }
  });

  // Identity, credential, sign-off and language are ONE card, not four: a
  // reader auditing their own account reads one title and finds three answers
  // at one x. The claim is about the three ROWS being there, in the one card —
  // a page that grew a second panel back would still pass a query for any one
  // of them on its own.
  it("carries the identity, the password, the signature and the language in ONE card", async () => {
    render(<SettingsScreen route={settingsAddress()} />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());

    const card = screen
      .getByRole("heading", { name: "Your account" })
      .closest("section");
    if (!(card instanceof HTMLElement)) {
      throw new Error("the account card is not a section");
    }
    // The identity block, and the three verbs/answers that belong to it.
    expect(within(card).getByText("ada@acme.test")).toBeTruthy();
    expect(
      within(card).getByRole("button", { name: "Change password" }),
    ).toBeTruthy();
    expect(
      within(card).getByRole("button", { name: "Edit signature" }),
    ).toBeTruthy();
    expect(
      within(card).getByRole("combobox", { name: "Language" }),
    ).toBeTruthy();
    // And nothing else on the tab: four panels became one.
    expect(screen.getAllByRole("heading", { level: 2 })).toHaveLength(1);
  });

  it("switches the language from the Account tab, through the design-system select", async () => {
    const user = userEvent.setup();
    render(<SettingsScreen route={settingsAddress()} />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());

    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Language" }),
      "Deutsch",
    );
    // The choice reaches the chrome around the control, not just the control's
    // own face — which is the whole point of changing a language here.
    expect(screen.getByRole("combobox", { name: "Sprache" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Ihr Konto" })).toBeTruthy();
  });

  // WCAG 2.2 AA 3.1.2. This is the one picker in the product where every option
  // is deliberately in a language other than the page's, so a screen reader has
  // to be told which. Derived from LOCALES rather than listed, the way the login
  // footer's own coverage is: a hardcoded pair keeps passing after a fourth
  // language is added without one.
  it("declares each language name's own language, on the options and on the face", async () => {
    const user = userEvent.setup();
    render(<SettingsScreen route={settingsAddress()} />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    const trigger = screen.getByRole("combobox", { name: "Language" });

    expect(trigger.querySelector(".select-face")?.getAttribute("lang")).toBe(
      "en",
    );
    await user.click(trigger);

    for (const locale of LOCALES) {
      const name = translate("en", localeNameKey(locale));
      const option = screen.getByRole("option", { name });
      expect(
        option.querySelector(".select-option-label")?.getAttribute("lang"),
      ).toBe(locale);
    }
  });

  it("the passport row's token reads as withheld — masked, never re-disclosed — on the Agents tab", async () => {
    render(<SettingsScreen route={settingsAddress("agents")} />);
    await waitFor(() => expect(screen.getByText("Scout")).toBeTruthy());
    expect(screen.getByRole("img", { name: "Masked value" })).toBeTruthy();
    expect(screen.queryByText(/mgp_/)).toBeNull();
  });

  // The spend cards follow `automation:update` rather than any AI-named object,
  // so the principal here holds the model-price grants that open the AI entry and
  // nothing else: the read reaches the page, the write authors the price table on
  // it, and the two cards whose endpoints would 403 stay off it.
  //
  // An OPERATOR seat, because the AI entry is in the admin group and nobody
  // outside that seat reaches it at all — the claim under test is about the
  // automation grant, so the seat is the floor it stands on.
  it("withholds the AI spend and call trace from a principal without the automation grant", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return jsonResponse(
            meFixture({
              roles: ["ops"],
              allow: { ai_model_rate: ["read", "update"] },
            }),
          );
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    render(<SettingsScreen route={settingsAddress("ai")} />);
    // The model prices this grant authors are on screen, so the tab rendered...
    await waitFor(() =>
      expect(screen.getByText("AI model costs")).toBeTruthy(),
    );
    // ...and the two cards whose endpoints require the automation grant KEEP
    // their place and say they are withheld. Absent, they would claim the
    // installation had spent nothing and made no model calls — a statement about
    // the data, where the truth is only about who may read it. No request is made
    // for either, so a rep never hits a 403 error box (GET /ai/usage, /ai/calls).
    expect(await screen.findByText("AI usage & budget")).toBeTruthy();
    expect(
      await screen.findByText(
        /only an operator can see what the AI runtime spent/i,
      ),
    ).toBeTruthy();
    expect(screen.getByText("AI call trace")).toBeTruthy();
    expect(
      screen.getByText(/only an operator can read the per-call trace/i),
    ).toBeTruthy();
  });
});

// A reindex marker with work waiting, so the search-index card has a state to
// report rather than a shapeless payload to guess at.
const REINDEX_STATUS = {
  configured_identity: "anthropic/voyage-3@1024",
  populated_identity: "anthropic/voyage-2@1024",
  status: "idle",
  updated_at: "2026-07-21T12:00:00Z",
  reindex_needed: true,
  entities_pending: 42,
  per_workspace: [{ entities_pending: 42 }],
};

// Every read the three restructured entries make, answered honestly in one
// place: the passports Agents lists, the consent registry and audit trail
// Privacy & audit now share, and the two operational reports on Maintenance.
function mergedEntryBackend(opts: {
  roles: string[];
  seat?: "full" | "read";
  allow?: GrantSpec;
  dataResetAvailable?: boolean;
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      const me = meFixture({
        roles: opts.roles,
        seat: opts.seat ?? "full",
        allow: opts.allow ?? {},
      });
      return jsonResponse({
        ...me,
        workspace_name: "Acme Inc",
        data_reset_available: opts.dataResetAvailable ?? false,
      });
    }
    if (url.includes("/passports")) {
      return jsonResponse({
        data: [
          {
            id: "pp-1",
            label: "Scout",
            scopes: ["read"],
            created_at: "2026-07-01T08:00:00Z",
            expires_at: null,
            revoked_at: null,
          },
        ],
        page: { next_cursor: null, has_more: false },
      });
    }
    if (url.includes("/audit-log")) {
      return jsonResponse({
        data: [auditEntry],
        page: { next_cursor: null, has_more: false },
      });
    }
    if (url.includes("/admin/job-health")) {
      return jsonResponse(IDLE_JOB_HEALTH);
    }
    if (url.includes("/embeddings/reindex/status")) {
      return jsonResponse(REINDEX_STATUS);
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

// The entries the restructure created or merged into, read as CONTENT: a merged
// page has to carry the surfaces its parts brought, and the personal one has to
// open for a seat no grant would have admitted.
describe("SettingsScreen restructured entries", () => {
  it("opens Agents for a read-only seat, passports and all", async () => {
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({ roles: ["rep"], seat: "read" }),
    );
    renderSettings("agents");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Agents" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // A passport is lent by the HUMAN who minted it, so the surface that mints
    // and lists one opens for a seat holding no org grant and no writing
    // licence at all — gating it behind the org group would have meant only
    // admins could lend.
    expect(
      await screen.findByRole("heading", { name: "Agent passports" }),
    ).toBeTruthy();
    expect(screen.getByText("Scout")).toBeTruthy();
    // And the autonomy table the passports sit under, which came off the
    // organization's AI entry with them.
    expect(
      screen.getByRole("heading", { name: "Autonomy tiers" }),
    ).toBeTruthy();
  });

  it("renders the audit trail beside the consent registry on Privacy & audit", async () => {
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({ roles: ["admin"], allow: readOn("person") }),
    );
    renderSettings("privacy");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Privacy & audit" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // The purpose registry the entry has always carried...
    expect(
      await screen.findByRole("heading", { name: "Consent purposes" }),
    ).toBeTruthy();
    // ...and the trail that proves those purposes were honoured, which had a
    // tab of its own before: its filters — in a disclosure now, closed on
    // arrival — and an entry answering them.
    expect(
      await screen.findByRole("heading", { name: "Audit log" }),
    ).toBeTruthy();
    expect(screen.getByLabelText("Actor").closest("details")).not.toBeNull();
    expect(screen.getByText("update")).toBeTruthy();
  });

  // The READ alone opens it, editor included. Before this page absorbed it the
  // automations editor was a route of its own that nothing gated, so gating the
  // merged entry on the WRITE grant would be the merge inheriting the spend
  // cards' authority and dropping the door's — an operator who may read the
  // automations would reach a page they cannot open.
  it("opens AI for an operator on the automations read alone, editor and all", async () => {
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({ roles: ["ops"], allow: { automation: ["read"] } }),
    );
    renderSettings("ai");
    await waitFor(() =>
      expect(
        screen.getByRole("link", { name: "AI" }).getAttribute("aria-current"),
      ).toBe("page"),
    );
    // The surface they came for.
    expect(
      await screen.findByRole("heading", { name: "Automations" }),
    ).toBeTruthy();
    // The spend card follows the automation WRITE grant, so this seat is not
    // handed the operator's bill — but it is told that, rather than left to read
    // an absent card as "nothing was spent".
    expect(
      screen.getByRole("heading", { name: "AI usage & budget" }),
    ).toBeTruthy();
    expect(
      screen.getByText(/only an operator can see what the AI runtime spent/i),
    ).toBeTruthy();
  });

  // The trail is the admin's alone, and this page opens for OPS — the consent
  // registry above it is theirs. Before the merge the audit log was an entry of
  // its own, gated on the admin role by the nav; merging it onto a page ops
  // reaches moved that gate's job into the card, and nothing was doing it.
  it("withholds the audit trail from an ops seat, and asks the server for nothing", async () => {
    const backend = mergedEntryBackend({
      roles: ["ops"],
      allow: readOn("person"),
    });
    vi.stubGlobal("fetch", backend);
    renderSettings("privacy");
    // Ops reaches the page for the registry, which renders.
    expect(
      await screen.findByRole("heading", { name: "Consent purposes" }),
    ).toBeTruthy();
    // The trail keeps its place and says why it is empty — absent, it would read
    // as "nothing has happened here", a different claim entirely.
    expect(
      await screen.findByRole("heading", { name: "Audit log" }),
    ).toBeTruthy();
    expect(
      screen.getByText(/only an admin can read the full trail/i),
    ).toBeTruthy();
    // Six inputs that narrow a list you cannot see are a control with nothing
    // behind it, so the filter disclosure is absent rather than withheld.
    expect(screen.queryByLabelText("Actor")).toBeNull();
    // And the request is never issued: it could only ever come back 403, and a
    // red failure with a futile Retry is what the withheld body replaces.
    const asked = backend.mock.calls.map((call) =>
      String(call[0] instanceof Request ? call[0].url : call[0]),
    );
    expect(asked.some((url) => url.includes("/audit-log"))).toBe(false);
  });

  // The two admin-ONLY surfaces inside Maintenance, from an ops seat that reaches
  // the page. The seat gate admits the whole admin group for ops, so this is what
  // proves it did not also hand over what the server spells with RequireAdmin:
  // job health and the danger zone are withheld INSIDE the page the reindex read
  // opened.
  it("renders the reindex on Maintenance for an operator holding only that grant, and withholds job health and the danger zone", async () => {
    vi.stubGlobal(
      "fetch",
      mergedEntryBackend({
        roles: ["ops"],
        allow: { embedding_reindex: ["read", "update"] },
        // The switch the danger zone's second gate asks for, so the ROLE is
        // the only thing left holding it back below.
        dataResetAvailable: true,
      }),
    );
    renderSettings("maintenance");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Maintenance" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // The verb this grant buys, which used to hide beside the field editor.
    expect(
      await screen.findByRole("heading", { name: "Search index" }),
    ).toBeTruthy();
    // The job report keeps its place and withholds its content: the endpoint is
    // the admin's, and an absent card here would read as "nothing is queued".
    expect(
      screen.getByRole("heading", { name: "Background jobs" }),
    ).toBeTruthy();
    expect(
      screen.getByText(/Only an admin can see background-job health/),
    ).toBeTruthy();
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });
});
