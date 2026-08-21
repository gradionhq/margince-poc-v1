import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Disclosure,
  Field,
  Textarea,
} from "../design-system/atoms";
import { Select } from "../design-system/select";
import { formatDateTime } from "../format/format";
import { viewerZone } from "../format/timezone";
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
// last word. Closed by construction on both sides — no endpoint serves this
// vocabulary, so there is nothing to read it from at runtime.
const SIGNAL_BANDS: Readonly<Record<SignalFactor, readonly string[]>> = {
  web_traffic: ["low", "medium", "high"],
  employees: ["1-10", "11-50", "51-200", "201+"],
  budget_hint: ["none", "unknown", "some", "confirmed"],
};
const SIGNAL_FACTORS = Object.keys(SIGNAL_BANDS) as SignalFactor[];
const SIGNAL_KINDS: readonly SignalKind[] = ["fact", "assumption", "judgement"];

const CONFIDENCE_LEVELS = ["0.5", "0.7", "0.9", "1"] as const;

/**
 * What the wire carries when the rep never opens "More…".
 *
 * `assumption`, not `fact`: the three questions ask what a rep believes about
 * an account, not what they can show. Recording an unqualified answer as a
 * verified fact would put a claim on the score that nobody made, and
 * `assumption` ("a working estimate") is the weakest thing the contract's
 * `LeadManualSignalKind` can say — the enum has no "unstated", so this is what
 * an unopened disclosure honestly means.
 *
 * Confidence stays NULL, which the contract allows and which is the only value
 * that claims nothing. Any number — 0.5 included — is a statement about
 * certainty, and a rep who was never asked made none. Points come from the
 * band alone (`leadManualFactorBands`), so a null confidence neither flatters
 * nor penalises the score.
 *
 * Change either default and every signal entered afterwards means something
 * different from the ones already stored, with nothing on the row to say so.
 */
const DEFAULT_SIGNAL_KIND: SignalKind = "assumption";
const CONFIDENCE_UNSTATED = "";

// One band per factor, "" for a question the rep left alone. Only answered
// questions are written: filling these in from what is already stored would
// re-stamp `set_by`/`set_at` on factors nobody touched.
type Answers = Readonly<Record<SignalFactor, string>>;
const NO_ANSWERS: Answers = {
  web_traffic: "",
  employees: "",
  budget_hint: "",
};

/**
 * LeadManualSignals is the human half of the score (S-E13.6, ADR-0105 §4):
 * a rep enters what capture cannot fetch — a traffic band, an employee
 * count, a budget hint — and it feeds the same transparent score, showing up
 * in the decomposition as its own factor, never blended into an
 * auto-captured one.
 *
 * The form asks the three plain questions and nothing else. Evidence quality
 * and confidence still reach the wire, because they are what makes a manual
 * signal auditable, but they sit behind "More…" with the defaults above: a
 * form that opens by asking a rep to grade their own evidence is a form a rep
 * abandons.
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
  const zone = viewerZone();
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
  const [answers, setAnswers] = useState<Answers>(NO_ANSWERS);
  const [note, setNote] = useState("");
  const [kind, setKind] = useState<SignalKind>(DEFAULT_SIGNAL_KIND);
  const [confidence, setConfidence] = useState<string>(CONFIDENCE_UNSTATED);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["lead", id] });
    queryClient.invalidateQueries({ queryKey: ["leads"] });
    queryClient.invalidateQueries({
      queryKey: ["lead", id, "manual-signals"],
    });
  };
  const set = useMutation({
    // The batch arrives as the mutation's variable, never read back out of
    // render state (mutation-variable-coverage.test.ts): the click that
    // carries it belongs to the render the rep was looking at.
    mutationFn: async (writes: readonly SetSignalRequest[]) => {
      // Sequential on purpose. Each PUT recomputes the lead's score inside
      // its own transaction, so three in flight at once would race that
      // recompute and the last total written could be missing the others'
      // points.
      for (const body of writes) {
        const { error } = await api.PUT("/leads/{id}/manual-signals", {
          params: { path: { id } },
          body,
        });
        if (error) {
          throwProblem(error, t);
        }
        // Retired as it lands, not once the whole batch has. A batch can stop
        // part-way through, and an answer still on the form after the server
        // took it is one the next save re-sends: that re-stamps
        // `set_by`/`set_at` on a factor nobody touched the second time and
        // appends a history row recording no change. What stays on the form
        // is exactly what is still outstanding.
        setAnswers((prev) => ({ ...prev, [body.factor]: "" }));
      }
    },
    onSuccess: () => {
      setNote("");
      setKind(DEFAULT_SIGNAL_KIND);
      setConfidence(CONFIDENCE_UNSTATED);
    },
    // On failure too: a batch can stop part-way through, and the list above is
    // then the only place the rep can see which answers actually landed.
    onSettled: invalidate,
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
  const answered = SIGNAL_FACTORS.filter((name) => answers[name] !== "");
  const label = (key: string) => t(`lead.signal.${key}` as MessageKey);

  // "How do you know?" is optional on screen, while `reason` is required and
  // non-empty on the wire — a scoring input nobody can account for is what
  // this feature exists to end. A blank line therefore sends a sentence
  // saying exactly that, never an invented justification, and it is
  // translated because every other reason on this list is written in the
  // language its author used.
  const writes = (): readonly SetSignalRequest[] =>
    answered.map((name) => ({
      factor: name,
      band: answers[name],
      signal_kind: kind,
      confidence:
        confidence === CONFIDENCE_UNSTATED ? null : Number(confidence),
      reason: note.trim() === "" ? t("lead.signalReasonUnstated") : note.trim(),
    }));

  return (
    <div className="form-stack">
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
        <div className="form-stack">
          {SIGNAL_FACTORS.map((name) => (
            <Field key={name} label={label(`ask.${name}`)}>
              {(control) => (
                <Select
                  {...control}
                  value={answers[name]}
                  disabled={!canEdit}
                  placeholder={t("lead.signalBandPick")}
                  onChange={(next) =>
                    setAnswers((prev) => ({ ...prev, [name]: next }))
                  }
                  options={SIGNAL_BANDS[name].map((value) => ({
                    value,
                    label: label(`${name}.${value}`),
                  }))}
                />
              )}
            </Field>
          ))}
          <Field
            label={t("lead.signalReason")}
            hint={t("lead.signalReasonHint")}
          >
            {(control) => (
              <Textarea
                {...control}
                value={note}
                disabled={!canEdit}
                onChange={(event) => setNote(event.target.value)}
              />
            )}
          </Field>
          <Disclosure summary={t("lead.signalMore")}>
            <div className="form-stack">
              <p className="t-caption">{t("lead.signalProvenanceHint")}</p>
              <Field label={t("lead.signalEvidenceQuality")}>
                {(control) => (
                  <Select
                    {...control}
                    value={kind}
                    disabled={!canEdit}
                    onChange={(next) => {
                      // `find` rather than an assertion: the Select reports a
                      // string, and only a value the catalog actually holds
                      // may become the kind on the wire.
                      const picked = SIGNAL_KINDS.find(
                        (value) => value === next,
                      );
                      if (picked) {
                        setKind(picked);
                      }
                    }}
                    options={SIGNAL_KINDS.map((value) => ({
                      value,
                      label: label(value),
                    }))}
                  />
                )}
              </Field>
              <Field label={t("lead.signalConfidence")}>
                {(control) => (
                  <Select
                    {...control}
                    value={confidence}
                    disabled={!canEdit}
                    placeholder={t("lead.signalConfidenceUnstated")}
                    onChange={setConfidence}
                    options={[
                      {
                        value: CONFIDENCE_UNSTATED,
                        label: t("lead.signalConfidenceUnstated"),
                      },
                      ...CONFIDENCE_LEVELS.map((value) => ({
                        value,
                        label: t("lead.signalConfidenceValue", {
                          value: Math.round(Number(value) * 100),
                        }),
                      })),
                    ]}
                  />
                )}
              </Field>
            </div>
          </Disclosure>
          {(set.isError || clear.isError) && (
            <span className="t-caption form-error">
              {problemMessageOf(set.isError ? set.error : clear.error, t)}
            </span>
          )}
          <div className="form-actions">
            <Button
              small
              variant="primary"
              disabled={!canEdit || answered.length === 0}
              onClick={() => set.mutate(writes())}
            >
              {t("lead.signalSave")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
