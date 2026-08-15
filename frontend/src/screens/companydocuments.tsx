import { useQuery } from "@tanstack/react-query";
import { Fragment, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState } from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem } from "./common";
import { RECORD_ZONE, SectionPart, type SectionState } from "./company360";
import { DocumentExtractionPanel } from "./documentextraction";

// The account's documents: the contracts, offers and legal files a rep goes
// looking for before a call.
//
// Until now a file was reachable only from whichever record it happened to be
// attached to, with a filename and nothing else — so "the signed contract" on an
// account with forty files was the filename and somebody's memory.
//
// TWO THINGS THIS SURFACE WILL NOT DO.
//
// It does not infer which version is current. `doc_state` is asserted by a human
// or by the source that produced the file; nothing here reads the newest upload
// date or a filename containing "final" as an answer. The most recent upload is
// very often a draft and `final-v3` is a joke everyone has made, so an inference
// would be a confident wrong answer to the exact question the card exists for.
//
// It does not offer a download for a file that cannot be downloaded. Scanning
// and blocked are states with their own words, because a download button that
// fails on click teaches a reader to distrust the ones that work.

type Attachment = components["schemas"]["Attachment"];
type Category = NonNullable<Attachment["category"]>;
type DocState = NonNullable<Attachment["doc_state"]>;

const CATEGORY_LABELS: Record<Category, MessageKey> = {
  contract: "docs.category.contract",
  offer: "docs.category.offer",
  legal: "docs.category.legal",
  email_attachment: "docs.category.email",
  other: "docs.category.other",
};

const STATE_LABELS: Record<DocState, MessageKey> = {
  draft: "docs.state.draft",
  current: "docs.state.current",
  final: "docs.state.final",
  superseded: "docs.state.superseded",
};

// Superseded is the one state that changes how a row should READ: it is history,
// not a candidate. The rest are equal citizens and get no tone.
const STATE_TONE: Partial<Record<DocState, "warn">> = { superseded: "warn" };

// A FILTERED read that found nothing is not an empty account. SectionCard's
// empty state replaces the whole body — filters included — so reporting it here
// would strand the reader on a category with no matches and no control left to
// clear it. Only an unfiltered zero is the account's own emptiness.
function documentsState(
  loading: boolean,
  failed: boolean,
  count: number,
  filtered: boolean,
): SectionState {
  if (loading) {
    return "loading";
  }
  if (failed) {
    return "failed";
  }
  if (count === 0 && !filtered) {
    return "empty";
  }
  return "ready";
}

export function CompanyDocumentsCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const [category, setCategory] = useState<Category | "">("");

  const query = useQuery({
    queryKey: ["orgDocuments", orgId, category],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/documents", {
        params: {
          path: { id: orgId },
          query: category ? { category } : {},
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data?.data ?? [];
    },
  });
  const documents = query.data ?? [];
  // Its own endpoint, so its own state — not a 360 section, and
  // `sections_omitted` has no word for it. A failed read is UNAVAILABLE and
  // an empty one is EMPTY: "this account has no contracts" and "we could not
  // find out" are different sentences and only one is about the account.
  const state = documentsState(
    query.isPending,
    query.isError,
    documents.length,
    category !== "",
  );
  const present = state === "ready" || state === "empty";

  return (
    <Panel title={t("docs.title")}>
      {present && (
        <PanelBody className="docs-filters">
          <Button small onClick={() => setCategory("")}>
            {t("docs.category.all")}
          </Button>
          {(Object.keys(CATEGORY_LABELS) as Category[]).map((key) => (
            <Button
              key={key}
              small
              aria-pressed={category === key}
              onClick={() => setCategory(category === key ? "" : key)}
            >
              {t(CATEGORY_LABELS[key])}
            </Button>
          ))}
        </PanelBody>
      )}
      {present ? (
        documents.length === 0 ? (
          // The filter found nothing, which is different from the account
          // having no documents at all — and only one of those is worth
          // clearing a filter over.
          <PanelBody>
            <EmptyState>
              {t(category ? "docs.noneInCategory" : "docs.empty")}
            </EmptyState>
          </PanelBody>
        ) : (
          documents.map((doc) => (
            <Fragment key={doc.id}>
              <PanelRow className="docs-row">
                {doc.pinned && <Badge tone="accent">{t("docs.pinned")}</Badge>}
                {/* The title if somebody gave it one, else the filename. A
                  display name is what a reader looks for; the filename is
                  what arrived. */}
                <span className="docs-name">{doc.title || doc.filename}</span>
                {doc.category && (
                  <Badge>{t(CATEGORY_LABELS[doc.category])}</Badge>
                )}
                {doc.doc_state && (
                  <Badge tone={STATE_TONE[doc.doc_state]}>
                    {t(STATE_LABELS[doc.doc_state])}
                  </Badge>
                )}
                <span className="t-caption">
                  {formatDateTime(doc.created_at, locale, RECORD_ZONE)}
                </span>
                <DownloadState doc={doc} />
              </PanelRow>
              {/* The staged reading sits UNDER its own row rather than inside it:
                what it offers is about the document above it, and a panel wedged
                into a list row would push the filename and the download out of
                line for every file that has never been read. Only a deal-scoped
                file gets one, because a deal is the only record the accept can
                write to — offering it on a person's CV would be offering an act
                that can only be refused. */}
              {doc.entity_type === "deal" && (
                <PanelRow className="docs-row">
                  <DocumentExtractionPanel attachmentId={doc.id} canAccept />
                </PanelRow>
              )}
            </Fragment>
          ))
        )
      ) : (
        <PanelBody>
          <SectionPart
            state={state}
            emptyLabel={t("docs.empty")}
            detail={
              state === "failed" ? { onRetry: () => void query.refetch() } : {}
            }
          >
            {null}
          </SectionPart>
        </PanelBody>
      )}
    </Panel>
  );
}

// Whether the bytes can be reached, said in words.
//
// The scan gates the byte stream, not the row, so a file that is still scanning
// or was blocked is LISTED — hiding it would be a claim that it does not exist —
// but it is not offered as a download it cannot serve.
function DownloadState({ doc }: Readonly<{ doc: Attachment }>) {
  const t = useT();
  if (doc.scan_status === "scanning") {
    return <span className="t-caption">{t("docs.scanning")}</span>;
  }
  if (doc.scan_status === "blocked") {
    return <span className="t-caption">{t("docs.blocked")}</span>;
  }
  return (
    <a
      className="link-button"
      href={`/v1/attachments/${doc.id}`}
      download={doc.filename}
    >
      {t("docs.download")}
    </a>
  );
}
