import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Webhook } from "lucide-react";
import { useId, useState } from "react";
import { api } from "../api/client";
import { subscribableEventTypeValues } from "../api/public-events";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Modal,
  SectionHeader,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { ArchiveAction } from "./archive";
import {
  canConfigureAutomations,
  problemMessage,
  QueryGate,
  throwProblem,
  useMe,
} from "./common";
import {
  type CreateField,
  type CreateFieldOption,
  CreateRecordModal,
  joinMultiselectValue,
  splitMultiselectValue,
} from "./create";
import { EditAction } from "./edit";

// Settings → Integrations (B-E10.14): the subscription list for outbound
// webhooks. The list wire (WebhookSubscription) carries no per-item delivery
// health — that lives on the separate deliveries sub-resource, out of this
// card's scope — so the health line here renders only what the list itself
// is honest about: state, the subscribed event set, and last-updated. A
// deployment with no signing key answers 503 webhooks_not_configured; that
// is a deliberate, documented feature-off state, never an error.

type WebhookSubscription = components["schemas"]["WebhookSubscription"];
type WebhookDeliveryStatus = components["schemas"]["WebhookDelivery"]["status"];
type UpdateWebhookSubscriptionRequest =
  components["schemas"]["UpdateWebhookSubscriptionRequest"];

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

type WebhookSubscriptionCreated =
  components["schemas"]["WebhookSubscriptionCreated"];

// The event-type multiselect's options: the checkbox label IS the wire value
// (there is no translated display name per event, so showing the raw type —
// e.g. "deal.stage_changed" — is honest, and matches how SubscriptionRow
// already renders a subscription's chosen types above). The list itself
// comes from `subscribableEventTypeValues`, the ONE runtime array
// `pnpm gen:events` derives straight from the backend's published
// SubscribableEventType enum (backend/api/public-events.yaml) — never a
// hand-maintained list here, so a catalog change can't silently drift.
const EVENT_TYPE_OPTIONS: CreateFieldOption[] = subscribableEventTypeValues.map(
  (eventType) => ({ value: eventType, label: eventType }),
);

const CREATE_SUBSCRIPTION_FIELDS: CreateField[] = [
  {
    key: "target_url",
    label: "webhooks.field.targetUrl",
    type: "text",
    required: true,
    placeholder: "https://example.test/hooks/margince",
  },
  {
    key: "event_types",
    label: "webhooks.field.eventTypes",
    type: "multiselect",
    required: true,
    options: EVENT_TYPE_OPTIONS,
  },
];

// The edit form: pause/resume (state) and re-target the subscribed event set
// (event_types) — the only two fields `UpdateWebhookSubscriptionRequest`
// accepts (the contract has no target_url update; re-targeting means the
// event set, not the URL). `event_types`'s `toInput` joins the record's
// `string[]` through the SAME multiselect delimiter the field's own
// checkbox-toggle uses, so the edit form prefills the subscription's
// current selection rather than falling back to Array#toString's
// coincidentally-matching-but-unspecified comma join.
function editSubscriptionFields(t: (key: MessageKey) => string): CreateField[] {
  return [
    {
      key: "state",
      label: "webhooks.field.state",
      type: "select",
      required: true,
      options: [
        { value: "active", label: t("webhooks.state.active") },
        { value: "paused", label: t("webhooks.state.paused") },
      ],
    },
    {
      key: "event_types",
      label: "webhooks.field.eventTypes",
      type: "multiselect",
      required: true,
      options: EVENT_TYPE_OPTIONS,
      toInput: (raw) =>
        joinMultiselectValue(Array.isArray(raw) ? (raw as string[]) : []),
    },
  ];
}

// The PATCH body from the edit form's values — the ONE place that knows the
// form's comma-joined `event_types` string decodes back to the wire's
// `string[]`, so a screen mistake here can't silently drop the split.
export function mapWebhookUpdate(
  values: Record<string, unknown>,
): UpdateWebhookSubscriptionRequest {
  return {
    state: values.state as WebhookSubscription["state"],
    event_types: splitMultiselectValue(String(values.event_types ?? "")),
  };
}

// Registering a subscription is registering outbound egress, not landing on
// a record 360 — there is no webhook-subscription screen to navigate to, so
// this is a bespoke mutation (mirrors tasks.tsx's create-in-place) rather
// than the shared CreateAction choreography, whose success path always
// navigates. On success it hands the one-time `signing_secret` up so the
// card can reveal it, and invalidates the list query so the refreshed list
// (which the wire never carries the secret on) replaces it.
function useCreateWebhookSubscription(onCreated: (secret: string) => void) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (
      values: Record<string, string>,
    ): Promise<WebhookSubscriptionCreated> => {
      const { data, error } = await api.POST("/webhook-subscriptions", {
        body: {
          target_url: values.target_url.trim(),
          event_types: splitMultiselectValue(values.event_types ?? ""),
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ["webhook-subscriptions"] });
      onCreated(created.signing_secret);
    },
  });
}

// The EditAction transport: PATCH with If-Match(current version) — the
// standard optimistic-concurrency precondition every native mutating
// endpoint accepts. A 409 code:version_skew surfaces through EditAction's own
// error handling (edit.tsx), never handled again here.
function updateWebhookSubscription(
  subscription: WebhookSubscription,
): (values: Record<string, unknown>) => Promise<WebhookSubscription> {
  return async (values) => {
    const { data, error } = await api.PATCH("/webhook-subscriptions/{id}", {
      params: {
        path: { id: subscription.id },
        ...ifMatch(subscription.version),
      },
      body: mapWebhookUpdate(values),
    });
    if (error) {
      throwProblem(error);
    }
    return data;
  };
}

// Archive stops all delivery (DELETE, no If-Match — mirrors products.tsx/
// people.tsx's ArchiveAction usage: archiving isn't a concurrent-edit hazard
// the way a field patch is).
async function archiveWebhookSubscription(
  subscription: WebhookSubscription,
): Promise<WebhookSubscription> {
  const { data, error } = await api.DELETE("/webhook-subscriptions/{id}", {
    params: { path: { id: subscription.id } },
  });
  if (error) {
    throwProblem(error);
  }
  return data ?? subscription;
}

// Rotate-secret: a Button + the shared ConfirmModal chrome (mirrors offers.tsx's
// RejectOfferAction) guarding the one irreversible side effect — the OLD
// secret stops verifying the moment this succeeds. The new secret is handed
// up to the card so it reuses the SAME SecretRevealModal a create shows.
function RotateSecretAction({
  subscription,
  onRotated,
}: Readonly<{
  subscription: WebhookSubscription;
  onRotated: (secret: string) => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const mutation = useMutation({
    mutationFn: async (): Promise<WebhookSubscriptionCreated> => {
      const { data, error } = await api.POST(
        "/webhook-subscriptions/{id}/rotate-secret",
        { params: { path: { id: subscription.id } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ["webhook-subscriptions"] });
      queryClient.invalidateQueries({
        queryKey: ["webhook-subscription", subscription.id],
      });
      setConfirming(false);
      onRotated(created.signing_secret);
    },
  });

  return (
    <>
      <Button
        small
        onClick={() => setConfirming(true)}
        data-testid="rotate-webhook-secret"
      >
        {t("webhooks.rotate")}
      </Button>
      <ConfirmModal
        open={confirming}
        onClose={() => setConfirming(false)}
        title={t("webhooks.rotateConfirm.title")}
        confirmLabel={t("deals.confirm")}
        onConfirm={() => mutation.mutate()}
        pending={mutation.isPending}
        error={mutation.isError ? mutation.error.message : null}
      >
        <p className="t-body">{t("webhooks.rotateConfirm.body")}</p>
      </ConfirmModal>
    </>
  );
}

// The one-time secret reveal: shown immediately after a successful create,
// gone the moment this modal closes — the secret lives only in the parent's
// local state (never react-query cache, never re-derivable from a list/get
// response) and there is no way back to it once dismissed.
function SecretRevealModal({
  secret,
  onClose,
}: Readonly<{ secret: string; onClose: () => void }>) {
  const t = useT();
  const headingId = useId();
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);

  async function copySecret() {
    if (!navigator.clipboard) {
      setCopyFailed(true);
      return;
    }
    try {
      await navigator.clipboard.writeText(secret);
      setCopied(true);
      setCopyFailed(false);
    } catch {
      setCopied(false);
      setCopyFailed(true);
    }
  }

  return (
    <Modal open onClose={onClose} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {t("webhooks.secret.title")}
      </h2>
      <p className="t-caption" style={{ marginBottom: "var(--space-2)" }}>
        {t("webhooks.secret.warning")}
      </p>
      <pre className="code-block t-mono" data-testid="webhook-signing-secret">
        {secret}
      </pre>
      {copyFailed && (
        <p
          role="alert"
          className="t-caption"
          style={{ color: "var(--danger)" }}
        >
          {t("webhooks.secret.copyFailed")}
        </p>
      )}
      <div className="actions">
        <Button small onClick={() => void copySecret()}>
          {copied ? t("webhooks.secret.copied") : t("webhooks.secret.copy")}
        </Button>
        <Button small variant="primary" onClick={onClose}>
          {t("webhooks.secret.done")}
        </Button>
      </div>
    </Modal>
  );
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
  canManage,
  onRotated,
}: Readonly<{
  subscription: WebhookSubscription;
  canManage: boolean;
  onRotated: (secret: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <Card inset className="webhook-row">
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
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
          gap: "var(--space-2)",
          flexWrap: "wrap",
          marginTop: "var(--space-2)",
        }}
      >
        {subscription.event_types.map((eventType) => (
          <Badge key={eventType} tone="accent">
            {eventType}
          </Badge>
        ))}
      </div>
      {subscription.updated_at && (
        <p className="t-caption" style={{ marginTop: "var(--space-2)" }}>
          {t("webhooks.updated", {
            date: formatDateTime(
              subscription.updated_at,
              locale,
              "Europe/Berlin",
            ),
          })}
        </p>
      )}
      {canManage && (
        <div
          style={{
            display: "flex",
            gap: "var(--space-2)",
            marginTop: "var(--space-2)",
          }}
        >
          <EditAction
            label={t("webhooks.edit")}
            invalidate="webhook-subscriptions"
            recordKey="webhook-subscription"
            record={{ ...subscription }}
            update={updateWebhookSubscription(subscription)}
            fields={editSubscriptionFields(t)}
          />
          <RotateSecretAction
            subscription={subscription}
            onRotated={onRotated}
          />
          <ArchiveAction
            label={t("webhooks.archive")}
            confirmText={t("webhooks.archiveConfirm")}
            invalidate="webhook-subscriptions"
            recordKey="webhook-subscription"
            onArchived={() => {}}
            archive={() => archiveWebhookSubscription(subscription)}
          />
        </div>
      )}
    </Card>
  );
}

export function WebhooksCard() {
  const t = useT();
  const me = useMe();
  const canManage = canConfigureAutomations(me.data?.roles);
  const query = useWebhookSubscriptions();
  const [creating, setCreating] = useState(false);
  const [revealedSecret, setRevealedSecret] = useState<string | null>(null);
  const create = useCreateWebhookSubscription((secret) => {
    setCreating(false);
    setRevealedSecret(secret);
  });
  // Gated on the deployment actually being configured (never on the CURRENT
  // list happening to be empty) — the button lives outside QueryGate's
  // render-prop specifically so the very first subscription (the empty-list
  // case) is still creatable; QueryGate's `empty` branch renders the shared
  // EmptyState in place of `children`, which would otherwise swallow a
  // button nested inside it.
  const canCreate = canManage && query.data?.configured === true;

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader title={t("webhooks.title")} sub={t("webhooks.sub")} />
      {canCreate && (
        <div style={{ marginBottom: "var(--space-3)" }}>
          <Button
            small
            variant="primary"
            data-testid="new-webhook-subscription"
            onClick={() => setCreating(true)}
          >
            <Webhook aria-hidden /> {t("webhooks.new")}
          </Button>
        </div>
      )}
      <QueryGate
        query={query}
        empty={(result) => result.configured && result.data.length === 0}
      >
        {(result) => {
          if (!result.configured) {
            return <NotConfiguredState />;
          }
          return (
            <div
              style={{
                display: "flex",
                flexDirection: "column",
                gap: "var(--space-2)",
              }}
            >
              {result.data.map((subscription) => (
                <SubscriptionRow
                  key={subscription.id}
                  subscription={subscription}
                  canManage={canManage}
                  onRotated={setRevealedSecret}
                />
              ))}
            </div>
          );
        }}
      </QueryGate>
      {canCreate && (
        <CreateRecordModal
          open={creating}
          onClose={() => setCreating(false)}
          title={t("webhooks.new")}
          fields={CREATE_SUBSCRIPTION_FIELDS}
          pending={create.isPending}
          error={create.isError ? create.error.message : null}
          onSubmit={(values) => create.mutate(values)}
        />
      )}
      {revealedSecret && (
        <SecretRevealModal
          secret={revealedSecret}
          onClose={() => setRevealedSecret(null)}
        />
      )}
    </section>
  );
}
