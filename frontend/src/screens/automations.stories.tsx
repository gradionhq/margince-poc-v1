// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { AutomationRow, AutomationsAdmin } from "./automations";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// AutomationRow with its two lazy panel toggles (Runs / Preview). The panels
// only fetch once opened; a benign stub answers the run list and preview POST
// so an interactive reviewer can open either without a live stack.

type Automation = components["schemas"]["Automation"];
type CatalogEntry = components["schemas"]["AutomationCatalogEntry"];

const meta: Meta = {
  title: "Settings/Admin settings/AI/Automations",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const entry: CatalogEntry = {
  key: "stalled_deal_nudge",
  name: "Stalled-deal nudge",
  description: "Stages a follow-up when a deal stalls.",
  trigger: "deal.stalled",
  action: "send_email",
  tier: "confirmation_required",
  params_schema: {
    type: "object",
    properties: {
      due_in_days: { type: "integer", minimum: 1, maximum: 30, default: 3 },
    },
    required: ["due_in_days"],
  },
};

// A second entry, and it earns its place: the library's whole shape is the
// interval and the hairline BETWEEN two entries, which a one-entry fixture
// cannot picture. It also carries the other autonomy tier and no description at
// all, so the row has to read with the recipe as its only second line.
const autoEntry: CatalogEntry = {
  key: "task_on_stage_entry",
  name: "Task on stage entry",
  trigger: "deal.stage_changed",
  action: "create_task",
  tier: "auto_execute",
  params_schema: {
    type: "object",
    properties: {
      due_in_days: { type: "integer", minimum: 1, maximum: 30, default: 7 },
    },
    required: ["due_in_days"],
  },
};

const automation: Automation = {
  id: "au-1",
  key: "stalled_deal_nudge",
  name: "Nudge stalled fleet deals",
  status: "enabled",
  params: { due_in_days: 3 },
  version: 3,
  created_at: "2026-07-01T08:00:00Z",
};

function stubPanels() {
  installFetchStub({
    "GET /automations/au-1/runs": () =>
      jsonResponse({ data: [], page: { next_cursor: null } }),
    "POST /automations/au-1/preview": (body) =>
      jsonResponse({
        matches_now: 8,
        would_have_fired: 21,
        window_days: (body as { window_days: number }).window_days,
      }),
  });
}

// The verbs live behind the row's overflow menu, so a story that leaves it
// closed captures the same picture whatever the grants say — which is what the
// pair below was doing: two names, one screenshot, and the difference they exist
// to show never drawn. Opening it is the story.
const openRowActions: NonNullable<Story["play"]> = async ({
  canvasElement,
}) => {
  await userEvent
    .setup()
    .click(within(canvasElement).getByRole("button", { name: /Actions for/ }));
};

const renderConfigurable = () => {
  stubPanels();
  return (
    <StoryProviders>
      <ul style={{ listStyle: "none" }}>
        <AutomationRow
          automation={automation}
          entry={entry}
          canViewRuns
          canEdit
          canDelete
        />
      </ul>
    </StoryProviders>
  );
};

export const Configurable: Story = {
  play: openRowActions,
  render: renderConfigurable,
};

// The row in dark with its menu open. Two things carry meaning by colour and
// nothing else: the Switch track, which is the only statement that this
// automation is live, and the tier badge that says whether it acts alone. The
// open menu is the other half — a popover paints its OWN background over the
// page, so it is the element that reads as a leftover light panel if a surface
// token does not re-resolve.
export const ConfigurableDark: Story = {
  globals: { theme: "dark" },
  play: openRowActions,
  render: renderConfigurable,
};

// The row at 390px with the menu open, which is where the popover's anchoring
// meets the edge of the screen. The row wraps its controls onto a second line, so
// the trigger ends up near the LEFT margin; the menu is anchored to the trigger's
// right edge and opens leftwards from there. Whether the verbs are still on
// screen is the whole question, and a bounding box cannot answer it — hit-test
// the middle of each item.
export const ConfigurablePhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  play: openRowActions,
  render: renderConfigurable,
};

// Deliberately a combination no seeded role holds: update without delete. The
// row must offer pause and edit while withholding the destructive action — the
// case a single "canManage" flag could not express, and the one a divergent
// fixture exists to catch.
export const EditableButNotDeletable: Story = {
  play: openRowActions,
  render: () => {
    stubPanels();
    return (
      <StoryProviders>
        <ul style={{ listStyle: "none" }}>
          <AutomationRow
            automation={automation}
            entry={entry}
            canViewRuns
            canEdit
            canDelete={false}
          />
        </ul>
      </StoryProviders>
    );
  },
};

export const ReadOnly: Story = {
  render: () => {
    stubPanels();
    return (
      <StoryProviders>
        <ul style={{ listStyle: "none" }}>
          <AutomationRow
            automation={automation}
            entry={entry}
            canViewRuns
            canEdit={false}
            canDelete={false}
          />
        </ul>
      </StoryProviders>
    );
  },
};

// The row's Edit verb opens the definition in a DIALOG — a name plus every
// parameter the schema declares is a form submitted together, so it cannot be
// a panel unfolding under the row without the list stopping being a list.
//
// Both the menu panel and the dialog are portalled to the document body, so
// the trigger is found in the canvas and everything the trigger opens is found
// in the body. A play scoped to the canvas alone would look for the item in the
// one place it is not.
const openEditor: NonNullable<Story["play"]> = async ({ canvasElement }) => {
  const user = userEvent.setup();
  await user.click(
    within(canvasElement).getByRole("button", { name: /Actions for/ }),
  );
  const body = within(canvasElement.ownerDocument.body);
  await user.click(await body.findByRole("button", { name: "Edit" }));
};

export const EditingInADialog: Story = {
  play: openEditor,
  render: renderConfigurable,
};

// The dialog in dark. It paints its own surface over the card, so it is the
// element that reads as a leftover light panel if a surface token does not
// re-resolve — and the params form inside it carries every field kind the
// catalog can ask for.
export const EditingInADialogDark: Story = {
  globals: { theme: "dark" },
  play: openEditor,
  render: renderConfigurable,
};

// The whole card, which is what the row language changed: two decisions, each
// of which IS a list rather than an answer that would fit beside its naming, so
// both take the full width below it. What is running comes first; the closed
// library that feeds it comes second, and every library entry is now a ROW of
// the same language — name and recipe in the naming column, `Use template` at
// the x every answer on this page sits at, a hairline between entries.
const CARD_ROUTES = {
  "GET /me": meRoute({ automation: ["create", "read", "update", "delete"] }),
  "GET /automations/catalog": () => jsonResponse({ data: [entry, autoEntry] }),
  "GET /automations": () =>
    jsonResponse({ data: [automation], page: { next_cursor: null } }),
};

export const AdminCard: Story = {
  render: () => {
    installFetchStub(CARD_ROUTES);
    return (
      <StoryProviders>
        <AutomationsAdmin />
      </StoryProviders>
    );
  },
};

// The card in dark. The library's hairlines are `--borderSubtle` between rows
// and the recipe line is mono `--textMeta` under a description — the two pairs
// most likely to disappear into the card when the ground goes dark, and both are
// what the entries' new rhythm is made of.
export const AdminCardDark: Story = {
  globals: { theme: "dark" },
  render: () => {
    installFetchStub(CARD_ROUTES);
    return (
      <StoryProviders>
        <AutomationsAdmin />
      </StoryProviders>
    );
  },
};

// The same card for a seat that may read the surface and change nothing: the
// library stays readable — no object grant gates the catalog — while the verb
// that authors from it, the row's switch and the row's menu items are the parts
// that answer to a grant. The card says once, above the rows, why.
export const AdminCardReadOnly: Story = {
  render: () => {
    installFetchStub({
      ...CARD_ROUTES,
      "GET /me": meRoute({ automation: ["read"] }),
    });
    return (
      <StoryProviders>
        <AutomationsAdmin />
      </StoryProviders>
    );
  },
};
