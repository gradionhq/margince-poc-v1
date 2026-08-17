import { useQuery } from "@tanstack/react-query";
import { Fragment, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState } from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem, useMe } from "./common";
import { RECORD_ZONE } from "./company360";
import { DocumentExtractionPanel } from "./documentextraction";

// The account's documents: the offers, legal files and loose paper a rep goes
// looking for before a call.
//
// Until now a file was reachable only from whichever record it happened to be
// attached to, with a filename and nothing else — so "the signed contract" on an
// account with forty files was the filename and somebody's memory.
//
// WHAT THIS SURFACE WILL NOT DO.
//
// It does not infer which version is current. `doc_state` is asserted by a human
// or by the source that produced the file; nothing here reads the newest upload
// date or a filename containing "final" as an answer. The most recent upload is
// very often a draft and `final-v3` is a joke everyone has made, so an inference
// would be a confident wrong answer to the exact question the card exists for.
//
// It also does not list a file TWICE on one tab. A document filed against an
// agreement renders on that agreement's row in the contracts card above, where
// its commercial meaning is; repeating it here made one signed PDF read as two
// documents, which is the one thing a library must never do.

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

// The chips a reader can press. `contract` is absent because the agreements
// card above is where contract paper is read — a chip that filtered this list
// down to the files it deliberately excludes would be a control whose every
// press looks like a bug. The badge vocabulary above keeps the word, since a
// file may still carry the category without being filed to an agreement.
const FILTER_CATEGORIES: readonly Category[] = [
  "offer",
  "legal",
  "email_attachment",
  "other",
];

const STATE_LABELS: Record<DocState, MessageKey> = {
  draft: "docs.state.draft",
  current: "docs.state.current",
  final: "docs.state.final",
  superseded: "docs.state.superseded",
};

// Superseded is the one state that changes how a row should READ: it is history,
// not a candidate. The rest are equal citizens and get no tone.
const STATE_TONE: Partial<Record<DocState, "warn">> = { superseded: "warn" };

// Whether this reader may write what a document says onto a deal.
//
// Read from /me's own effective grants rather than assumed: reading a document
// and writing what it says are different authorities, and a panel that offered
// Accept to a seat holding only the first would hand out a button whose every
// press is a 403. The grant is ABSENT from the map when it was never given —
// the generated index signature cannot say so — which is why it is widened and
// read fail-closed.
function useCanWriteDeals(): boolean {
  const me = useMe();
  const objects: Readonly<Record<string, { update?: boolean } | undefined>> =
    me.data?.authorization?.objects ?? {};
  return objects.deal?.update === true;
}

// A FILTERED read that found nothing is not an empty account. SectionCard's
// empty state replaces the whole body — filters included — so reporting it here
// would strand the reader on a category with no matches and no control left to
// clear it. Only a read that returned nothing AT ALL is the account's own
// emptiness; everything this card itself withholds is reported in the body,
// where the control that withheld it is still on screen.
function documentsState(
  loading: boolean,
  failed: boolean,
  returned: number,
): SectionState {
  if (loading) {
    return "loading";
  }
  if (failed) {
    return "failed";
  }
  if (returned === 0) {
    return "empty";
  }
  return "ready";
}

// Why this list is empty when the account is not, said in the reader's own
// terms — each answer names the thing that is holding the rows back, because
// "no documents" in front of a pressed filter is a lie about the account.
function emptyReason(
  category: Category | "",
  onAgreements: number,
  superseded: number,
): MessageKey {
  if (category !== "") {
    return "docs.noneInCategory";
  }
  if (superseded > 0) {
    return "docs.allSuperseded";
  }
  if (onAgreements > 0) {
    return "docs.allOnAgreements";
  }
  return "docs.empty";
}

export function CompanyDocumentsCard({ orgId }: Readonly<{ orgId: string }>) {
  const canWriteDeals = useCanWriteDeals();
  const t = useT();
  const [category, setCategory] = useState<Category | "">("");
  // History is off by default. Three uploads of one agreement's terms are one
  // document to a rep, and listing every replaced version beside the live one
  // is how a library of forty files reads as a library of ninety.
  const [showSuperseded, setShowSuperseded] = useState(false);

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
  const returned = query.data ?? [];
  // Paper filed against an agreement is READ ON THAT AGREEMENT, above. The
  // endpoint has no "unfiled only" question to ask, so the split is made here
  // from `contract_id`, which the upload files and nothing infers.
  const unfiled = returned.filter((doc) => !doc.contract_id);
  const superseded = unfiled.filter((doc) => doc.doc_state === "superseded");
  const documents = showSuperseded
    ? unfiled
    : unfiled.filter((doc) => doc.doc_state !== "superseded");

  // Its own endpoint, so its own state — not a 360 section, and
  // `sections_omitted` has no word for it. A failed read is UNAVAILABLE and
  // an empty one is EMPTY: "this account has no contracts" and "we could not
  // find out" are different sentences and only one is about the account.
  const state = documentsState(query.isPending, query.isError, returned.length);
  const present = state === "ready" || state === "empty";

  return (
    <Panel title={t("docs.title")}>
      {present && (
        <PanelBody className="docs-filters">
          <Button
            small
            aria-pressed={category === ""}
            onClick={() => setCategory("")}
          >
            {t("docs.category.all")}
          </Button>
          {FILTER_CATEGORIES.map((key) => (
            <Button
              key={key}
              small
              aria-pressed={category === key}
              onClick={() => setCategory(category === key ? "" : key)}
            >
              {t(CATEGORY_LABELS[key])}
            </Button>
          ))}
          {/* The toggle shows only when there IS history to show. A control
              that can never change anything teaches a reader to ignore the
              row it sits in. */}
          {superseded.length > 0 && (
            <Button
              small
              aria-pressed={showSuperseded}
              onClick={() => setShowSuperseded(!showSuperseded)}
            >
              {t("docs.superseded.show", { count: String(superseded.length) })}
            </Button>
          )}
        </PanelBody>
      )}
      {present ? (
        documents.length === 0 ? (
          <PanelBody>
            <EmptyState>
              {t(
                emptyReason(
                  category,
                  returned.length - unfiled.length,
                  superseded.length,
                ),
              )}
            </EmptyState>
          </PanelBody>
        ) : (
          documents.map((doc) => (
            <DocumentRow key={doc.id} doc={doc} canWriteDeals={canWriteDeals} />
          ))
        )
      ) : (
        <PanelBody>
          <SurfaceState
            state={state}
            emptyLabel={t("docs.empty")}
            detail={
              state === "failed" ? { onRetry: () => void query.refetch() } : {}
            }
          >
            {null}
          </SurfaceState>
        </PanelBody>
      )}
    </Panel>
  );
}

function DocumentRow({
  doc,
  canWriteDeals,
}: Readonly<{ doc: Attachment; canWriteDeals: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  // The staged reading is OPENED, never mounted by the list. Each panel asks
  // the server for its own document's reading on mount, so a list that opened
  // them all fired one request per deal file and stacked a wall of panels over
  // the filenames the reader came for.
  const [reading, setReading] = useState(false);
  // Only a deal-scoped file is offered one, because a deal is the only record
  // the accept can write to — offering it on a person's CV would be offering
  // an act that can only be refused.
  const offersReading = doc.entity_type === "deal";

  return (
    <Fragment>
      <PanelRow className="docs-row">
        {doc.pinned && <Badge tone="accent">{t("docs.pinned")}</Badge>}
        {/* The NAME is the download. A reader who wants a document clicks its
            title — a separate action word at the far end of the row is a second
            thing to find for the only thing this row does. The title if
            somebody gave it one, else the filename: a display name is what a
            reader looks for; the filename is what arrived, and it is what the
            saved file is called. */}
        <a
          className="docs-name co-rowlink"
          href={`/v1/attachments/${doc.id}`}
          download={doc.filename}
        >
          {doc.title || doc.filename}
        </a>
        {doc.category && <Badge>{t(CATEGORY_LABELS[doc.category])}</Badge>}
        {doc.doc_state && (
          <Badge tone={STATE_TONE[doc.doc_state]}>
            {t(STATE_LABELS[doc.doc_state])}
          </Badge>
        )}
        <span className="t-caption">
          {formatDateTime(doc.created_at, locale, RECORD_ZONE)}
        </span>
        {offersReading && (
          <Button
            small
            aria-expanded={reading}
            onClick={() => setReading(!reading)}
          >
            {t(reading ? "docs.reading.hide" : "docs.reading.show")}
          </Button>
        )}
      </PanelRow>
      {/* The staged reading sits UNDER its own row rather than inside it: what
          it offers is about the document above it, and a panel wedged into a
          list row would push the filename and the download out of line. */}
      {offersReading && reading && (
        <PanelRow className="docs-row">
          <DocumentExtractionPanel
            attachmentId={doc.id}
            canAccept={canWriteDeals}
          />
        </PanelRow>
      )}
    </Fragment>
  );
}
