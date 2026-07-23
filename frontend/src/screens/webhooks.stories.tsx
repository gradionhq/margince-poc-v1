import type { Meta, StoryObj } from "@storybook/react-vite";
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
