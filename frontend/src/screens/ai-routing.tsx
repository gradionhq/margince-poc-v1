import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Button, Field, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import { problemMessageOf, QueryGate, throwProblem } from "./common";

// Which vendor this installation's text is sent to (ai-operational-spec §1.4).
//
// Read by admin/ops only, unlike most cards here: `ai_routing` is narrow on both
// verbs because nothing needs the grant to SEE which models are bound — the AI
// profile answers that from the running config. This is the editable document,
// and it decides where an installation's correspondence goes.
//
// Editing re-points a lane. It does NOT add or remove one: the tier vocabulary
// comes from the task contract rather than from a person, so the form offers
// the tiers the installation already binds. An installation that binds nothing
// says so and points at where a binding is declared, rather than presenting an
// empty form that cannot be completed here.

type Routing = components["schemas"]["AiRouting"];
type TierBinding = components["schemas"]["AiTierBinding"];

// The adapters a tier may name. Written out because the wire carries a free
// string — the server refuses an unknown one, and a reader choosing from a list
// should not have to discover that by being refused.
const PROVIDERS = [
  "gemini",
  "anthropic",
  "openai",
  "openai_compatible",
  "ollama",
  "vllm",
  "fake",
] as const;

const PROFILES = ["eu_hosted", "sovereign", "cloud_frontier"] as const;

function useRouting() {
  return useQuery({
    queryKey: ["ai-routing"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/ai/routing");
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
  });
}

function useReplaceRouting() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (next: Routing) => {
      const { data, error } = await api.PUT("/ai/routing", { body: next });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["ai-routing"], data);
    },
  });
}

export function AiRoutingCard() {
  const t = useT();
  const canManage = useCanWrite("ai_routing", "update");
  const query = useRouting();

  return (
    <Panel title={t("aiRouting.title")}>
      <PanelBody className="form-stack">
        <p className="t-caption">{t("aiRouting.sub")}</p>
        <QueryGate query={query}>
          {(routing) => <RoutingForm routing={routing} canManage={canManage} />}
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}

// The ladder, cheapest to most capable. Tiers are shown in THIS order rather
// than alphabetically, because alphabetical puts cheap_cloud above frontier and
// local_small above premium — an order that tells a reader nothing about what
// they are choosing between.
//
// A tier this list does not know still renders, last and in a stable order: the
// vocabulary comes from the task contract, so a new one must appear rather than
// vanish from a form that is the only way to bind it.
const TIER_ORDER = [
  "local_small",
  "cheap_cloud",
  "premium",
  "frontier",
  "local_large",
];

function orderedTiers(tiers: Routing["tiers"] | undefined): string[] {
  const rank = (tier: string) => {
    const i = TIER_ORDER.indexOf(tier);
    return i === -1 ? TIER_ORDER.length : i;
  };
  return Object.keys(tiers ?? {}).sort(
    (a, b) => rank(a) - rank(b) || a.localeCompare(b),
  );
}

function RoutingForm({
  routing,
  canManage,
}: Readonly<{ routing: Routing; canManage: boolean }>) {
  const t = useT();
  const replace = useReplaceRouting();
  // The stored document is the starting point, and the reader's edits live
  // here until they save. Keyed on the routing version so a binding another
  // role changed replaces an untouched form rather than being overwritten by
  // it — see the key at the call site.
  const [draft, setDraft] = useState<Routing>(routing);

  // Defensive on a field the contract marks required: a client that dies on an
  // unexpected shape takes the whole settings page with it, and the shape it
  // dies on is the one an error path or an older server produces.
  const tiers = orderedTiers(draft.tiers);
  if (tiers.length === 0) {
    // Not an error and not an empty form: this installation binds nothing, and
    // the way to give it a first binding is the deployment file's seed, which
    // is where a deployment declares one. Saying so beats a form whose every
    // field is blank and whose Save cannot produce a valid document.
    return <Callout tone="info">{t("aiRouting.unbound")}</Callout>;
  }

  const setTier = (tier: string, next: TierBinding) =>
    setDraft((d) => ({ ...d, tiers: { ...d.tiers, [tier]: next } }));

  return (
    <>
      <Field
        label={t("aiRouting.profile.label")}
        hint={t("aiRouting.profile.help")}
      >
        {(control) => (
          <Select
            {...control}
            value={draft.profile}
            disabled={!canManage || replace.isPending}
            options={PROFILES.map((p) => ({
              value: p,
              label: t(`aiRouting.profile.${p}`),
            }))}
            onChange={(value) =>
              setDraft((d) => ({ ...d, profile: value as Routing["profile"] }))
            }
          />
        )}
      </Field>

      {tiers.map((tier) => (
        <TierRow
          key={tier}
          tier={tier}
          binding={draft.tiers[tier]}
          disabled={!canManage || replace.isPending}
          onChange={(next) => setTier(tier, next)}
        />
      ))}

      {replace.isError && (
        <Callout tone="danger" live="alert">
          {problemMessageOf(replace.error, t)}
        </Callout>
      )}
      {replace.isSuccess && (
        <Callout tone="success" live="status">
          {t("aiRouting.saved")}
        </Callout>
      )}
      <Button
        onClick={() => replace.mutate(draft)}
        pending={replace.isPending}
        busyLabel={t("aiRouting.saving")}
        reason={canManage ? undefined : t("aiRouting.adminOnly")}
      >
        {t("aiRouting.save")}
      </Button>
    </>
  );
}

// One tier's binding: which adapter serves it, and which of that adapter's
// models. Two controls rather than one, because the model id is the vendor's
// vocabulary and no list this app holds could stay current with it.
//
// The tier name is rendered RAW, like the provider name beside it. Both are the
// routing document's own vocabulary — an operator editing this reads `premium`
// and `gemini` in the same file — and translating an identifier would need a
// key built at runtime, which the narrow MessageKey type refuses for the good
// reason that a typo in one would otherwise ship.
function TierRow({
  tier,
  binding,
  disabled,
  onChange,
}: Readonly<{
  tier: string;
  binding: TierBinding;
  disabled: boolean;
  onChange: (next: TierBinding) => void;
}>) {
  const t = useT();
  return (
    <div className="form-row" data-testid={`ai-routing-tier-${tier}`}>
      <Field label={tier}>
        {(control) => (
          <Select
            {...control}
            value={binding.provider}
            disabled={disabled}
            options={PROVIDERS.map((p) => ({ value: p, label: p }))}
            onChange={(provider) => onChange({ ...binding, provider })}
          />
        )}
      </Field>
      <Field label={t("aiRouting.model.label")}>
        {(control) => (
          <TextInput
            {...control}
            value={binding.model}
            disabled={disabled}
            onChange={(e) => onChange({ ...binding, model: e.target.value })}
          />
        )}
      </Field>
    </div>
  );
}
