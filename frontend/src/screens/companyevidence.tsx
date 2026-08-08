import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, EmptyState, Modal, Skeleton } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem } from "./common";
import { RECORD_ZONE } from "./company360";

type Receipt = components["schemas"]["ClaimEvidence"];
type SourceKind = Receipt["source_kind"];

// Typed against the contract's own enum, so a provenance kind added upstream
// fails to compile rather than rendering as a blank label.
const KIND_LABELS: Record<SourceKind, MessageKey> = {
  site_read: "co.evidence.kind.site_read",
  connector: "co.evidence.kind.connector",
  human: "co.evidence.kind.human",
  migration: "co.evidence.kind.migration",
  rule: "co.evidence.kind.rule",
};

/** The record a citation chip names. */
export type CitedRecord = { entityType: string; entityId: string };

/**
 * EvidenceModal is the receipt behind one cited record: where the value came
 * from, when it was read, whether a person has confirmed it — and what could
 * not be filled in.
 *
 * The gaps are shown, not hidden. A claim the reader was told is checkable,
 * with no source to check it against, is worth knowing about; rendering that
 * as a blank field would let it pass for a receipt that simply had nothing to
 * add.
 */
export function EvidenceModal({
  orgId,
  cited,
  onClose,
}: Readonly<{
  orgId: string;
  cited: CitedRecord;
  onClose: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const receipt = useQuery({
    queryKey: ["claim-evidence", orgId, cited.entityType, cited.entityId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/organizations/{id}/evidence/{entityType}/{entityId}",
        {
          params: {
            path: {
              id: orgId,
              entityType: cited.entityType as
                | "organization"
                | "fact"
                | "profile_field",
              entityId: cited.entityId,
            },
          },
        },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const shown = receipt.data;
  return (
    <Modal open onClose={onClose} labelledBy="co-evidence-title">
      <h2 id="co-evidence-title">{t("co.evidence.title")}</h2>
      {receipt.isPending ? (
        <Skeleton width="100%" height={120} />
      ) : !shown?.source_kind ? (
        <EmptyState>{t("co.evidence.unavailable")}</EmptyState>
      ) : (
        <div className="co-evidence">
          <p className="co-evidence-value">
            {shown.label ? `${shown.label}: ` : ""}
            {shown.value}
          </p>
          <p className="co-evidence-origin">
            <Badge>{t(KIND_LABELS[shown.source_kind])}</Badge>{" "}
            {t("co.evidence.producedBy", { who: shown.produced_by })}
          </p>
          {shown.excerpt && (
            /* The verbatim span, quoted rather than paraphrased — the reader is
               comparing it against the source, so any rewording defeats it. */
            <blockquote className="co-evidence-excerpt">
              {shown.excerpt}
            </blockquote>
          )}
          <EvidenceIdentity identity={shown.identity} />
          <EvidenceTimes
            retrievedAt={shown.retrieved_at}
            lastVerifiedAt={shown.last_verified_at}
            locale={locale}
          />
          {typeof shown.confidence === "number" && (
            <p className="co-evidence-line">
              {t("co.evidence.confidence", {
                percent: Math.round(shown.confidence * 100),
              })}
            </p>
          )}
          {shown.gaps && shown.gaps.length > 0 && (
            <p className="co-evidence-gaps">
              {t("co.evidence.gaps", { fields: shown.gaps.join(", ") })}
            </p>
          )}
        </div>
      )}
    </Modal>
  );
}

/** The identifying fields this provenance kind owes, rendered as given. */
function EvidenceIdentity({
  identity,
}: Readonly<{ identity?: Record<string, unknown> }>) {
  if (!identity) {
    return null;
  }
  const entries = Object.entries(identity).filter(
    ([, value]) => typeof value === "string" && value !== "",
  );
  if (entries.length === 0) {
    return null;
  }
  return (
    <dl className="co-evidence-identity">
      {entries.map(([name, value]) => (
        <div key={name}>
          <dt>{name.replace(/_/g, " ")}</dt>
          <dd>
            {name === "source_url" ? (
              <a href={String(value)} target="_blank" rel="noreferrer">
                {String(value)}
              </a>
            ) : (
              String(value)
            )}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * Read and confirmed are shown as two lines, never merged. They are different
 * assurances: one says a machine fetched this and it still said so, the other
 * says a person looked at it and agreed.
 */
function EvidenceTimes({
  retrievedAt,
  lastVerifiedAt,
  locale,
}: Readonly<{
  retrievedAt?: string | null;
  lastVerifiedAt?: string | null;
  locale: "en" | "de" | "vi";
}>) {
  const t = useT();
  return (
    <>
      {retrievedAt && (
        <p className="co-evidence-line">
          {t("co.evidence.retrievedAt", {
            when: formatDateTime(retrievedAt, locale, RECORD_ZONE),
          })}
        </p>
      )}
      {lastVerifiedAt && (
        <p className="co-evidence-line">
          {t("co.evidence.verifiedAt", {
            when: formatDateTime(lastVerifiedAt, locale, RECORD_ZONE),
          })}
        </p>
      )}
    </>
  );
}
