import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState, Skeleton } from "../design-system/atoms";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody } from "../design-system/panel";
import { Meter } from "../design-system/readings";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";
import {
  type BriefSentence,
  RECORD_ZONE,
  SentenceList,
  WrittenBy,
} from "./company360";

type GrowthFit = components["schemas"]["OrganizationGrowthFit"];
type Band = GrowthFit["band"];

type SubScoreDimension = NonNullable<
  GrowthFit["sub_scores"]
>[number]["dimension"];

const SUB_SCORE_LABELS: Record<SubScoreDimension, MessageKey> = {
  industry_fit: "co.growthFit.dim.industryFit",
  company_size: "co.growthFit.dim.companySize",
  transformation_need: "co.growthFit.dim.transformationNeed",
  access: "co.growthFit.dim.access",
};

const BAND_LABELS: Record<Band, MessageKey> = {
  strong: "co.growthFit.band.strong",
  moderate: "co.growthFit.band.moderate",
  weak: "co.growthFit.band.weak",
  unknown: "co.growthFit.band.unknown",
};

// `unknown` is deliberately absent from the tones. It is an ABSTENTION, not a
// low score, and giving it a colour on the same scale as the other three would
// place it on that scale — which is the single misreading this panel exists to
// prevent. It renders as prose instead.
const BAND_TONES: Partial<Record<Band, "success" | "warn">> = {
  strong: "success",
  weak: "warn",
};

/**
 * GrowthFitPanel answers what this company is worth to US, where the dossier
 * beside it answers what the company IS.
 *
 * The band is never shown alone. A reader who sees "unknown" with nothing
 * beside it cannot tell "we could not judge" from "a poor fit", and those are
 * opposite conclusions — so the completeness figure and the next step are part
 * of the answer rather than a footnote under it.
 *
 * Both completeness counts always render. "4 of 7 inputs" and "4 of 40" are
 * different claims about how much we know, and a bare proportion renders them
 * identically.
 */
export function GrowthFitPanel({
  orgId,
  enabled,
  onOpenRecord,
}: Readonly<{
  orgId: string;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const fit = useQuery({
    queryKey: ["org-growth-fit", orgId],
    enabled,
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/growth-fit", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const reassess = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/organizations/{id}/growth-fit", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) =>
      queryClient.setQueryData(["org-growth-fit", orgId], data),
  });

  // A workspace reading from an incumbent mirror has none of the facts this is
  // assembled from, so the panel is absent rather than empty.
  if (!enabled) {
    return null;
  }

  const written = fit.data;
  // A payload this build cannot read is not a company we know nothing about.
  // Without this check the two would render identically, and a schema skew
  // would look like a data gap the reader could go and close.
  // Every field the panel goes on to read is checked here, not just the band:
  // a half-shaped payload that passes a one-field guard crashes further down,
  // which is a worse answer than the unavailable state this falls back to.
  const readable =
    written &&
    typeof written.band === "string" &&
    typeof written.generated_by === "string" &&
    typeof written.generated_at === "string" &&
    written.data_completeness &&
    typeof written.data_completeness.present === "number" &&
    typeof written.data_completeness.expected === "number"
      ? written
      : undefined;

  return (
    <Panel
      className="co-worth"
      title={t("co.growthFit.title")}
      // The same shape the account brief beside it keeps: the "as of" stamp
      // reads as the header's own fact — when this reading was assembled —
      // while who wrote it and the verb to have it written again sit together
      // in the footer band, which is the panel's sourcing rather than part of
      // what it says.
      titleAction={
        readable && (
          <span className="t-small">
            {t("co.brief.generatedAt", {
              when: formatDateTime(readable.generated_at, locale, RECORD_ZONE),
            })}
          </span>
        )
      }
      footer={
        readable && (
          <div className="co-brief-foot">
            <WrittenBy by={readable.generated_by} />
            <Button
              small
              onClick={() => reassess.mutate()}
              disabled={reassess.isPending}
            >
              {reassess.isPending
                ? t("co.growthFit.reassessing")
                : t("co.growthFit.reassess")}
            </Button>
          </div>
        )
      }
    >
      {fit.isPending ? (
        <PanelBody>
          <Skeleton width="100%" height={64} />
        </PanelBody>
      ) : !readable ? (
        <PanelBody>
          <EmptyState>{t("co.growthFit.unavailable")}</EmptyState>
        </PanelBody>
      ) : (
        <>
          <GrowthFitVerdict fit={readable} />
          <GrowthFitReasons fit={readable} onOpenRecord={onOpenRecord} />
        </>
      )}
      {reassess.error && (
        <PanelBody className="co-worth-block">
          <p className="co-part-error">{problemMessageOf(reassess.error, t)}</p>
        </PanelBody>
      )}
    </Panel>
  );
}

/**
 * GrowthFitVerdict is the band and everything a reader needs to weigh it: how
 * much of what the assessment wanted was actually there, what is still
 * missing, why the band could not go higher, and what to do next.
 */
function GrowthFitVerdict({ fit }: Readonly<{ fit: GrowthFit }>) {
  const t = useT();
  const { present, expected } = fit.data_completeness;
  return (
    <>
      <PanelBody className="co-worth-block">
        <p className="co-growth-fit-band">
          <Badge tone={BAND_TONES[fit.band]}>{t(BAND_LABELS[fit.band])}</Badge>{" "}
          {/* Both counts, always. A proportion without its denominator is not a
              completeness figure. */}
          <span className="co-growth-fit-completeness">
            {t("co.growthFit.completeness", { present, expected })}
          </span>
        </p>
        {/* The cap and the next step qualify the BAND, so they stay in its
            block rather than becoming rows of their own further down. */}
        {fit.band_capped_reason && (
          <p className="co-growth-fit-capped">
            {t("co.growthFit.capped", { reason: fit.band_capped_reason })}
          </p>
        )}
        {fit.next_step && (
          <p className="co-growth-fit-next">
            {t("co.growthFit.nextStep", { step: fit.next_step })}
          </p>
        )}
      </PanelBody>
      {/* The band, taken apart (DOSS-AC-17). Four named dimensions over the
          same evidence the band was read from, so a reader who disagrees with
          the verdict can see which input carried it.

          Absent below the abstention floor, with the claims that justified a
          judgment the assembly declined to make — never drawn as zeroes, which
          would be a claim about the company rather than about the reading. */}
      {fit.sub_scores && fit.sub_scores.length > 0 && (
        <PanelBody className="co-worth-block">
          <ul className="co-growth-fit-scores">
            {fit.sub_scores.map((sub) => (
              <li key={sub.dimension} className="co-growth-fit-score">
                {/* The label and the figure are DRAWN, not only announced:
                    Meter carries its label to assistive tech as an aria-label
                    and renders none, so a row of bare bars would say nothing
                    about which dimension is which. */}
                <span className="co-growth-fit-score-head">
                  <span>{t(SUB_SCORE_LABELS[sub.dimension])}</span>
                  <span className="co-growth-fit-score-value">{sub.score}</span>
                </span>
                {/* Flat, like the health meters beside it: the gradient's
                    second colour reads as a warning creeping in at the high
                    end, and a high dimension score is the GOOD end here. */}
                <Meter
                  value={sub.score}
                  max={100}
                  flat
                  label={t(SUB_SCORE_LABELS[sub.dimension])}
                />
                {/* The reason travels WITH the bar. A number and no sentence is
                    the unexplainable score this model replaced. */}
                <span className="co-row-meta co-growth-fit-score-reason">
                  {sub.reason}
                </span>
              </li>
            ))}
          </ul>
        </PanelBody>
      )}
    </>
  );
}

/**
 * GrowthFitReasons renders the claims behind a band, in the one claim
 * vocabulary every generated surface here uses — so a reader learns the
 * fact/assessment/recommendation distinction once and it holds everywhere.
 *
 * A group with nothing in it is ABSENT rather than empty. An empty "what
 * argues against them" heading reads as a finding that nothing does, which is
 * a different claim from having looked and found nothing to say.
 */
function GrowthFitReasons({
  fit,
  onOpenRecord,
}: Readonly<{
  fit: GrowthFit;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  const missing = fit.data_completeness.missing;
  // The two sides of the argument are read AGAINST each other, so they sit in
  // one block as two columns rather than as two stacked sections a reader has
  // to scroll between to compare.
  const forAgainst: ReadonlyArray<{
    key: MessageKey;
    sentences: BriefSentence[] | undefined;
  }> = [
    { key: "co.growthFit.positive", sentences: fit.positive_factors },
    { key: "co.growthFit.negative", sentences: fit.negative_factors },
  ];
  // Everything else is one answer to one question, so it reads as a labelled
  // row: the label in its own column, the answer beside it.
  const rows: ReadonlyArray<{
    key: MessageKey;
    sentences: BriefSentence[] | undefined;
  }> = [
    { key: "co.growthFit.whitespace", sentences: fit.whitespace },
    { key: "co.growthFit.objections", sentences: fit.objections },
    {
      key: "co.growthFit.angle",
      sentences: fit.recommended_angle ? [fit.recommended_angle] : undefined,
    },
  ];
  const sides = forAgainst.filter(
    (group) => group.sentences && group.sentences.length > 0,
  );
  return (
    <>
      {/* Two columns only when there are two sides to read across. One side
          alone in a half-width column is a column of empty space claiming the
          other argument exists and was left blank. */}
      {sides.length > 0 && (
        <PanelBody
          className={
            sides.length > 1
              ? "co-worth-block co-worth-split"
              : "co-worth-block"
          }
        >
          {sides.map((group) => (
            <div key={group.key} className="co-worth-column">
              <Eyebrow as="h3">{t(group.key)}</Eyebrow>
              <SentenceList
                sentences={group.sentences ?? []}
                onOpenRecord={onOpenRecord}
              />
            </div>
          ))}
        </PanelBody>
      )}
      {rows.map(
        (group) =>
          group.sentences &&
          group.sentences.length > 0 && (
            <GrowthFitRow key={group.key} label={group.key}>
              <SentenceList
                sentences={group.sentences}
                onOpenRecord={onOpenRecord}
              />
            </GrowthFitRow>
          ),
      )}
      {missing && missing.length > 0 && (
        <GrowthFitRow label="co.growthFit.missing">
          <p className="co-growth-fit-missing">{missing.join(", ")}</p>
        </GrowthFitRow>
      )}
    </>
  );
}

/** One labelled answer: the label in its own column, the answer beside it. */
function GrowthFitRow({
  label,
  children,
}: Readonly<{ label: MessageKey; children: ReactNode }>) {
  const t = useT();
  return (
    <PanelBody className="co-worth-block co-worth-row">
      <Eyebrow as="h3" className="co-worth-row-label">
        {t(label)}
      </Eyebrow>
      <div>{children}</div>
    </PanelBody>
  );
}
