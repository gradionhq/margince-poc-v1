import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useInstallationSettings } from "../app/uploadlimit";
import {
  Button,
  Checkbox,
  Field,
  Modal,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Select } from "../design-system/select";
import { formatDate } from "../format/format";
import { toMinorUnits } from "../format/minorunits";
import { viewerZone } from "../format/timezone";
import { type Locale, useLocale, useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import { EntityRef } from "./entityref";

type Lead = components["schemas"]["Lead"];
type PromoteLeadRequest = components["schemas"]["PromoteLeadRequest"];
type PromoteLeadResponse = components["schemas"]["PromoteLeadResponse"];
type Pipeline = components["schemas"]["Pipeline"];

// The qualify dialog: "this lead is now a contact, and maybe an opportunity".
// Three blocks — what happens to the contact (read from the server's own
// preview), an optional deal opened in the same transaction, and why: the
// reason is DERIVED from what was captured (the strongest engagement signal)
// rather than asked for as a technical trigger; a rep who qualifies a lead
// nobody has heard from is recorded as the human who decided it.

function usePromotePreview(id: string, open: boolean) {
  const t = useT();
  return useQuery({
    queryKey: ["lead-promote-preview", id],
    enabled: open,
    // Always fresh: the answer is about the workspace as it stands the moment
    // the dialog opens, and a 30s-old "create" can be a merge by now.
    staleTime: 0,
    queryFn: async () => {
      const { data, error } = await api.GET("/leads/{id}/promote-preview", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
  });
}

function usePipelinesForQualify(open: boolean) {
  return useQuery({
    queryKey: ["pipelines", "all"],
    enabled: open,
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
  });
}

// triggerFor reads the wire trigger off the lead's captured evidence; a lead
// with none is qualified by the human's own judgement.
export function triggerFor(lead: Lead): PromoteLeadRequest["trigger"] {
  return lead.qualification_evidence?.trigger ?? "human_qualify";
}

function reasonSentence(
  lead: Lead,
  t: ReturnType<typeof useT>,
  locale: Locale,
  zone: string,
): string {
  const evidence = lead.qualification_evidence;
  if (!evidence) {
    return t("lead.qualify.reasonHuman");
  }
  const at = evidence.occurred_at
    ? formatDate(evidence.occurred_at, locale, zone)
    : "";
  switch (evidence.trigger) {
    case "meeting_held":
      return t("lead.qualify.reasonMeetingHeld", { at });
    case "meeting_booked":
      return t("lead.qualify.reasonMeetingBooked", { at });
    default:
      return t("lead.qualify.reasonReplied", { at });
  }
}

// dealBlock is the wire's deal request from the dialog's fields. The money
// pair holds from birth: an amount only travels with the currency, and an
// empty or unreadable amount travels as no money at all.
function dealBlock(input: {
  pipelineId: string | undefined;
  stageId: string | undefined;
  name: string;
  amount: string;
  currency: string | undefined;
}): NonNullable<PromoteLeadRequest["deal"]> {
  const parsed = Number(input.amount);
  const priced =
    input.amount.trim() !== "" && Number.isFinite(parsed) && input.currency;
  return {
    pipeline_id: input.pipelineId ?? null,
    stage_id: input.stageId ?? null,
    name: input.name.trim() || null,
    // priced is false without a currency, so the code below is always the
    // real one — a lead promoted at a dong price is stored as dong, not as a
    // hundredth of one.
    amount_minor: priced ? toMinorUnits(parsed, input.currency ?? "") : null,
    currency: priced ? (input.currency ?? null) : null,
  };
}

// amountState is what the typed amount does to the confirm: an unreadable
// number is refused, and a number with no currency to carry it waits — sent
// as-is it would be dropped on the way to the server, which is worse than a
// wait.
function amountState(amount: string, currency: string | undefined) {
  const typed = amount.trim() !== "";
  return {
    amountInvalid: typed && !Number.isFinite(Number(amount)),
    amountWaitsForCurrency: typed && !currency,
  };
}

function openStagesOf(pipeline: Pipeline | undefined) {
  return [...(pipeline?.stages ?? [])]
    .filter((stage) => stage.semantic === "open")
    .sort((a, b) => a.position - b.position);
}

export function QualifyDialog({
  lead,
  open,
  onClose,
  onQualified,
}: Readonly<{
  lead: Lead;
  open: boolean;
  onClose: () => void;
  onQualified: (result: PromoteLeadResponse) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const zone = viewerZone();
  const headingId = useId();
  const queryClient = useQueryClient();
  const preview = usePromotePreview(lead.id, open);
  const pipelines = usePipelinesForQualify(open);
  // The money pair holds from birth: an amount travels with the
  // installation's base currency, the only one a qualify dialog can name.
  const installation = useInstallationSettings();
  const currency = installation.data?.base_currency;
  // An engaged lead is one a rep has something to sell to, so the deal
  // block starts ticked for it and unticked otherwise.
  const [withDeal, setWithDeal] = useState(lead.status === "engaged");
  const [pipelineId, setPipelineId] = useState("");
  const [stageId, setStageId] = useState("");
  const [dealName, setDealName] = useState("");
  const [amount, setAmount] = useState("");
  const [note, setNote] = useState("");

  const defaultPipeline =
    pipelines.data?.find((p) => p.is_default) ?? pipelines.data?.[0];
  const chosenPipeline =
    pipelines.data?.find((p) => p.id === pipelineId) ?? defaultPipeline;
  const stages = openStagesOf(chosenPipeline);
  const chosenStage = stages.find((s) => s.id === stageId) ?? stages[0];
  const suggestedName = chosenStage
    ? `${lead.company_name ?? lead.full_name ?? lead.email ?? ""} — ${chosenStage.name}`
    : (lead.company_name ?? lead.full_name ?? "");

  const qualify = useMutation({
    mutationFn: async (body: PromoteLeadRequest) => {
      const { data, error } = await api.POST("/leads/{id}/promote", {
        params: { path: { id: lead.id } },
        body,
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ["leads"] });
      queryClient.invalidateQueries({ queryKey: ["lead", lead.id] });
      // The promotion WROTE the audit row the page reads its outcome from; a
      // cached history page saying there is nothing more would hide it.
      queryClient.invalidateQueries({
        queryKey: ["record-history", "lead", lead.id],
      });
      if (result.deal_id) {
        queryClient.invalidateQueries({ queryKey: ["deals"] });
      }
      onQualified(result);
    },
  });

  // A promotion in flight is not something to walk away from: the dialog
  // stays until the server has answered, then closes on success or shows
  // the refusal.
  const close = () => {
    if (qualify.isPending) return;
    qualify.reset();
    onClose();
  };

  const submit = () => {
    const trimmedNote = note.trim();
    qualify.mutate({
      trigger: triggerFor(lead),
      evidence: {
        activity_id: lead.qualification_evidence?.activity_id ?? null,
        note: trimmedNote || null,
      },
      deal: withDeal
        ? dealBlock({
            pipelineId: chosenPipeline?.id,
            stageId: chosenStage?.id,
            name: dealName.trim() || suggestedName,
            amount,
            currency,
          })
        : undefined,
    });
  };

  const { amountInvalid, amountWaitsForCurrency } = amountState(
    amount,
    currency,
  );
  const name = lead.full_name ?? lead.email ?? "";

  return (
    <Modal open={open} onClose={close} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {t("lead.qualify.title", { name })}
      </h2>
      <div className="lead-qualify">
        <section className="lead-qualify-block">
          <h3 className="t-label">{t("lead.qualify.contact")}</h3>
          <PreviewSentence preview={preview} t={t} />
        </section>

        <section className="lead-qualify-block">
          <Checkbox
            label={t("lead.qualify.alsoDeal")}
            checked={withDeal}
            data-testid="lead-qualify-with-deal"
            onChange={(event) => setWithDeal(event.target.checked)}
          />
          {withDeal && (
            <div className="lead-qualify-deal">
              <Field label={t("lead.qualify.pipeline")}>
                {(control) => (
                  <Select
                    {...control}
                    value={chosenPipeline?.id ?? ""}
                    onChange={(value) => {
                      setPipelineId(value);
                      setStageId("");
                    }}
                    options={(pipelines.data ?? []).map((p) => ({
                      value: p.id,
                      label: p.name,
                    }))}
                  />
                )}
              </Field>
              <Field label={t("lead.qualify.stage")}>
                {(control) => (
                  <Select
                    {...control}
                    value={chosenStage?.id ?? ""}
                    onChange={setStageId}
                    options={stages.map((s) => ({
                      value: s.id,
                      label: s.name,
                    }))}
                  />
                )}
              </Field>
              <Field label={t("lead.qualify.dealName")}>
                {(control) => (
                  <TextInput
                    {...control}
                    data-testid="lead-qualify-deal-name"
                    value={dealName}
                    placeholder={suggestedName}
                    onChange={(event) => setDealName(event.target.value)}
                  />
                )}
              </Field>
              <Field
                label={t("lead.qualify.amount", { currency: currency ?? "" })}
                hint={t("lead.qualify.amountHint")}
                error={
                  amountInvalid
                    ? t("lead.qualify.amountInvalid")
                    : amountWaitsForCurrency
                      ? t("lead.qualify.amountNoCurrency")
                      : undefined
                }
              >
                {(control) => (
                  <TextInput
                    {...control}
                    inputMode="decimal"
                    value={amount}
                    onChange={(event) => setAmount(event.target.value)}
                  />
                )}
              </Field>
            </div>
          )}
        </section>

        <section className="lead-qualify-block">
          <h3 className="t-label">{t("lead.qualify.why")}</h3>
          <p className="t-small">{reasonSentence(lead, t, locale, zone)}</p>
          <Field label={t("lead.evidenceNote")}>
            {(control) => (
              <Textarea
                {...control}
                value={note}
                onChange={(event) => setNote(event.target.value)}
              />
            )}
          </Field>
        </section>

        {qualify.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(qualify.error, t)}
          </Callout>
        )}
        <div
          style={{
            display: "flex",
            gap: "var(--space-2)",
            justifyContent: "flex-end",
          }}
        >
          <Button small onClick={close} disabled={qualify.isPending}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="primary"
            data-testid="lead-qualify-confirm"
            disabled={
              qualify.isPending || amountInvalid || amountWaitsForCurrency
            }
            onClick={submit}
          >
            {withDeal
              ? t("lead.qualify.confirmWithDeal")
              : t("lead.qualify.confirm")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function PreviewSentence({
  preview,
  t,
}: Readonly<{
  preview: ReturnType<typeof usePromotePreview>;
  t: ReturnType<typeof useT>;
}>) {
  if (preview.isPending) {
    return <p className="t-small">{t("lead.previewPending")}</p>;
  }
  // A failed preview does not block the qualification; the confirm still
  // runs the same ladder. It just cannot be described in advance.
  if (preview.isError || !preview.data) {
    return null;
  }
  if (preview.data.outcome === "create") {
    return <p className="t-small">{t("lead.previewCreate")}</p>;
  }
  if (!preview.data.person) {
    return <p className="t-small">{t("lead.previewMergeWithheld")}</p>;
  }
  return (
    <p className="t-small">
      {t("lead.previewMerge")}{" "}
      <EntityRef kind="person" id={preview.data.person.id} />
    </p>
  );
}
