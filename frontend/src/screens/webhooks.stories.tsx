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
