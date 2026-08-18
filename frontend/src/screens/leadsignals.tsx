import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, Textarea } from "../design-system/atoms";
import { Select } from "../design-system/select";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";
import { EntityRef } from "./entityref";

type SetSignalRequest = components["schemas"]["SetLeadManualSignalRequest"];
type SignalFactor = SetSignalRequest["factor"];
type SignalKind = SetSignalRequest["signal_kind"];
type ManualSignal = components["schemas"]["LeadManualSignal"];

// The §24 catalog the server enforces (leadmanualsignal.go): the three
// factors a human may supply and the bands each accepts. Spelled here so the
// form offers only what the server will take; the server's 422 remains the
// last word.
const SIGNAL_BANDS: Readonly<Record<SignalFactor, readonly string[]>> = {
  web_traffic: ["low", "medium", "high"],
  employees: ["1-10", "11-50", "51-200", "201+"],
  budget_hint: ["none", "unknown", "some", "confirmed"],
};
const SIGNAL_FACTORS = Object.keys(SIGNAL_BANDS) as SignalFactor[];
const SIGNAL_KINDS: readonly SignalKind[] = ["fact", "assumption", "judgement"];

const CONFIDENCE_LEVELS = ["0.5", "0.7", "0.9", "1"] as const;

/**
 * LeadManualSignals is the human half of the score (S-E13.6, ADR-0105 §4):
 * a rep enters what capture cannot fetch — a traffic band, an employee
 * count, a budget hint — with the kind of claim it is and a written reason.
 * It feeds the same transparent score and shows up in the decomposition as
 * its own factor, never blended into an auto-captured one.
 *
 * Read-only on a terminal lead, WITH the reason: the inputs a rep made are
 * part of why the lead was worked, and hiding them on a closed lead would
 * hide the fact the reader came for (STATE-4a).
 */
export function LeadManualSignals({
  id,
  readOnlyReason,
}: Readonly<{ id: string; readOnlyReason?: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const queryClient = useQueryClient();
  const signals = useQuery({
    queryKey: ["lead", id, "manual-signals"],
    queryFn: async () => {
      const { data, error } = await api.GET("/leads/{id}/manual-signals", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
  });
  const [factor, setFactor] = useState<SignalFactor>("web_traffic");
  const [band, setBand] = useState("");
  const [kind, setKind] = useState<SignalKind>("fact");
  const [confidence, setConfidence] = useState("0.9");
  const [reason, setReason] = useState("");

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["lead", id] });
    queryClient.invalidateQueries({ queryKey: ["leads"] });
    queryClient.invalidateQueries({
      queryKey: ["lead", id, "manual-signals"],
    });
  };
  const set = useMutation({
    mutationFn: async (body: SetSignalRequest) => {
      const { data, error } = await api.PUT("/leads/{id}/manual-signals", {
        params: { path: { id } },
        body,
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      invalidate();
      setBand("");
      setReason("");
    },
  });
  const clear = useMutation({
    mutationFn: async (target: SignalFactor) => {
      const { error } = await api.DELETE(
        "/leads/{id}/manual-signals/{factor}",
        { params: { path: { id, factor: target } } },
      );
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: invalidate,
  });
  const busy = set.isPending || clear.isPending;
  const canEdit = !readOnlyReason && !busy;
  const submittable = band !== "" && reason.trim() !== "";
  const label = (key: string) => t(`lead.signal.${key}` as MessageKey);

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-2)",
      }}
    >
      <span className="t-caption">{t("lead.signalsTitle")}</span>
      {/* An absent factor list is not an empty one: while the explanation is
          loading, failed, or not yet retained (ADR-0105 §1), nothing here can
          say what is set, so nothing here claims "not entered". */}
      {signals.isPending && (
        <span className="t-caption">{t("lead.scoreLoading")}</span>
      )}
      {signals.isError && (
        <span className="t-caption">{problemMessageOf(signals.error, t)}</span>
      )}
      {signals.isSuccess && (
        <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
          {SIGNAL_FACTORS.map((name) => {
            const entries = signals.data.filter(
              (entry: ManualSignal) => entry.factor === name,
            );
            const live = entries.find((entry) => !entry.superseded_at);
            const superseded = entries.filter((entry) => entry.superseded_at);
            return (
              <li
                key={name}
                style={{
                  display: "flex",
                  gap: "var(--space-2)",
                  alignItems: "baseline",
                  flexWrap: "wrap",
                }}
              >
                <span>{label(name)}</span>
                {live ? (
                  <>
                    <strong>{label(`${name}.${live.band}`)}</strong>
                    <Badge>
                      {live.points >= 0 ? "+" : ""}
                      {live.points}
                    </Badge>
                    <span className="t-caption">{label(live.signal_kind)}</span>
                    {live.confidence != null && (
                      <span className="t-caption">
                        {t("lead.signalConfidenceValue", {
                          value: Math.round(live.confidence * 100),
                        })}
                      </span>
                    )}
                    <span className="t-caption">{live.reason}</span>
                    <span className="t-caption">
                      <EntityRef kind="user" id={live.set_by} />
                    </span>
                    <span className="t-caption">
                      {t("lead.signalRecordedAt", {
                        at: formatDateTime(live.set_at, locale, zone),
                      })}
                    </span>
                    {!readOnlyReason && (
                      <Button
                        small
                        disabled={busy}
                        onClick={() => clear.mutate(name)}
                      >
                        {t("lead.signalClear")}
                      </Button>
                    )}
                  </>
                ) : (
                  <span className="t-caption">{t("lead.signalUnset")}</span>
                )}
                {superseded.map((entry) => (
                  <span
                    className="t-caption"
                    key={`${entry.set_at}-${entry.band}`}
                  >
                    {t("lead.signalSuperseded", {
                      value: label(`${name}.${entry.band}`),
                      source:
                        entry.superseded_by ?? t("lead.signalAutomaticSource"),
                    })}
                  </span>
                ))}
              </li>
            );
          })}
        </ul>
      )}
      {readOnlyReason ? (
        <span className="t-caption">{readOnlyReason}</span>
      ) : (
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            gap: "var(--space-2)",
          }}
        >
          <label className="t-caption field">
            {t("lead.signalFactor")}
            <Select
              aria-label={t("lead.signalFactor")}
              value={factor}
              disabled={!canEdit}
              onChange={(next) => {
                if (SIGNAL_FACTORS.includes(next as SignalFactor)) {
                  // Band, kind and reason describe ONE factor together; a
                  // reason typed for the old factor must not be recorded
                  // against the new one.
                  setFactor(next as SignalFactor);
                  setBand("");
                  setKind("fact");
                  setConfidence("0.9");
                  setReason("");
                }
              }}
              options={SIGNAL_FACTORS.map((name) => ({
                value: name,
                label: label(name),
              }))}
            />
          </label>
          <label className="t-caption field">
            {t("lead.signalBand")}
            <Select
              aria-label={t("lead.signalBand")}
              value={band}
              disabled={!canEdit}
              placeholder={t("lead.signalBandPick")}
              onChange={setBand}
              options={SIGNAL_BANDS[factor].map((value) => ({
                value,
                label: label(`${factor}.${value}`),
              }))}
            />
          </label>
          <label className="t-caption field">
            {t("lead.signalEvidenceQuality")}
            <Select
              aria-label={t("lead.signalKind")}
              value={kind}
              disabled={!canEdit}
              onChange={(next) => {
                if (SIGNAL_KINDS.includes(next as SignalKind)) {
                  setKind(next as SignalKind);
                }
              }}
              options={SIGNAL_KINDS.map((value) => ({
                value,
                label: label(value),
              }))}
            />
          </label>
          <label className="t-caption field">
            {t("lead.signalConfidence")}
            <Select
              aria-label={t("lead.signalConfidence")}
              value={confidence}
              disabled={!canEdit}
              onChange={setConfidence}
              options={CONFIDENCE_LEVELS.map((value) => ({
                value,
                label: t("lead.signalConfidenceValue", {
                  value: Math.round(Number(value) * 100),
                }),
              }))}
            />
          </label>
          <label className="t-caption field">
            {t("lead.signalReason")}
            <Textarea
              aria-label={t("lead.signalReason")}
              value={reason}
              disabled={!canEdit}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
          {(set.isError || clear.isError) && (
            <span className="t-caption" style={{ color: "var(--danger)" }}>
              {problemMessageOf(set.isError ? set.error : clear.error, t)}
            </span>
          )}
          <div>
            <Button
              small
              variant="primary"
              disabled={!canEdit || !submittable}
              onClick={() =>
                set.mutate({
                  factor,
                  band,
                  signal_kind: kind,
                  confidence: Number(confidence),
                  reason: reason.trim(),
                })
              }
            >
              {t("lead.signalSave")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
