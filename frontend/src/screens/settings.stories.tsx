// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { pickOption } from "../design-system/select-testing";
import {
  AuditLogCard,
  PipelinesCard,
  SettingsScreen,
  settingsAddress,
} from "./settings";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The settings entries and the cards each one carries. Every story installs the
// fetch stub those cards read through, so the render is deterministic and
// network-free — the same fixture shapes the settings.test.tsx cases use.
//
// An organization entry is only reachable when the principal holds what its
// cards ask for, and SettingsScreen falls back to Account for anything else. So
// a story about such an entry has to name its grants: `me({...})` builds the
// /me body that opens the entry the story is capturing.

const me =
  (allow: GrantSpec = {}) =>
  () =>
    jsonResponse({
      ...meFixture({ roles: ["admin"], allow }),
      // The BARE user id, the way /me reports it. The audit fixture spells the
      // same actor the way the wire does ("human:u-mor"), so the trail reads
      // "You" for the viewer's own entry — ActorTag owns that difference.
      user: { ...meFixture().user, id: "u-mor", email: "ada@acme.test" },
    });

const passports = () =>
  jsonResponse({
    data: [
      {
        id: "pp-1",
        label: "Scout",
        scopes: ["read", "draft"],
        created_at: "2026-07-01T08:00:00Z",
        expires_at: "2026-10-01T08:00:00Z",
        revoked_at: null,
      },
    ],
    page: { next_cursor: null, has_more: false },
  });

// IT-1 governed tool console: two tools of differing tier/egress, plus a
// read-only passport so the play() below can show the send_email row dim
// (its "send" scope isn't in the selected passport's grant). Both live on the
// personal "Your agents" entry, which no grant gates.
const tools = () =>
  jsonResponse({
    data: [
      {
        name: "search_records",
        required_scope: "read",
        tier: "auto_execute",
        egress: false,
      },
      {
        name: "send_email",
        required_scope: "send",
        tier: "confirmation_required",
        egress: true,
      },
    ],
  });

// Attribution names the PERSON and says a machine did the typing second
// (PD-002), so the fixture carries the resolved names the read path returns and
// spells actor_id the way storekit stamps it — "human:<uuid>", not a bare id.
// A fixture that skipped the prefix is what let the "You" branch look covered
// while being unreachable in the product.
const auditLog = () =>
  jsonResponse({
    data: [
      {
        id: "a1",
        occurred_at: "2026-07-10T14:09:00Z",
        actor_type: "human",
        actor_id: "human:u-mor",
        actor_name: "Ada Mortensen",
        action: "create",
        entity_type: "custom_field",
        entity_id: "cf-1",
      },
      {
        id: "a2",
        occurred_at: "2026-07-10T09:41:00Z",
        actor_type: "human",
        actor_id: "human:u-lars",
        actor_name: "Lars Vogt",
        action: "update",
        entity_type: "deal",
        entity_id: "d-1",
      },
      {
        // An agent under a human's authority reads as that human, qualified.
        id: "a3",
        occurred_at: "2026-07-10T08:12:00Z",
        actor_type: "agent",
        actor_id: "agent:01a01740-c9c2-736d-a0b6-d3e3dcb13111",
        passport_id: "01a01740-c9c2-736d-a0b6-d3e3dcb13999",
        on_behalf_of: "u-lars",
        on_behalf_of_name: "Lars Vogt",
        action: "update",
        entity_type: "deal",
        entity_id: "d-1",
      },
      {
        // A grant was presented and no human resolved behind it: a gap, and it
        // says so rather than reading as "System".
        id: "a4",
        occurred_at: "2026-07-10T07:30:00Z",
        actor_type: "agent",
        actor_id: "agent:scheduled_send",
        passport_id: "01a01740-c9c2-736d-a0b6-d3e3dcb13aaa",
        action: "send_email",
        entity_type: "activity",
        entity_id: "ac-1",
      },
      {
        // No grant presented — a background pass nobody's context ran. Not a
        // gap, so it shows what acted and claims no missing authority.
        id: "a5",
        occurred_at: "2026-07-10T06:00:00Z",
        actor_type: "agent",
        actor_id: "agent:org_name_promotion",
        action: "update",
        entity_type: "organization",
        entity_id: "o-1",
      },
      {
        id: "a6",
        occurred_at: "2026-07-10T05:00:00Z",
        actor_type: "system",
        actor_id: "system",
        action: "erase",
        entity_type: "person",
        entity_id: "p-9",
      },
    ],
    page: { next_cursor: null, has_more: false },
  });

function tab(tabId: string, routes: RouteMap) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <SettingsScreen route={settingsAddress(tabId)} />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof SettingsScreen> = {
  title: "Settings/Settings screen",
  component: SettingsScreen,
};
export default meta;

type Story = StoryObj<typeof SettingsScreen>;

export const AccountTab: Story = {
  render: tab("account", { "GET /me": me() }),
};

// Theme and language sit on this tab because they belong to the person, not to
// the sidebar. The play() opens the language listbox so the capture carries the
// options rather than only the control's closed face.
export const AccountPreferences: Story = {
  render: tab("account", { "GET /me": me() }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("combobox", { name: "Language" }),
    );
  },
};

// The person's own agent authority: the autonomy table, the passports they have
// minted, the clients holding one, and the tools those credentials reach.
export const AgentsTab: Story = {
  render: tab("agents", { "GET /me": me(), "GET /passports": passports }),
};

// AS-2 kill-switch: PassportCard revoke is a hard DELETE behind a ConfirmModal.
// Mirrors share.stories' revoke play() — render the card with a live
// (non-revoked) passport, click Revoke, leave the confirm modal open so the
// guarded state is what the render gate captures.
export const PassportRevokeConfirm: Story = {
  render: tab("agents", { "GET /me": me(), "GET /passports": passports }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const revokeButton = await canvas.findByRole("button", { name: "Revoke" });
    await userEvent.click(revokeButton);
  },
};

// The mint DRAWER, which is where the create form lives now. It used to be a
// row inside the card — a label, a name field, five scope ticks and the submit
// on one flex line with one gap value between all eight — so nothing said where
// the field ended and the choices began.
export const PassportMintDrawer: Story = {
  name: "Mint a passport",
  render: tab("agents", { "GET /me": me(), "GET /passports": passports }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Mint passport" }),
    );
  },
};

// The same drawer in dark, because the fieldset's legend, the checkbox rows and
// the recessed token plate are three surfaces whose separation is carried by
// tokens that move between themes.
export const PassportMintDrawerDark: Story = {
  name: "Mint a passport — dark",
  globals: { theme: "dark" },
  render: tab("agents", { "GET /me": me(), "GET /passports": passports }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Mint passport" }),
    );
  },
};

// The governed tool console renders the inventory unfiltered by default,
// then dims the send_email row once the read-only "Scout" passport (whose
// only granted scope is "read") is selected — its required "send" scope
// is absent from that grant.
const toolConsoleRoutes = {
  "GET /me": me(),
  "GET /passports": passports,
  "GET /agent-tools": tools,
};

// Selects the read-only passport, so the send_email row is dimmed. Shared with
// the dark variant below, which is about that dimming and nothing else.
const selectScoutPassport = async ({
  canvasElement,
}: {
  canvasElement: HTMLElement;
}) => {
  const canvas = within(canvasElement);
  await canvas.findByText("search_records");
  // The listbox is portalled to the body, outside this story's canvas, so the
  // pick goes through the shared helper rather than a canvas-scoped query.
  await pickOption(
    userEvent.setup(),
    canvas.getByRole("combobox", { name: "All passports" }),
    "Reachable by Scout",
  );
};

export const AgentToolConsole: Story = {
  render: tab("agents", toolConsoleRoutes),
  play: selectScoutPassport,
};

// The unreachable row is dimmed, and dimming is the one signal that does not
// survive a theme swap by construction: on a light ground it reads as "faded
// toward the paper", on a dark one the same reduction moves the text toward the
// background it is meant to stay legible against. This is the story that says
// whether the dim row is still readable text or has become a grey smear — and
// whether the tier/egress badges beside it still separate from each other.
export const AgentToolConsoleDark: Story = {
  globals: { theme: "dark" },
  render: tab("agents", toolConsoleRoutes),
  play: selectScoutPassport,
};

// The shape a record takes, on one page: the field editor, the pipeline
// designer, the product list and the offer templates. The four surfaces used to
// be three separate screens behind door-cards, and the doors are gone.
//
// The custom_field READ is what opens the entry — opening a page is reading it,
// and `meFixture` grants only the verbs named here. A write-only fixture reaches
// no entry at all and the story silently captures the Account fallback instead,
// which is exactly what it did: nothing asserts on a story, so the gates stayed
// green while the picture was of the wrong page. The writes stay so the builder
// and the row actions render.
export const DataModelTab: Story = {
  render: tab("data-model", {
    "GET /me": me({ custom_field: ["read", "create", "update"] }),
  }),
};

// The consent registry and the audit trail on one page: the trail is what proves
// the surfaces above it were honoured, so it moved here from a tab of its own.
export const PrivacyTab: Story = {
  // `person:read` is what opens this entry — the consent registry is gated on it
  // server-side (consent/store.go), not on a role. Without it the entry is not
  // visible, useVisibleSettingsTabs falls back to Account, and this story
  // captured the Account tab: byte-identical to AccountTab, under the name of a
  // page it never rendered. The comment two stories up describes this exact
  // failure; it happened again here.
  render: tab("privacy", {
    "GET /me": me({ person: ["read"] }),
    "GET /audit-log": auditLog,
  }),
};

const privacyRoutes = {
  "GET /me": me({ person: ["read"] }),
  "GET /audit-log": auditLog,
};

// A whole settings PAGE at 390px, which is the thing only this file can show —
// every other story in the settings tree renders one card with nothing above or
// below it. `layout: "fullscreen"` because SettingsScreen brings `.wrap`, which
// carries production's own gutter; the canvas frame would add a second one and
// make this a 326px phone instead of a 390px one.
//
// What it watches is the seam BETWEEN cards rather than any one card's insides:
// three panels stack here, each with a title and an action button on the same
// header line, and one of them is a withheld body whose whole content is a
// sentence explaining a denial. A page is where the panel header's title/action
// split and the stack's own rhythm have to hold at once — and where the DSR facet
// control runs out of width first.
export const PrivacyTabPhone: Story = {
  parameters: { layout: "fullscreen" },
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: tab("privacy", privacyRoutes),
};

// PipelinesCard (D-8, on the Data model entry) reads GET /me (roles →
// pipeline grant) and GET /pipelines. Rendered directly here so
// the admin write affordances vs the rep read-only state each get a story.
const pipelinesFixture = {
  data: [
    {
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
          id: "s2",
          pipeline_id: "pl",
          name: "Proposal",
          position: 2,
          semantic: "open",
          win_probability: 50,
        },
        {
          id: "s3",
          pipeline_id: "pl",
          name: "Won",
          position: 3,
          semantic: "won",
          win_probability: 100,
        },
      ],
    },
  ],
  page: { next_cursor: null, has_more: false },
};

const pipelineMe = (allow: GrantSpec) =>
  jsonResponse({
    ...meFixture({ allow }),
    user: { ...meFixture().user, id: "u-1", display_name: "Me" },
  });

// useMe() fails fast without a workspace slug, collapsing the admin state into
// read-only — seed the slug so /me resolves and the affordances render.
function pipelinesCard(allow: GrantSpec) {
  return () => {
    globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
    installFetchStub({
      "GET /me": () => pipelineMe(allow),
      "GET /pipelines": () => jsonResponse(pipelinesFixture),
    });
    return (
      <StoryProviders>
        <PipelinesCard />
      </StoryProviders>
    );
  };
}

export const PipelinesAdmin: Story = {
  render: pipelinesCard({ pipeline: ["read", "create", "update"] }),
};

export const PipelinesReadOnly: Story = {
  render: pipelinesCard({ pipeline: ["read"] }),
};

// The narrow render of the one rule in settings.css that has its own breakpoint.
// A `.stage-row` is four tracks of which three are fixed — an 88px semantic
// badge, a 56px win probability, and the Edit verb — so on a phone the fixed
// tracks ARE the width and the stage name has nothing left; under 560px the row
// becomes two lines instead. Nothing has ever drawn it below 1024px. The ladder
// is what makes this the right story on this page: the four other data-model
// cards answer their list routes from the stub's empty-page fallback, so a
// page-level narrow story here would picture three empty states and a heading.
export const PipelinesAdminPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: pipelinesCard({ pipeline: ["read", "create", "update"] }),
};

// And the dark render of the same ladder, which is the densest real content the
// data model page has: three stage names, the Open/Won semantic badges beside the
// pipeline's own Default badge, three `.t-mono` win probabilities, the row verbs,
// and no hairline between rows at all. What it watches is whether Open and Won
// stay distinguishable from each other and from the row behind them once the
// ground goes dark — a badge is tinted text on a tinted surface, and both move.
export const PipelinesAdminDark: Story = {
  globals: { theme: "dark" },
  render: pipelinesCard({ pipeline: ["read", "create", "update"] }),
};

// AuditLogCard (AO-3/AO-4): one entry carrying a full before/after diff plus
// the agent attribution trail (passport, on-behalf-of human, authorization
// rule, grounding evidence), collapsed by default — the expand toggle is
// what a reviewer exercises to confirm the panel renders honestly.
const auditLogPage = {
  data: [
    {
      id: "al-1",
      actor_type: "agent",
      actor_id: "agent:sdr",
      passport_id: "pp-9",
      on_behalf_of: "u-1",
      on_behalf_of_name: "Me",
      action: "update",
      entity_type: "person",
      entity_id: "p-1",
      before: { stage: "new" },
      after: { stage: "qualified" },
      authorization_rule: "role:admin",
      evidence: { snippet: "Reply confirmed budget", source: "email:msg-1" },
      occurred_at: "2026-07-10T09:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

const auditLogMe = (roles: string[]) =>
  jsonResponse({ user: { id: "u-1", display_name: "Me" }, roles, teams: [] });

export const AuditLog: Story = {
  render: () => {
    globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
    installFetchStub({
      "GET /me": () => auditLogMe(["admin"]),
      "GET /audit-log": () => jsonResponse(auditLogPage),
      "GET /people/p-1": () =>
        jsonResponse({ id: "p-1", full_name: "Priya Shah" }),
    });
    return (
      <StoryProviders>
        <AuditLogCard />
      </StoryProviders>
    );
  },
};
