import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState, Skeleton } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";
import { RECORD_ZONE, SentenceList, WrittenBy } from "./company360";

type Dossier = components["schemas"]["OrganizationDossier"];
type SectionKind = Dossier["sections"][number]["kind"];

// Typed against the contract's own enum, so a section kind added upstream fails
// to compile here rather than rendering as a blank heading.
const SECTION_LABELS: Record<SectionKind, MessageKey> = {
  summary: "co.dossier.section.summary",
  products_services: "co.dossier.section.products_services",
  markets: "co.dossier.section.markets",
  buying_center: "co.dossier.section.buying_center",
  differentiation: "co.dossier.section.differentiation",
  firmographics: "co.dossier.section.firmographics",
};

/**
 * DossierPanel answers what this company IS, from its own recorded facts.
 *
 * It is separate from the account brief on purpose. The brief describes our
 * relationship and ages in hours; the dossier describes the company and ages in
 * weeks. A page mixing "they operate in Germany and Austria" with "the economic
 * buyer has not replied in eighteen days" gives a reader no way to tell which
 * claims are which.
 *
 * Every sentence carries the records it was written from, so the reader can
 * open the evidence rather than take the sentence on trust.
 */
export function DossierPanel({
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
  const dossier = useQuery({
    queryKey: ["org-dossier", orgId],
    enabled,
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/dossier", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const rewrite = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/organizations/{id}/dossier", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => queryClient.setQueryData(["org-dossier", orgId], data),
  });

  // A workspace reading from an incumbent mirror holds none of the facts this
  // is assembled from, so the panel is absent rather than empty.
  if (!enabled) {
    return null;
  }

  const written = dossier.data;
  // A payload this build cannot read is not a company we know nothing about.
  // Without this the two render identically, and a schema skew would look like
  // a company nobody has described.
  // The sections are checked one by one, not just as an array: a section whose
  // kind this build has no label for would render as an unnamed heading, which
  // reads as a defect in the dossier rather than in the payload carrying it.
  const readable =
    written &&
    Array.isArray(written.sections) &&
    typeof written.generated_by === "string" &&
    typeof written.generated_at === "string" &&
    written.sections.every(
      (section) =>
        section?.kind in SECTION_LABELS && Array.isArray(section.sentences),
    )
      ? written
      : undefined;

  return (
    <section className="co-part co-dossier" aria-label={t("co.dossier.title")}>
      <h2 className="co-part-label">{t("co.dossier.title")}</h2>
      {dossier.isPending ? (
        <Skeleton width="100%" height={64} />
      ) : !readable ? (
        <EmptyState>{t("co.dossier.unavailable")}</EmptyState>
      ) : readable.sections.length === 0 ? (
        <EmptyState>{t("co.dossier.empty")}</EmptyState>
      ) : (
        <>
          {/* One reading, not five labelled ones. The mockup draws the dossier
              as continuous prose with its sources underneath; a heading per
              section turned three sentences about one company into a form with
              five fields, and the headings said less than the sentences under
              them. The sections still ORDER the prose — the server decides
              what comes first — they just no longer announce themselves. */}
          {/* ONE list over every section. Per-section lists would each collect
              their own sources, so a one-sentence section still drew a chip
              directly under its line — the wall of "fact" relocated rather
              than removed. The sections still ORDER the prose; the receipts
              gather once, under all of it. */}
          <SentenceList
            sentences={readable.sections.flatMap(
              (section) => section.sentences,
            )}
            onOpenRecord={onOpenRecord}
            citations="collected"
          />
          <p className="co-part-foot">
            <WrittenBy by={readable.generated_by} />{" "}
            {readable.needs_refresh && (
              /* Said out loud BESIDE the content, never instead of it: a stale
                 dossier is more useful than none, and hiding it would leave the
                 reader with nothing rather than with something dated. */
              <Badge tone="warn">{t("co.dossier.stale")}</Badge>
            )}{" "}
            {t("co.brief.generatedAt", {
              when: formatDateTime(readable.generated_at, locale, RECORD_ZONE),
            })}{" "}
            <Button
              variant="ghost"
              small
              onClick={() => rewrite.mutate()}
              disabled={rewrite.isPending}
            >
              {rewrite.isPending
                ? t("co.dossier.rewriting")
                : t("co.dossier.rewrite")}
            </Button>
          </p>
          {rewrite.error && (
            <p className="co-part-error">
              {problemMessageOf(rewrite.error, t)}
            </p>
          )}
        </>
      )}
    </section>
  );
}
