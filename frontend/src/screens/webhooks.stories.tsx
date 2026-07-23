import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
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
  workspace_id: "w1",
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
  workspace_id: "w1",
  owner_id: "u1",
  target_url: "https://hooks.partner.test/inbound",
  event_types: ["organization.updated"],
  state: "paused",
  version: 5,
  created_at: "2026-05-11T09:00:00Z",
  updated_at: "2026-07-15T08:05:00Z",
  archived_at: null,
};

function meRoute(roles: string[]) {
  return () =>
    jsonResponse({
      user: { email: "person@acme.test" },
      roles,
      teams: [],
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

const meta: Meta<typeof WebhooksCard> = {
  title: "screens/webhooks",
  component: WebhooksCard,
};
export default meta;
type Story = StoryObj<typeof WebhooksCard>;

export const Active: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription], page }),
  }),
};

export const PausedSubscription: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription, pausedSubscription], page }),
  }),
};

export const NonAdminReadOnly: Story = {
  render: cardStory({
    "GET /me": meRoute(["rep"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription], page }),
  }),
};

export const NotConfigured: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
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
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () => jsonResponse({ data: [], page }),
  }),
};

// Task 8 (B-E10.14): the create form, opened from the empty list — the
// button lives outside QueryGate's empty branch specifically so the FIRST
// subscription is still creatable; this story is the render proof of that.
// The event-type checkboxes come straight off the generated
// subscribableEventTypeValues catalog (webhooks.tsx), never a hand-picked
// subset — the fe-uat screenshot shows the full published set.
export const CreateOpen: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () => jsonResponse({ data: [], page }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByTestId("new-webhook-subscription"),
    );
  },
};

// The one-time signing-secret reveal, right after a successful create: shown
// exactly once, copy-to-clipboard, "won't see this again" copy — gone the
// moment the modal closes (webhooks.tsx's SecretRevealModal holds it only in
// local state, never in the react-query cache the refreshed list reads from).
export const SecretRevealed: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () => jsonResponse({ data: [], page }),
    "POST /webhook-subscriptions": () =>
      jsonResponse(
        {
          subscription: {
            id: "sub-new",
            workspace_id: "w1",
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
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByTestId("new-webhook-subscription"),
    );
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
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription], page }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByTestId("edit-record"));
  },
};

export const RotateSecretConfirm: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription], page }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByTestId("rotate-webhook-secret"));
  },
};

// The rotated secret revealed through the SAME SecretRevealModal a create
// shows — proof rotate reuses it rather than growing a second reveal UI.
export const RotateSecretRevealed: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription], page }),
    "POST /webhook-subscriptions/sub-active/rotate-secret": () =>
      jsonResponse({
        subscription: { ...activeSubscription, version: 3 },
        signing_secret: "whsec_rotatedNEW9f3c2b7a1d==",
      }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByTestId("rotate-webhook-secret"));
    await userEvent.click(canvas.getByRole("button", { name: "Confirm" }));
  },
};

export const ArchiveConfirm: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription], page }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByTestId("archive-record"));
  },
};

// Task 10 (B-E10.14/B-E10.15): the deliveries + dead-letter panel, opened
// from a subscription row's "View deliveries" toggle — mixed statuses, the
// dead-lettered group, honest has_more (LoadMoreButton), and the replay
// confirm.

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

export const DeliveriesPanelOpen: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription], page }),
    "GET /webhook-subscriptions/sub-active/deliveries": () =>
      jsonResponse({
        data: [activeDelivery, retryingDelivery, deadLetteredDelivery],
        page: { next_cursor: null, has_more: true },
      }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByTestId("view-deliveries"));
    await canvas.findByTestId("dead-letter-group");
  },
};

export const DeliveriesReplayConfirm: Story = {
  render: cardStory({
    "GET /me": meRoute(["admin"]),
    "GET /webhook-subscriptions": () =>
      jsonResponse({ data: [activeSubscription], page }),
    "GET /webhook-subscriptions/sub-active/deliveries": () =>
      jsonResponse({
        data: [deadLetteredDelivery],
        page: { next_cursor: null, has_more: false },
      }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByTestId("view-deliveries"));
    await userEvent.click(await canvas.findByTestId("replay-delivery"));
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
