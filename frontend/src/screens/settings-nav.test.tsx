/** @vitest-environment jsdom */
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { translate } from "../i18n";
import { companyContextCapabilitiesQueryKey } from "./company-context";
import {
  SETTINGS_TABS,
  SettingsScreen,
  type SettingsTabId,
  settingsAddress,
} from "./settings";
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
  // These layout assertions run as an admin holding the admin-group grants, so
  // every tab under test is present. Which principal sees which tab is the
  // Admin-group suite's subject, not this one's.
  beforeEach(() => {
    vi.stubGlobal("fetch", settingsBackend());
  });

  it("groups the nav into personal and admin entries, Account current by default", async () => {
    renderNav();
    // ONE navigation landmark in the chrome: the level names itself with a
    // heading rather than opening a second `nav` beside the sidebar's own.
    const nav = screen.getByRole("navigation", { name: /primary navigation/i });
    expect(
      within(nav).getByRole("heading", { level: 2, name: "Settings" }),
    ).toBeTruthy();
    // The admin entries appear once the /me probe resolves the operator seat.
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Data model" })).toBeTruthy(),
    );
    // The two groups the level carries, under its own title rather than beside
    // it — the outline reads Settings → You / Admin settings.
    expect(
      within(nav)
        .getAllByRole("heading", { level: 3 })
        .map((heading) => heading.textContent),
    ).toEqual(["You", "Admin settings"]);
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
    // A personal row addresses the level's own depth; an admin row addresses one
    // segment deeper, which is where its page now lives.
    expect(account.getAttribute("href")).toBe("#/settings/account");
    expect(
      screen.getByRole("link", { name: "Data model" }).getAttribute("href"),
    ).toBe("#/settings/admin/data-model");
  });

  it("renders only the active entry's cards — the passport is off the Account tab", async () => {
    render(<SettingsScreen route={settingsAddress()} />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    // Scout lives on Agents; the default Account tab must not render it.
    expect(screen.queryByText("Scout")).toBeNull();
  });

  it("renders the custom-field editor itself on the Data model tab, never a door to it", async () => {
    render(<SettingsScreen route={settingsAddress("data-model")} />);
    // Org entry: visible once /me resolves the custom_field read grant.
    expect(
      await screen.findByRole("heading", { name: "Custom fields" }),
    ).toBeTruthy();
    // The editor IS the content now, so nothing on the page navigates to it.
    expect(screen.queryByRole("link", { name: /custom fields/i })).toBeNull();
  });

  it("renders the pipeline, product and offer-template surfaces on the Data model tab, never doors to them", async () => {
    render(<SettingsScreen route={settingsAddress("data-model")} />);
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

// The nav, driven by exactly the three things the Admin settings group composes:
// the seat /me reports, the grant map beside it, and the company-context rollout
// flag. Every other endpoint answers empty, so a failure here can only be about
// visibility.
function adminNavBackend(opts: {
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
function adminNavBackendHoldingCapabilities(opts: {
  roles: string[];
  allow?: GrantSpec;
}) {
  const answer = adminNavBackend({ ...opts, companyReadEnabled: false });
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
// first, then the admin group. Asserting the WHOLE list rather than one
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
const tabsIn = (group: "you" | "admin") =>
  SETTINGS_TABS.filter((entry) => entry.group === group).map((entry) =>
    labelOf(entry.id),
  );

const PERSONAL_TABS = tabsIn("you");
const ADMIN_TABS = tabsIn("admin");
const EVERY_TAB = [...PERSONAL_TABS, ...ADMIN_TABS];

// What an OPERATOR holding only the reads every seeded role holds reaches:
// everything except the two entries that ask for a grant only admin and ops hold
// — the reindex read behind Maintenance, and `license:read` behind License (core
// migration 0261 grants it to admin and ops, and to nobody else).
//
// Named rather than sliced. The old form took the tail off the list and called
// it "every entry but Maintenance", which was true only while Maintenance was
// declared last — and the moment License landed beside it, the same slice
// silently claimed a seat without the grant could reach the licensing page.
const ADMIN_ONLY_TABS = [labelOf("license"), labelOf("maintenance")];
const SHARED_READ_TABS = EVERY_TAB.filter(
  (tab) => !ADMIN_ONLY_TABS.includes(tab),
);

// What the operator SEAT alone buys: the ONE admin entry with no grant to ask
// for. No RBAC object describes identity administration and none can, and
// `GET /users` answers 200 to any authenticated principal — so within the group
// the nav admits every operator, as the server does.
//
// The seat is the floor now, not membership: a rep or a manager reaches none of
// these, whatever they hold. That is the OPERATOR_ prefix's whole content.
//
// Privacy is deliberately NOT here. `consent_config` is absent from the shipped
// vocabulary, but the registry's server gate is not a role either: ListPurposes
// demands `person:read`, so that is what the entry asks for. Every seeded role
// holds it; a principal holding nothing does not.
const OPERATOR_TABS = [...PERSONAL_TABS, "People & access"];
const OPERATOR_TABS_WITH_PRIVACY = [...OPERATOR_TABS, "Privacy & audit"];

// The seat's two entries plus Maintenance, which is what EITHER half of that
// entry's predicate buys on its own — the admin role, or the reindex read an
// edited role can hold without it. Both halves are asserted against this list.
const OPERATOR_TABS_WITH_MAINTENANCE = [
  ...OPERATOR_TABS_WITH_PRIVACY,
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

describe("SettingsScreen Admin settings group", () => {
  // TWO gates, and every case below names both. The SEAT decides whether this
  // reader administers the installation at all — admin or ops, and nobody else
  // — and each entry's own READ grant then decides whether that page has
  // anything in it for them. Opening a page is reading it, so every predicate
  // asks for a read; the write affordances inside gate themselves, and no case
  // here reaches a page by granting one.
  //
  // A case about ONE grant therefore runs on an operator seat: the claim is
  // still "this grant is what opens this entry", and the seat is the floor it
  // stands on rather than part of what is being proved.

  it("renders every entry in its declared order, split across the two groups", async () => {
    vi.stubGlobal(
      "fetch",
      adminNavBackend({ roles: ["admin"], allow: EVERY_TAB_GRANTED }),
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
      "Admin settings",
    ]);
    const [you, admin] = headings;
    expect(navGroupTabs(you)).toEqual(PERSONAL_TABS);
    expect(navGroupTabs(admin)).toEqual(ADMIN_TABS);
  });

  it("gives an operator holding no read at all the one entry that asks for none", async () => {
    // People & access has no grant to ask for: the member roster answers 200 to
    // any authenticated principal and no RBAC object describes identity
    // administration. So it is the floor of this group for an operator rather
    // than a case — every gated member is gone here, and that one stays.
    //
    // Privacy is NOT on the floor with it: the consent registry's server gate is
    // `person:read`, and a principal holding nothing does not hold it.
    vi.stubGlobal("fetch", adminNavBackend({ roles: ["ops"] }));
    renderNav();
    // /me has to have SETTLED before this claim means anything: a nav read
    // mid-flight is empty for every principal. Waiting on the entries themselves
    // is what proves it settled — the sidebar no longer prints the signed-in
    // address, which is what this used to wait for, because the account block
    // moved to the top bar.
    await waitFor(() => expect(navTabs()).toEqual(OPERATOR_TABS));
  });

  // The seat, on its own, from the other side. This principal holds every read
  // the admin group's entries ask for — the ops matrix entire — and reaches NONE
  // of them, because the group is not theirs to open. The heading is what makes
  // it a claim about the group rather than about nine predicates: an empty
  // "Admin settings" panel would say the installation has settings this reader
  // may not touch, where the truth is that configuring it is not their job.
  it.each(["manager", "rep"] as const)(
    "offers a seeded %s no admin group at all, holding every read it asks for",
    async (role) => {
      vi.stubGlobal(
        "fetch",
        adminNavBackend({ roles: [role], allow: SEEDED_OPS_READS }),
      );
      const { client } = renderNav();
      // The ANSWER has to be in the cache before an absence claim means
      // anything. Every other case here waits on an entry appearing, which is
      // its own proof that /me settled; this one expects no entry to appear, and
      // a nav read taken mid-flight looks exactly like the result it wants.
      await waitFor(() =>
        expect(client.getQueryState(["me"])?.status).toBe("success"),
      );
      expect(navTabs()).toEqual(PERSONAL_TABS);
      const nav = screen.getByRole("navigation", {
        name: /primary navigation/i,
      });
      expect(
        within(nav)
          .getAllByRole("heading", { level: 3 })
          .map((heading) => heading.textContent),
      ).toEqual(["You"]);
    },
  );

  // A write is still not what opens a page, inside the group as it was outside
  // it: this operator may AUTHOR custom fields and holds no read anywhere, and
  // the data-model row stays shut. The affordance the write buys is on the page,
  // and the page is reached by reading it.
  it("opens no entry for an operator holding writes and no read", async () => {
    vi.stubGlobal(
      "fetch",
      adminNavBackend({
        roles: ["ops"],
        allow: { custom_field: ["create", "update"] },
      }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(OPERATOR_TABS));
  });

  it.each(DATA_MODEL_READS)(
    "opens Data model for a lone %s read",
    async (object) => {
      const allow = readOn(object);
      vi.stubGlobal("fetch", adminNavBackend({ roles: ["ops"], allow }));
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
      vi.stubGlobal("fetch", adminNavBackend({ roles: ["ops"], allow }));
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
      adminNavBackend({ roles: ["ops"], allow: readOn("capture_settings") }),
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
    // an operator who is not an admin — ops here — which is the half of that
    // predicate a role check could never express.
    vi.stubGlobal(
      "fetch",
      adminNavBackend({
        roles: ["ops"],
        allow: readOn("embedding_reindex"),
      }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual(OPERATOR_TABS_WITH_MAINTENANCE),
    );
  });

  it("opens General for a lone fx_rate read, and no other entry with it", async () => {
    // The currency table joined the base currency it converts to, so fx_rate is
    // one of the three terms General's predicate unions — this read alone has to
    // open it, and the neighbouring entries have to stay shut.
    vi.stubGlobal(
      "fetch",
      adminNavBackend({ roles: ["ops"], allow: readOn("fx_rate") }),
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
      adminNavBackend({ roles: ["ops"], allow: readOn("ai_model_rate") }),
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

  // THE LICENSING SEAT, which is a THIRD axis and gates none of this: the server
  // clamps a read seat on the HTTP method, so it still READS every page behind
  // these entries. An operator on a read seat therefore reaches the level
  // undiminished, and the withheld things inside are the write controls.
  //
  // Named as its own case because folding it into the entry predicates is the
  // regression this rule exists to prevent: measured against the live API, the
  // write-shaped predicates hid a read seat from eight of the eleven entries the
  // server answers 200 on — three of which (products, offer templates, custom
  // fields) were ungated routes of their own before the merge.
  it("narrows nothing for an operator on a read seat", async () => {
    vi.stubGlobal(
      "fetch",
      adminNavBackend({
        roles: ["ops"],
        seat: "read",
        allow: SEEDED_OPS_READS,
      }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(EVERY_TAB));
  });

  // The grant still decides INSIDE the group, which is what keeps the seat from
  // becoming the only gate: this operator holds exactly the reads every seeded
  // role holds, so the two entries whose grants belong to admin and ops alone —
  // the reindex read, `license:read` — are the two it loses, and nothing else
  // moves.
  it("withholds License and Maintenance from an operator holding only the shared reads", async () => {
    vi.stubGlobal(
      "fetch",
      adminNavBackend({ roles: ["ops"], allow: SEEDED_READS }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(SHARED_READ_TABS));
  });

  it("reaches every entry for a seeded ops, whose reindex and licence reads open the last two", async () => {
    // The two entries that genuinely narrow, and they narrow to admin/ops rather
    // than to admin: ops holds both the reindex read and `license:read`, so each
    // opens on its grant and not on a role name — which is what lets an edited
    // role holding the same read reach them too.
    vi.stubGlobal(
      "fetch",
      adminNavBackend({ roles: ["ops"], allow: SEEDED_OPS_READS }),
    );
    renderNav();
    await waitFor(() => expect(navTabs()).toEqual(EVERY_TAB));
  });

  // An EDITED role, which is the case the seat gate must not swallow. This admin
  // is inside the group by seat and holds every grant it asks for except one —
  // and loses exactly the one entry that asks for it. A group gated on the seat
  // alone would hand them the licensing page their role no longer reads.
  it("loses only License for an admin whose role dropped that one read", async () => {
    const { license: _revoked, ...withoutLicense } = EVERY_TAB_GRANTED;
    vi.stubGlobal(
      "fetch",
      adminNavBackend({ roles: ["admin"], allow: withoutLicense }),
    );
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual(
        EVERY_TAB.filter((tab) => tab !== labelOf("license")),
      ),
    );
  });

  it("adds Maintenance for an admin holding no read at all, and loses Privacy with it", async () => {
    // The role half of Maintenance's predicate, on its own: an admin whose grants
    // were all revoked still administers the installation, and the danger zone
    // inside asks for that same role.
    //
    // Privacy goes, and that is the point of asking for a grant rather than
    // reading the seat twice: the consent registry's server gate is `person:read`, so
    // an admin stripped of it would reach a page of four refusals. The entry
    // follows the grant, not the role.
    vi.stubGlobal("fetch", adminNavBackend({ roles: ["admin"] }));
    renderNav();
    await waitFor(() =>
      expect(navTabs()).toEqual([...OPERATOR_TABS, "Maintenance"]),
    );
  });

  it("shows General to an admin holding the organization read once the company rollout flag is on", async () => {
    vi.stubGlobal(
      "fetch",
      adminNavBackend({
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
    const { fetchMock, answerCapabilities } =
      adminNavBackendHoldingCapabilities({
        roles: ["admin"],
        allow: readOn("organization"),
      });
    vi.stubGlobal("fetch", fetchMock);
    const { client } = renderNav();

    // Moment one: the nav is fully composed from /me — its role-gated entries
    // are on screen — while the flag is still unanswered, because this test
    // holds the answer.
    await screen.findByRole("link", { name: "Maintenance" });
    expect(navTabs()).toEqual(OPERATOR_TABS_WITH_MAINTENANCE);

    // Moment two: the answer is in the cache, which is the fact the emptiness
    // claim needs — the request having been SENT proves nothing about what the
    // nav has rendered.
    answerCapabilities();
    await waitFor(() =>
      expect(
        client.getQueryState(companyContextCapabilitiesQueryKey)?.status,
      ).toBe("success"),
    );
    expect(navTabs()).toEqual(OPERATOR_TABS_WITH_MAINTENANCE);
  });
});
