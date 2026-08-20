/** @vitest-environment jsdom */
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { translate } from "../i18n";
import { companyContextCapabilitiesQueryKey } from "./company-context";
import { SETTINGS_TABS, SettingsScreen, type SettingsTabId } from "./settings";
import {
  jsonResponse,
  readOn,
  render,
  renderNav,
  settingsBackend,
} from "./settings.testkit";

// WHICH settings entries a principal is offered, and which group holds each one.
// The level is composed from the SETTINGS_TABS register and the grant map /me
// carries, so every expectation here is DERIVED from that register rather than
// restated beside it: a list of labels written out by hand is a second source of
// truth, and nothing updates it.
//
// What an entry then RENDERS is `settings.test.tsx` and its siblings' subject;
// the shared fixtures are in `settings.testkit.tsx`.

// No shared fetch stub: the backend a claim needs is installed beside the claim,
// so what answered it is readable where it is asserted.
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
});

describe("SettingsScreen tab layout", () => {
  // These layout assertions run as an admin holding the org grants, so every
  // tab under test is present. Which principal sees which tab is the
  // Organization-group suite's subject, not this one's.
  beforeEach(() => {
    vi.stubGlobal("fetch", settingsBackend());
  });

  it("groups the nav into personal and organization entries, Account current by default", async () => {
    renderNav();
    // ONE navigation landmark in the chrome: the level names itself with a
    // heading rather than opening a second `nav` beside the sidebar's own.
    const nav = screen.getByRole("navigation", { name: /primary navigation/i });
    expect(
      within(nav).getByRole("heading", { level: 2, name: "Settings" }),
    ).toBeTruthy();
    // The organization entries appear once the /me role probe resolves to admin.
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Data model" })).toBeTruthy(),
    );
    // The two groups the level carries, under its own title rather than beside
    // it — the outline reads Settings → You / Organization.
    expect(
      within(nav)
        .getAllByRole("heading", { level: 3 })
        .map((heading) => heading.textContent),
    ).toEqual(["You", "Organization"]);
    for (const label of [
      "Account",
      "Writing voice",
      "Agents",
      "Connections",
      "People & access",
      "Data model",
      "Privacy & audit",
      "Maintenance",
    ]) {
      expect(screen.getByRole("link", { name: label })).toBeTruthy();
    }
    const account = screen.getByRole("link", { name: "Account" });
    expect(account.getAttribute("aria-current")).toBe("page");
    expect(
      screen.getByRole("link", { name: "Data model" }).getAttribute("href"),
    ).toBe("#/settings/data-model");
  });

  it("renders only the active entry's cards — the passport is off the Account tab", async () => {
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    // Scout lives on Agents; the default Account tab must not render it.
    expect(screen.queryByText("Scout")).toBeNull();
  });

  it("renders the custom-field editor itself on the Data model tab, never a door to it", async () => {
    render(<SettingsScreen tab="data-model" />);
    // Org entry: visible once /me resolves the custom_field read grant.
    expect(
      await screen.findByRole("heading", { name: "Custom fields" }),
    ).toBeTruthy();
    // The editor IS the content now, so nothing on the page navigates to it.
    expect(screen.queryByRole("link", { name: /custom fields/i })).toBeNull();
  });

  it("renders the pipeline, product and offer-template surfaces on the Data model tab, never doors to them", async () => {
    render(<SettingsScreen tab="data-model" />);
    expect(
      await screen.findByRole("heading", { name: "Products" }),
    ).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Pipelines" })).toBeTruthy();
    expect(
      screen.getByRole("heading", { name: "Offer templates" }),
    ).toBeTruthy();
    // Three former standalone screens are inline content: the door-cards that
    // stood in for them are gone rather than relabelled.
    const hrefs = screen
      .queryAllByRole("link")
      .map((link) => link.getAttribute("href"));
    expect(hrefs).not.toContain("#/products");
    expect(hrefs).not.toContain("#/offer-templates");
  });
});

// The nav, driven by exactly the two things the Organization group composes:
// the grant map /me carries and the company-context rollout flag. Every other
// endpoint answers empty, so a failure here can only be about visibility.
function orgNavBackend(opts: {
  roles: string[];
  allow?: GrantSpec;
  // The licensing seat, which the entry predicates deliberately leave out: a
  // read seat still READS every page behind them, so a case can name the seat
  // and expect the nav not to narrow.
  seat?: "full" | "read";
  companyReadEnabled?: boolean;
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return jsonResponse(
        meFixture({
          roles: opts.roles,
          seat: opts.seat ?? "full",
          allow: opts.allow ?? {},
        }),
      );
    }
    if (url.includes("/company/context/capabilities")) {
      const enabled = opts.companyReadEnabled ?? false;
      return jsonResponse({
        rollout: enabled ? "read" : "off",
        read_enabled: enabled,
        tasks_enabled: false,
        onboarding_enabled: false,
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

// The same backend with the rollout answer on a valve the test opens. The nav
// can then be read at two named moments — flag unanswered, flag answered "off"
// — instead of whichever of the two the event loop happens to serve first.
function orgNavBackendHoldingCapabilities(opts: {
  roles: string[];
  allow?: GrantSpec;
}) {
  const answer = orgNavBackend({ ...opts, companyReadEnabled: false });
  let release: (() => void) | undefined;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.includes("/company/context/capabilities")) {
      await held;
    }
    return answer(input);
  });
  return { fetchMock, answerCapabilities: () => release?.() };
}

// The settings entries currently in the nav, in render order — personal group
// first, then the organization group. Asserting the WHOLE list rather than one
// membership is the point: a predicate wired to the wrong object shows up as
// an extra or a missing entry, where a single getBy would pass regardless.
function navTabs(): string[] {
  return screen
    .getAllByRole("link")
    .filter((link) =>
      (link.getAttribute("href") ?? "").startsWith("#/settings/"),
    )
    .map((link) => link.textContent ?? "");
}

// The entries under ONE group heading. Each group renders its heading and its
// own links inside a single container, so the heading's parent is what says
// which entries belong to which group — the flat list above cannot tell a
// mis-grouped entry from a correctly grouped one.
function navGroupTabs(heading: HTMLElement): string[] {
  const container = heading.parentElement;
  if (!container) {
    throw new Error(`the group heading "${heading.textContent}" stands alone`);
  }
  return within(container)
    .getAllByRole("link")
    .map((link) => link.textContent ?? "");
}

// The expected labels, DERIVED from the register rather than restated beside it.
//
// This is the fix for a live hole rather than a tidy-up. The restated lists this
// replaces omitted `license` — a fourteenth entry with a register row, a
// predicate, a content component, labels in two locales and a deep link from the
// sidebar's seat meter — so every assertion in this file, including the two that
// claim to walk the whole level, was checking thirteen of fourteen entries and
// passing. A list of labels beside a list of entries is a second source of
// truth, and nothing updates it.
//
// `agents` and `connections` are in the personal group because what they carry
// is the PERSON's: gating `agents` would regress passport minting for every seat
// that is not an admin, and a mailbox and a LinkedIn network nobody else can see
// are not the installation's configuration.
const labelOf = (id: SettingsTabId) => translate("en", `settings.tab.${id}`);
const tabsIn = (group: "you" | "org") =>
  SETTINGS_TABS.filter((entry) => entry.group === group).map((entry) =>
    labelOf(entry.id),
  );

const PERSONAL_TABS = tabsIn("you");
const ORG_TABS = tabsIn("org");
const EVERY_TAB = [...PERSONAL_TABS, ...ORG_TABS];

// What a seat holding the seeded reads and no admin role reaches: everything
// except the two entries that ask for a grant only admin and ops hold — the
// reindex read behind Maintenance, and `license:read` behind License (core
// migration 0261 grants it to admin and ops, and to nobody else).
//
// Named rather than sliced. The old form took the tail off the list and called
// it "every entry but Maintenance", which was true only while Maintenance was
// declared last — and the moment License landed beside it, the same slice
// silently claimed a read seat could reach the licensing page.
const ADMIN_ONLY_TABS = [labelOf("license"), labelOf("maintenance")];
const SEAT_READ_TABS = EVERY_TAB.filter(
  (tab) => !ADMIN_ONLY_TABS.includes(tab),
);

// What mere membership buys: the ONE Organization entry with no grant to ask for.
// No RBAC object describes identity administration and none can, and `GET /users`
// answers 200 to any authenticated principal — so the nav admits everybody, as
// the server does.
//
// Privacy is deliberately NOT here. `consent_config` is absent from the shipped
// vocabulary, but the registry's server gate is not a role either: ListPurposes
// demands `person:read`, so that is what the entry asks for. Every seeded role
// holds it; a principal holding nothing does not.
const MEMBER_TABS = [...PERSONAL_TABS, "People & access"];
const MEMBER_TABS_WITH_PRIVACY = [...MEMBER_TABS, "Privacy & audit"];

// Membership's two entries plus Maintenance, which is what EITHER half of that
// entry's predicate buys on its own — the admin role, or the reindex read an
// edited role can hold without it. Both halves are asserted against this list.
const MEMBER_TABS_WITH_MAINTENANCE = [
  ...MEMBER_TABS_WITH_PRIVACY,
  "Maintenance",
];

// Every entry open at once: the admin role for Maintenance, and one read apiece
// for the entries that follow an object. `license` belongs here for the same
// reason as the rest — it was the omission that made the whole-level assertions
// pass against thirteen of fourteen entries.
const EVERY_TAB_GRANTED: GrantSpec = {
  person: ["read"],
  installation_settings: ["read"],
  webhook_subscription: ["read"],
  capture_settings: ["read"],
  custom_field: ["read"],
  automation: ["read"],
  license: ["read"],
};

// The four reads Data model unions. Each has to open the page alone: an entry
// wired to one object with three decorative terms passes any fixture that grants
// all four.
const DATA_MODEL_READS = [
  "custom_field",
  "pipeline",
  "product",
  "offer_template",
] as const;

// The seeded grant matrix, READ verbs only — the only verb an entry's predicate
// asks for. manager, read_only and rep hold the identical ten reads and differ
// only in the writes on top, which is exactly why write-shaped predicates hid
// pages the server serves: the differentiation the matrix carries lives in the
// writes, and a write is not what opens a page.
const SEEDED_READS: GrantSpec = {
  automation: ["read"],
  person: ["read"],
  capture_settings: ["read"],
  custom_field: ["read"],
  installation_settings: ["read"],
  offer_template: ["read"],
  organization: ["read"],
  overlay_connection: ["read"],
  pipeline: ["read"],
  product: ["read"],
  webhook_subscription: ["read"],
};

// What the matrix adds for ops: the objects it shares with admin alone.
// `embedding_reindex` among them is what opens Maintenance, and `license` — which
// core migration 0261 grants to admin and ops and to nobody else — is what opens
// License. That last one was missing here, which is why an ops seat's licensing
// page had no test at all.
const SEEDED_OPS_READS: GrantSpec = {
  ...SEEDED_READS,
  ai_model_rate: ["read"],
  embedding_reindex: ["read"],
  fx_rate: ["read"],
  retention_policy: ["read"],
  license: ["read"],
};

describe("SettingsScreen Organization group", () => {
  // The group is composed from its members: an entry appears when the principal
  // may READ some part of it — opening a page is reading it — or, for the two
  // surfaces with no RBAC object, on membership alone. The write affordances
  // inside each entry gate themselves, so no case below needs a write to reach a
  // page and none of them proves anything by granting one.

  it("renders every entry in its declared order, split across the two groups", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["admin"], allow: EVERY_TAB_GRANTED }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(EVERY_TAB));
    // And each half is under the heading that claims it: the flat order above
    // would read the same if an entry were declared in the wrong group.
    const nav = screen.getByRole("navigation", { name: /primary navigation/i });
    const headings = within(nav).getAllByRole("heading", { level: 3 });
    // Asserted before either heading is read, so a level that lost a group
    // fails on the missing heading rather than on a lookup inside it.
    expect(headings.map((heading) => heading.textContent)).toEqual([
      "You",
      "Organization",
    ]);
    const [you, org] = headings;
    expect(navGroupTabs(you)).toEqual(PERSONAL_TABS);
    expect(navGroupTabs(org)).toEqual(ORG_TABS);
  });

  it("gives a principal holding no read at all the two entries that ask for none", async () => {
    // People & access and Privacy & audit have no grant to ask for: the member
    // roster answers 200 to any authenticated principal, and `consent_config` is
    // not in the shipped RBAC vocabulary. So they are the floor of this level
    // rather than a case — every gated member is gone here, and those two stay.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["rep"] }));
    renderNav();
    // /me has to have SETTLED before this claim means anything: a nav read
    // mid-flight is empty for every principal. Waiting on the two entries
    // themselves is what proves it settled — the sidebar no longer prints the
    // signed-in address, which is what this used to wait for, because the
    // account block moved to the top bar.
    await waitFor(() => expect(navTabs()).toEqual(MEMBER_TABS));
  });

  it.each(DATA_MODEL_READS)(
    "opens Data model for a lone %s read",
    async (object) => {
      const allow = readOn(object);
      vi.stubGlobal("fetch", orgNavBackend({ roles: ["rep"], allow }));
      renderNav();
      await waitFor(() =>
        expect(navTabs()).toEqual([
          ...PERSONAL_TABS,
          "People & access",
          "Data model",
          "Privacy & audit",
        ]),
      );
    },
  );

  it.each(["webhook_subscription", "overlay_connection"] as const)(
    "opens Integrations for a lone %s read",
    async (object) => {
      // The installation's outside wiring was half of the entry Connections used
      // to be, and the system-of-record chip in the topbar points every seat at
      // it — so either read has to open it on its own, or whoever follows that
      // chip lands on the Account fallback.
      const allow = readOn(object);
      vi.stubGlobal("fetch", orgNavBackend({ roles: ["rep"], allow }));
      renderNav();
      await waitFor(() =>
        expect(navTabs()).toEqual([
          ...PERSONAL_TABS,
          "People & access",
          "Integrations",
          "Privacy & audit",
        ]),
      );
    },
  );

  it("opens Capture for a lone capture_settings read", async () => {
    // Two surfaces both called "Capture" became one page, and this is the read
    // the merged page asks for. Granted alone so a Capture wired to a
    // neighbouring object, or a neighbour wired to this one, shows up as an
    // entry the whole-list assertion does not expect.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: readOn("capture_settings") }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "People & access",
        "Capture",
        "Privacy & audit",
      ]),
    );
  });

  it("opens Maintenance for a lone embedding_reindex read, for a principal who is no admin", async () => {
    // The reindex moved to Maintenance and kept its object: taking the entry away
    // from a principal who could reach the verb before would be a regression
    // dressed as a tidy-up. It is also the term that lets Maintenance open for
    // someone who is not an admin, which is the half of that predicate a role
    // check could never express.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["rep"],
        allow: readOn("embedding_reindex"),
      }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual(MEMBER_TABS_WITH_MAINTENANCE),
    );
  });

  it("opens General for a lone fx_rate read, and no other entry with it", async () => {
    // The currency table joined the base currency it converts to, so fx_rate is
    // one of the three terms General's predicate unions — this read alone has to
    // open it, and the neighbouring entries have to stay shut.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: readOn("fx_rate") }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "General",
        "People & access",
        "Privacy & audit",
      ]),
    );
  });

  it("opens AI for a lone ai_model_rate read", async () => {
    // Model prices joined the AI runtime they price, and either term of that
    // entry's predicate opens it on its own — so the union has to be read as a
    // union and not as one object with a decorative second term.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["rep"], allow: readOn("ai_model_rate") }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([
        ...PERSONAL_TABS,
        "People & access",
        "AI",
        "Privacy & audit",
      ]),
    );
  });

  // THE REGRESSION THIS RULE EXISTS TO PREVENT. Measured against the live API,
  // the write-shaped predicates hid a read-only seat from eight of the eleven
  // entries the server answers 200 on — three of which (products, offer
  // templates, custom fields) were ungated routes of their own before the merge.
  // A client that hides a page the server serves is not protecting anything; it
  // is disagreeing with the authority.
  //
  // The licensing seat is named here too, and must not narrow the level either: a
  // read seat READS every page behind these entries, and the server clamps it on
  // the write.
  it("reaches every entry a read seat is granted, and neither admin-only one", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["read_only"],
        seat: "read",
        allow: SEEDED_READS,
      }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(SEAT_READ_TABS));
  });

  it.each(["manager", "rep"] as const)(
    "reaches the same entries for a seeded %s, whose extra writes buy no page",
    async (role) => {
      vi.stubGlobal(
        "fetch",
        orgNavBackend({ roles: [role], allow: SEEDED_READS }),
      );
      renderNav();
      await waitFor(() => expect(navTabs()).toEqual(SEAT_READ_TABS));
    },
  );

  it("reaches every entry for a seeded ops, whose reindex and licence reads open the last two", async () => {
    // The two entries that genuinely narrow, and they narrow to admin/ops rather
    // than to admin: ops holds both the reindex read and `license:read`, so each
    // opens on its grant and not on a role name — which is what lets an edited
    // role holding the same read reach them too.
    vi.stubGlobal(
      "fetch",
      orgNavBackend({ roles: ["ops"], allow: SEEDED_OPS_READS }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(EVERY_TAB));
  });

  it("adds Maintenance for an admin holding no read at all, and loses Privacy with it", async () => {
    // The role half of Maintenance's predicate, on its own: an admin whose grants
    // were all revoked still administers the installation, and the danger zone
    // inside asks for that same role.
    //
    // Privacy goes, and that is the point of asking for a grant rather than
    // assuming membership: the consent registry's server gate is `person:read`, so
    // an admin stripped of it would reach a page of four refusals. The entry
    // follows the grant, not the role.
    vi.stubGlobal("fetch", orgNavBackend({ roles: ["admin"] }));
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([...MEMBER_TABS, "Maintenance"]),
    );
  });

  it("shows General to an admin holding the organization read once the company rollout flag is on", async () => {
    vi.stubGlobal(
      "fetch",
      orgNavBackend({
        roles: ["admin"],
        allow: readOn("organization"),
        companyReadEnabled: true,
      }),
    );
    renderNav();
    expect(await screen.findByRole("link", { name: "General" })).toBeTruthy();
  });

  it("withholds General from that same admin while the rollout flag is off — before the flag answers and after", async () => {
    // The flag is a deployment posture, not a permission, so it ANDs with the
    // grant beside it: the company profile may simply not exist on this
    // installation. An unknown flag therefore reads as "off" — an entry that
    // appears while the answer is in flight and then vanishes has already offered
    // a surface this installation may not have.
    //
    // The organization read is the ONLY term of General's predicate this fixture
    // grants, which is what leaves the flag decisive. On a seeded installation
    // every role also holds `installation_settings:read` and General opens on
    // that regardless — so this case is about the flag's contribution to the
    // union, not a claim that General is ever unreachable in practice.
    const { fetchMock, answerCapabilities } = orgNavBackendHoldingCapabilities({
      roles: ["admin"],
      allow: readOn("organization"),
    });
    vi.stubGlobal("fetch", fetchMock);
    const { client } = renderNav();

    // Moment one: the nav is fully composed from /me — its role-gated entries
    // are on screen — while the flag is still unanswered, because this test
    // holds the answer.
    await screen.findByRole("link", { name: "Maintenance" });
    expect(navTabs()).toEqual(MEMBER_TABS_WITH_MAINTENANCE);

    // Moment two: the answer is in the cache, which is the fact the emptiness
    // claim needs — the request having been SENT proves nothing about what the
    // nav has rendered.
    answerCapabilities();
    await waitFor(() =>
      expect(
        client.getQueryState(companyContextCapabilitiesQueryKey)?.status,
      ).toBe("success"),
    );
    expect(navTabs()).toEqual(MEMBER_TABS_WITH_MAINTENANCE);
  });
});
