import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState, Skeleton } from "../design-system/atoms";
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
    <section
      className="co-part co-growth-fit"
      aria-label={t("co.growthFit.title")}
    >
      <h2 className="co-part-label">{t("co.growthFit.title")}</h2>
      {fit.isPending ? (
        <Skeleton width="100%" height={64} />
      ) : !readable ? (
        <EmptyState>{t("co.growthFit.unavailable")}</EmptyState>
      ) : (
        <>
          <GrowthFitVerdict fit={readable} />
          <GrowthFitReasons fit={readable} onOpenRecord={onOpenRecord} />
          <p className="co-part-foot">
            <WrittenBy by={readable.generated_by} />{" "}
            {t("co.brief.generatedAt", {
              when: formatDateTime(readable.generated_at, locale, RECORD_ZONE),
            })}{" "}
            <Button
              variant="ghost"
              small
              onClick={() => reassess.mutate()}
              disabled={reassess.isPending}
            >
              {reassess.isPending
                ? t("co.growthFit.reassessing")
                : t("co.growthFit.reassess")}
            </Button>
          </p>
          {reassess.error && (
            <p className="co-part-error">
              {problemMessageOf(reassess.error, t)}
            </p>
          )}
        </>
      )}
    </section>
  );
}

/**
 * GrowthFitVerdict is the band and everything a reader needs to weigh it: how
 * much of what the assessment wanted was actually there, what is still
 * missing, why the band could not go higher, and what to do next.
 */
function GrowthFitVerdict({ fit }: Readonly<{ fit: GrowthFit }>) {
  const t = useT();
  const { present, expected, missing } = fit.data_completeness;
  return (
    <div className="co-growth-fit-verdict">
      <p className="co-growth-fit-band">
        <Badge tone={BAND_TONES[fit.band]}>{t(BAND_LABELS[fit.band])}</Badge>{" "}
        {/* Both counts, always. A proportion without its denominator is not a
            completeness figure. */}
        <span className="co-growth-fit-completeness">
          {t("co.growthFit.completeness", { present, expected })}
        </span>
      </p>
      {missing && missing.length > 0 && (
        <p className="co-growth-fit-missing">
          {t("co.growthFit.missing", { inputs: missing.join(", ") })}
        </p>
      )}
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
    </div>
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
  const groups: ReadonlyArray<{
    key: MessageKey;
    sentences: BriefSentence[] | undefined;
  }> = [
    { key: "co.growthFit.positive", sentences: fit.positive_factors },
    { key: "co.growthFit.negative", sentences: fit.negative_factors },
    { key: "co.growthFit.whitespace", sentences: fit.whitespace },
    { key: "co.growthFit.objections", sentences: fit.objections },
    {
      key: "co.growthFit.angle",
      sentences: fit.recommended_angle ? [fit.recommended_angle] : undefined,
    },
  ];
  return (
    <>
      {groups.map(
        (group) =>
          group.sentences &&
          group.sentences.length > 0 && (
            <GrowthFitGroup
              key={group.key}
              label={group.key}
              sentences={group.sentences}
              onOpenRecord={onOpenRecord}
            />
          ),
      )}
    </>
  );
}

function GrowthFitGroup({
  label,
  sentences,
  onOpenRecord,
}: Readonly<{
  label: MessageKey;
  sentences: BriefSentence[];
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  return (
    <div className="co-growth-fit-group">
      <h3 className="co-part-sublabel">{t(label)}</h3>
      <SentenceList sentences={sentences} onOpenRecord={onOpenRecord} />
    </div>
  );
}
