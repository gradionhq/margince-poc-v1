import { useQuery } from "@tanstack/react-query";
import { Webhook } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import {
  canConfigureAutomations,
  problemMessage,
  QueryGate,
  useMe,
} from "./common";

// Settings → Integrations (B-E10.14): the subscription list for outbound
// webhooks. The list wire (WebhookSubscription) carries no per-item delivery
// health — that lives on the separate deliveries sub-resource, out of this
// card's scope — so the health line here renders only what the list itself
// is honest about: state, the subscribed event set, and last-updated. A
// deployment with no signing key answers 503 webhooks_not_configured; that
// is a deliberate, documented feature-off state, never an error.

type WebhookSubscription = components["schemas"]["WebhookSubscription"];
type WebhookDeliveryStatus = components["schemas"]["WebhookDelivery"]["status"];

// The shared delivery-status → Badge tone mapping (events.md §5's four
// delivery states): kept here, next to the subscription list it health-
// summarizes, so the deliveries panel reuses the ONE spelling rather than
// re-deriving its own tone rules per status.
export function webhookStatusBadge(
  status: WebhookDeliveryStatus,
): "success" | "warn" | "danger" | "accent" {
  switch (status) {
    case "delivered":
      return "success";
    case "dead_lettered":
      return "danger";
    case "retrying":
      return "warn";
    case "pending":
      return "accent";
  }
}

type SubscriptionsResult =
  | { configured: true; data: WebhookSubscription[] }
  | { configured: false };

function useWebhookSubscriptions() {
  return useQuery({
    queryKey: ["webhook-subscriptions"],
    queryFn: async (): Promise<SubscriptionsResult> => {
      const { data, error, response } = await api.GET(
        "/webhook-subscriptions",
        { params: { query: {} } },
      );
      // A bodiless 503 (openapi-fetch reports a falsy `error` for it same as
      // any other non-2xx without a matching typed response) is this
      // deployment's honest "not configured" answer — read the status, not
      // the error channel, so it never collapses into the generic error card.
      if (response.status === 503) {
        return { configured: false };
      }
      if (error) {
        throw new Error(problemMessage(error));
      }
      return { configured: true, data: data.data };
    },
  });
}

function subscriptionStateTone(
  state: WebhookSubscription["state"],
): "success" | "warn" {
  return state === "active" ? "success" : "warn";
}

function NotConfiguredState() {
  const t = useT();
  return <EmptyState>{t("webhooks.notConfigured")}</EmptyState>;
}

function SubscriptionRow({
  subscription,
}: Readonly<{ subscription: WebhookSubscription }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <Card inset className="webhook-row">
      <div
        style={{
          display: "flex",
          gap: 8,
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        <span className="t-mono">{subscription.target_url}</span>
        <Badge tone={subscriptionStateTone(subscription.state)}>
          {t(`webhooks.state.${subscription.state}`)}
        </Badge>
      </div>
      <div
        style={{
          display: "flex",
          gap: 6,
          flexWrap: "wrap",
          marginTop: 8,
        }}
      >
        {subscription.event_types.map((eventType) => (
          <Badge key={eventType} tone="accent">
            {eventType}
          </Badge>
        ))}
      </div>
      {subscription.updated_at && (
        <p className="t-caption" style={{ marginTop: 8 }}>
          {t("webhooks.updated", {
            date: formatDateTime(
              subscription.updated_at,
              locale,
              "Europe/Berlin",
            ),
          })}
        </p>
      )}
    </Card>
  );
}

export function WebhooksCard() {
  const t = useT();
  const me = useMe();
  const canManage = canConfigureAutomations(me.data?.roles);
  const query = useWebhookSubscriptions();

  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader title={t("webhooks.title")} sub={t("webhooks.sub")} />
      <QueryGate
        query={query}
        empty={(result) => result.configured && result.data.length === 0}
      >
        {(result) => {
          if (!result.configured) {
            return <NotConfiguredState />;
          }
          return (
            <>
              {canManage && (
                <div style={{ marginBottom: 10 }}>
                  <Button
                    small
                    variant="primary"
                    data-testid="new-webhook-subscription"
                    disabled
                  >
                    <Webhook aria-hidden /> {t("webhooks.new")}
                  </Button>
                </div>
              )}
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  gap: 8,
                }}
              >
                {result.data.map((subscription) => (
                  <SubscriptionRow
                    key={subscription.id}
                    subscription={subscription}
                  />
                ))}
              </div>
            </>
          );
        }}
      </QueryGate>
    </section>
  );
}
