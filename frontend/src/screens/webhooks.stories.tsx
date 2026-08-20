import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent, within } from "storybook/test";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { Badge } from "../design-system/atoms";
import { LocaleProvider } from "../i18n";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";
import { WebhooksCard, webhookStatusBadge } from "./webhooks";

// WebhooksCard stories for the fe-uat render gate: an active subscription, a
// paused one (the honest "dead" counterpart the list schema actually
// carries — no fabricated per-item delivery health), the 503
// webhooks_not_configured not-enabled state, and the empty state — all off
// the same fetch-stub shapes the unit tests exercise.

const page = { next_cursor: null, has_more: false };

const activeSubscription = {
  id: "sub-active",
  owner_id: "u1",
  target_url: "https://hooks.acme.test/margince",
  event_types: ["deal.stage_changed", "lead.promoted", "offer.accepted"],
  state: "active",
  version: 2,
  created_at: "2026-06-01T09:00:00Z",
  updated_at: "2026-07-20T14:32:00Z",
  archived_at: null,
};

const pausedSubscription = {
  id: "sub-paused",
  owner_id: "u1",
  target_url: "https://hooks.partner.test/inbound",
  event_types: ["organization.updated"],
  state: "paused",
  version: 5,
  created_at: "2026-05-11T09:00:00Z",
  updated_at: "2026-07-15T08:05:00Z",
  archived_at: null,
};

const WEBHOOK_OPERATOR: GrantSpec = {
  webhook_subscription: ["read", "create", "update", "delete"],
};

function meRoute(allow: GrantSpec) {
  return () =>
    jsonResponse({
      ...meFixture({ allow }),
      user: { ...meFixture().user, email: "person@acme.test" },
    });
}

function cardStory(routes: Record<string, () => Response>) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <WebhooksCard />
      </StoryProviders>
    );
  };
}

// The route pair almost every story below needs — an admin `/me` and the
// subscription list itself — merged with a story's own extra routes (a
// create/rotate/deliveries response) via object spread. Kept as a function
// rather than a plain constant because most stories vary the subscription
// list or the caller's roles (NonAdminReadOnly).
function baseRoutes(
  subscriptions: unknown[] = [activeSubscription],
  allow: GrantSpec = WEBHOOK_OPERATOR,
): Record<string, () => Response> {
  return {
    "GET /me": meRoute(allow),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: subscriptions, page, delivery_enabled: true }),
  };
}

// Drives a sequence of testid clicks against one render, returning a query
// handle so a play function can chain a further assertion or a non-testid
// interaction (a role-named confirm button, a label lookup).
//
// Document-scoped, because this card's verbs do not all live in the canvas:
// OverflowMenu portals its panel to document.body, so Rotate secret and Archive
// are outside it, and so is the ConfirmModal an item opens. A canvas-scoped
// lookup rejects on a menu item however correctly the menu is drawn — and the
// frame captured beside that rejection shows a closed row under the name of an
// armed dialog.
async function clickTestIds(testIds: string[]) {
  for (const testId of testIds) {
    await userEvent.click(await screen.findByTestId(testId));
  }
  return screen;
}

// Rotate secret and Archive are the two irreversible verbs, so they live behind
// the row's overflow rather than on it at Edit's weight — and OverflowMenu does
// not MOUNT its children until it is first opened (they carry their own reads).
// So a story about either opens the menu the way a reader does: a click aimed
// straight at the item's testid finds no such node, and the capture that
// follows shows a closed row under the name of an armed dialog.
// webhooks.test.tsx opens it the same way, for the same reason.
async function openRowActions(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(
    await canvas.findByRole("button", { name: "More actions" }),
  );
}

const meta: Meta<typeof WebhooksCard> = {
  title: "Settings/Admin settings/Integrations/Webhooks",
  component: WebhooksCard,
};
export default meta;
type Story = StoryObj<typeof WebhooksCard>;

export const Active: Story = {
  render: cardStory(baseRoutes()),
};

// A subscription row is the densest line in the settings tree: a `.t-mono`
// target URL nobody promised would be short as its label, a state badge and
// three event-type chips as its value, and three verbs as its control. Below
// 640px `SettingRow` gives up the two-column alignment and stacks, so what to
// check here is that the row reads as one block in reading order —
// label, description, answer, verbs — rather than pushing the card's scroll
// width past the viewport.
//
// No `layout` override: the canvas frame's 2rem gutter puts the card at ~326px,
// which is the column the overflow measurements on this branch were taken in.
export const ActivePhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: cardStory(baseRoutes()),
};

// The event set is the row's ANSWER, in the right column beside the verbs that
// change it — which is the one place putting it there can go wrong. Twenty event
// types is a legitimate subscription, and uncapped that badge run squeezes the
// target URL out of the label column and takes the one-x alignment the whole row
// language exists to hold with it. `.webhook-answer` caps the measure and wraps
// instead; what to check is that the URL still reads and the verbs still sit at
// the card's right edge.
export const ManyEventTypes: Story = {
  render: cardStory(
    baseRoutes([
      {
        ...activeSubscription,
        event_types: [
          "deal.stage_changed",
          "deal.created",
          "deal.won",
          "deal.lost",
          "lead.promoted",
          "lead.created",
          "offer.accepted",
          "offer.rejected",
          "person.merged",
          "organization.updated",
        ],
      },
    ]),
  ),
};

export const PausedSubscription: Story = {
  render: cardStory(baseRoutes([activeSubscription, pausedSubscription])),
};

export const NonAdminReadOnly: Story = {
  render: cardStory(baseRoutes([activeSubscription], {})),
};

export const NotConfigured: Story = {
  render: cardStory({
    "GET /me": meRoute(WEBHOOK_OPERATOR),
    "GET /webhook-subscriptions": () =>
      jsonResponse(
        {
          title: "Service Unavailable",
          code: "webhooks_not_configured",
          detail:
            "outbound webhooks require a deployment signing key that is not configured",
        },
        503,
      ),
  }),
};

export const Empty: Story = {
  render: cardStory(baseRoutes([])),
};

// Task 8 (B-E10.14): the create form, opened from the empty list — the
// button lives outside QueryGate's empty branch specifically so the FIRST
// subscription is still creatable; this story is the render proof of that.
// The event-type checkboxes come straight off the generated
// subscribableEventTypeValues catalog (webhooks.tsx), never a hand-picked
// subset — the fe-uat screenshot shows the full published set.
export const CreateOpen: Story = {
  render: cardStory(baseRoutes([])),
  play: async () => {
    await clickTestIds(["new-webhook-subscription"]);
  },
};

// The one-time signing-secret reveal, right after a successful create: shown
// exactly once, copy-to-clipboard, "won't see this again" copy — gone the
// moment the modal closes (webhooks.tsx's SecretRevealModal holds it only in
// local state, never in the react-query cache the refreshed list reads from).
export const SecretRevealed: Story = {
  render: cardStory({
    ...baseRoutes([]),
    "POST /webhook-subscriptions": () =>
      jsonResponse(
        {
          subscription: {
            id: "sub-new",
            owner_id: "u1",
            target_url: "https://hooks.acme.test/inbound",
            event_types: ["deal.stage_changed"],
            state: "active",
            version: 1,
            created_at: "2026-07-22T00:00:00Z",
            updated_at: "2026-07-22T00:00:00Z",
            archived_at: null,
          },
          signing_secret: "whsec_9f3c2b7a1d4e5f60ac71b8d92e==",
        },
        201,
      ),
  }),
  play: async () => {
    const canvas = await clickTestIds(["new-webhook-subscription"]);
    await userEvent.type(
      await canvas.findByLabelText(/target url/i),
      "https://hooks.acme.test/inbound",
    );
    await userEvent.click(canvas.getByLabelText("deal.stage_changed"));
    await userEvent.click(canvas.getByRole("button", { name: "Create" }));
  },
};

// Task 9 (B-E10.14): the edit form (pause/resume + re-target the event set),
// the rotate-secret confirm, and the archive confirm — all gated on the same
// admin/ops role the create affordance already gates on.
export const EditOpen: Story = {
  render: cardStory(baseRoutes()),
  play: async () => {
    await clickTestIds(["edit-record"]);
  },
};

export const RotateSecretConfirm: Story = {
  render: cardStory(baseRoutes()),
  play: async ({ canvasElement }) => {
    await openRowActions(canvasElement);
    await clickTestIds(["rotate-webhook-secret"]);
  },
};

// The rotated secret revealed through the SAME SecretRevealModal a create
// shows — proof rotate reuses it rather than growing a second reveal UI.
export const RotateSecretRevealed: Story = {
  render: cardStory({
    ...baseRoutes(),
    "POST /webhook-subscriptions/sub-active/rotate-secret": () =>
      jsonResponse({
        subscription: { ...activeSubscription, version: 3 },
        signing_secret: "whsec_rotatedNEW9f3c2b7a1d==",
      }),
  }),
  play: async ({ canvasElement }) => {
    await openRowActions(canvasElement);
    const canvas = await clickTestIds(["rotate-webhook-secret"]);
    // Scoped to the dialog, not to `screen`: the confirm button is labelled
    // with the ACT ("Rotate secret") rather than a generic "Confirm", which is
    // also the label on the menu item that opened it — and the menu stays open
    // behind the dialog on purpose, so an unscoped lookup for that name has two
    // matches and the one it wants is the second.
    const dialog = within(await canvas.findByRole("dialog"));
    await userEvent.click(
      await dialog.findByRole("button", { name: "Rotate secret" }),
    );
    // The reveal is the story: without this the play could only prove the
    // confirm was clickable, and a screenshot of the confirm dialog still
    // sitting there would pass under the name of a revealed secret.
    await canvas.findByTestId("webhook-signing-secret");
  },
};

export const ArchiveConfirm: Story = {
  render: cardStory(baseRoutes()),
  play: async ({ canvasElement }) => {
    await openRowActions(canvasElement);
    await clickTestIds(["archive-record"]);
  },
};

// Task 10 (B-E10.14/B-E10.15): the deliveries + dead-letter panel, opened
// from a subscription row's "View deliveries" toggle — mixed statuses, the
// dead-lettered group, honest has_more (LoadMoreButton), and the replay
// confirm.
//
// The log is a SIBLING row of the subscription rather than content nested inside
// it: it is the subject, not an answer, so it takes the card's full width below
// the row that opened it. What that buys is the table's own measure; what it
// costs is that attribution now rests on adjacency alone, which is what
// `DeliveriesOpenBetweenTwoSubscriptions` below exists to look at.

const activeDelivery = {
  id: "del-active",
  subscription_id: "sub-active",
  event_id: "evt-1",
  event_type: "offer.accepted",
  status: "delivered",
  attempts: 1,
  last_status_code: 200,
  last_error: null,
  next_retry_at: null,
  delivered_at: "2026-07-21T12:00:00Z",
  dead_lettered_at: null,
  created_at: "2026-07-21T11:59:00Z",
  updated_at: "2026-07-21T12:00:00Z",
};

const retryingDelivery = {
  id: "del-retrying",
  subscription_id: "sub-active",
  event_id: "evt-2",
  event_type: "lead.promoted",
  status: "retrying",
  attempts: 3,
  last_status_code: 503,
  last_error: "upstream returned 503",
  next_retry_at: "2026-07-22T09:00:00Z",
  delivered_at: null,
  dead_lettered_at: null,
  created_at: "2026-07-21T08:00:00Z",
  updated_at: "2026-07-21T08:04:00Z",
};

const deadLetteredDelivery = {
  id: "del-dead",
  subscription_id: "sub-active",
  event_id: "evt-3",
  event_type: "organization.updated",
  status: "dead_lettered",
  attempts: 6,
  last_status_code: 500,
  last_error: "connection refused",
  next_retry_at: null,
  delivered_at: null,
  dead_lettered_at: "2026-07-20T10:00:00Z",
  created_at: "2026-07-20T09:00:00Z",
  updated_at: "2026-07-20T10:00:00Z",
};

const deliveriesRoutes = {
  ...baseRoutes(),
  "GET /webhook-subscriptions/sub-active/deliveries": () =>
    jsonResponse({
      data: [activeDelivery, retryingDelivery, deadLetteredDelivery],
      page: { next_cursor: null, has_more: true },
    }),
};

const openDeliveries = async () => {
  const canvas = await clickTestIds(["view-deliveries"]);
  await canvas.findByTestId("dead-letter-group");
};

export const DeliveriesPanelOpen: Story = {
  render: cardStory(deliveriesRoutes),
  play: openDeliveries,
};

// The attribution case. With the log in its own row, a card holding two
// subscriptions puts it BETWEEN them — hairline above, hairline below — and
// nothing but proximity says which of the two it reports on. Its "Delivery
// attempts" label and the open toggle on the row above are the whole of that
// signal, so this is the render to judge them on: a reader landing here must not
// be able to read the log as the paused subscription's.
export const DeliveriesOpenBetweenTwoSubscriptions: Story = {
  render: cardStory({
    ...baseRoutes([activeSubscription, pausedSubscription]),
    "GET /webhook-subscriptions/sub-active/deliveries": () =>
      jsonResponse({
        data: [activeDelivery, deadLetteredDelivery],
        page: { next_cursor: null, has_more: false },
      }),
  }),
  // Not `openDeliveries`: two subscriptions mean two toggles, and a lookup by
  // testid alone rejects on the pair rather than picking one. The first row is
  // the one whose log is stubbed.
  play: async () => {
    const toggles = await screen.findAllByTestId("view-deliveries");
    await userEvent.click(toggles[0]);
    await screen.findByTestId("dead-letter-group");
  },
};

// The eight-column delivery table inside its `.table-scroll` box at 390px. The
// question is which of the two gives: either the scroller holds the table and
// scrolls it sideways inside the card, or the table wins and the whole settings
// column scrolls — and only a capture at this width can tell them apart, because
// a table that overflows its scroller measures the same either way.
export const DeliveriesPanelOpenPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: cardStory(deliveriesRoutes),
  play: openDeliveries,
};

// The delivery statuses in situ rather than as a swatch row: `delivered`,
// `retrying` and `dead_lettered` are three badge tones whose whole job is to be
// told apart at a glance, and they sit here on the table's own striping, inside
// the dead-letter group's tinted block, beside the `.t-mono` event ids. On a dark
// ground a badge surface token and a table row token can converge, and that is
// what this watches for — the pure `DeliveryStatusBadges` story below cannot,
// because it shows the tones with nothing to be confused with.
export const DeliveriesPanelOpenDark: Story = {
  globals: { theme: "dark" },
  render: cardStory(deliveriesRoutes),
  play: openDeliveries,
};

export const DeliveriesReplayConfirm: Story = {
  render: cardStory({
    ...baseRoutes(),
    "GET /webhook-subscriptions/sub-active/deliveries": () =>
      jsonResponse({
        data: [deadLetteredDelivery],
        page: { next_cursor: null, has_more: false },
      }),
  }),
  play: async () => {
    await clickTestIds(["view-deliveries", "replay-delivery"]);
  },
};

// The pure delivery-status → badge mapping the deliveries panel reuses — no
// fetch, no providers beyond the locale (mirrors quotas.stories.tsx's Ring).
export const DeliveryStatusBadges: StoryObj = {
  render: () => (
    <LocaleProvider initial="en">
      <div style={{ display: "flex", gap: "var(--space-4)" }}>
        <Badge tone={webhookStatusBadge("delivered")}>delivered</Badge>
        <Badge tone={webhookStatusBadge("pending")}>pending</Badge>
        <Badge tone={webhookStatusBadge("retrying")}>retrying</Badge>
        <Badge tone={webhookStatusBadge("dead_lettered")}>dead_lettered</Badge>
      </div>
    </LocaleProvider>
  ),
};
