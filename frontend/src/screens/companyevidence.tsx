import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  Modal,
  Skeleton,
} from "../design-system/atoms";
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

/**
 * The record a citation chip names.
 *
 * `entityType` is the contract's own union rather than a bare string, so the
 * page that routes a chip here narrows it once — at the routing decision, where
 * the knowledge lives — instead of asserting the type at the fetch.
 */
export type CitedRecord = {
  entityType: Receipt["entity_type"];
  entityId: string;
};

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
  onStep,
}: Readonly<{
  orgId: string;
  cited: CitedRecord;
  onClose: () => void;
  // Move to the neighbouring claim in the list that opened this. The ORDER is
  // the citing card's, not the drawer's: the drawer sees one receipt at a
  // time and has no idea what comes next. A card that offers no ordering gets
  // no arrows rather than arrows that guess.
  onStep?: (direction: -1 | 1) => void;
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
              entityType: cited.entityType,
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
    // A right-anchored DRAWER (mockup State C): the claim's own sentence stays
    // on screen behind it, which is the thing the reader is checking the
    // receipt against. A centred box covers it.
    <Modal
      open
      onClose={onClose}
      labelledBy="co-evidence-title"
      placement="right"
    >
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
            <ExtractedMark receipt={shown} />{" "}
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
      <EvidenceSteps onStep={onStep} />
    </Modal>
  );
}

// "AI extracted · not yet confirmed", derived rather than stored: a MODEL read
// it out of something, and no person has verified it since. The predicate is
// stated here rather than left implicit, because the badge makes a claim about
// a claim and a reader deserves to know what earned it.
//
// `site_read` alone. The other four are not model extractions and the badge
// would be false of each: a person typed a `human` value, an older system
// holds a `migration` one, a `connector` value came out of an API verbatim,
// and a `rule` value was computed by code somebody wrote. Calling any of them
// AI-extracted is exactly the mislabelling this badge exists to prevent.
function ExtractedMark({ receipt }: Readonly<{ receipt: Receipt }>) {
  const t = useT();
  if (receipt.source_kind !== "site_read" || receipt.last_verified_at) {
    return null;
  }
  return <Badge tone="ai">{t("co.evidence.extractedUnconfirmed")}</Badge>;
}

// Previous / next through the list the citing card owns.
//
// Rendered only when that card offered an ordering. A drawer that drew arrows
// for a card with no order would step through nothing, and one that guessed an
// order would step somewhere the reader cannot predict.
function EvidenceSteps({
  onStep,
}: Readonly<{ onStep?: (direction: -1 | 1) => void }>) {
  const t = useT();
  if (!onStep) {
    return null;
  }
  return (
    <div className="co-evidence-steps">
      <Button small onClick={() => onStep(-1)}>
        {t("co.evidence.previous")}
      </Button>
      <Button small onClick={() => onStep(1)}>
        {t("co.evidence.next")}
      </Button>
    </div>
  );
}

/**
 * isFollowable reports whether a recorded URL may become a link.
 *
 * `source_url` is a plain text column written by the site-read pipeline from
 * pages we crawled, and nothing on the write path constrains its scheme. React
 * does not sanitize `href`, so a `javascript:` value stored there would run on
 * click — a scraped page choosing what our UI executes.
 *
 * Anything that is not http or https still RENDERS, as text. The reader is
 * being shown where a claim came from, and hiding an odd-looking source would
 * withhold exactly the thing they opened this to see.
 */
function isFollowable(url: string): boolean {
  try {
    const scheme = new URL(url).protocol;
    return scheme === "http:" || scheme === "https:";
  } catch {
    // Not a URL this browser can parse is not one it should be asked to open.
    return false;
  }
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
            {name === "source_url" && isFollowable(String(value)) ? (
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
