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

// What the footer says about the history it is holding — written out per case
// rather than assembled from fragments, because the message layer does not
// speak ICU and a key built by concatenation is a key no search for it finds.
function supersededLine(shown: boolean, count: number): MessageKey {
  if (shown) {
    return count === 1
      ? "docs.superseded.shownOne"
      : "docs.superseded.shownMany";
  }
  return count === 1
    ? "docs.superseded.hiddenOne"
    : "docs.superseded.hiddenMany";
}

export function CompanyDocumentsCard({ orgId }: Readonly<{ orgId: string }>) {
  const canWriteDeals = useCanWriteDeals();
  const t = useT();
  const [category, setCategory] = useState<Category | "">("");
  // History is off by default. Three uploads of one agreement's terms are one
  // document to a rep, and listing every replaced version beside the live one
  // is how a library of forty files reads as a library of ninety.
  const [showSuperseded, setShowSuperseded] = useState(false);

  // ONE read, unfiltered, and the category chosen here. The endpoint filters by
  // category, but a filtered read can only report what it returned — the chips
  // would carry no counts, and "no documents of that kind" would be a claim
  // made from a request that never asked about the other kinds. The account's
  // library is a page of rows, not a feed.
  const query = useQuery({
    queryKey: ["orgDocuments", orgId],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/documents", {
        params: { path: { id: orgId } },
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
  const live = showSuperseded
    ? unfiled
    : unfiled.filter((doc) => doc.doc_state !== "superseded");
  const documents = category
    ? live.filter((doc) => doc.category === category)
    : live;

  // Its own endpoint, so its own state — not a 360 section, and
  // `sections_omitted` has no word for it. A failed read is UNAVAILABLE and
  // an empty one is EMPTY: "this account has no contracts" and "we could not
  // find out" are different sentences and only one is about the account.
  const state = documentsState(query.isPending, query.isError, returned.length);
  const present = state === "ready" || state === "empty";

  return (
    <Panel
      title={t("docs.title")}
      // The history that is being held back, said in the footer where the
      // control that holds it back also lives. A count with no way to act on it
      // is a puzzle; a control with no count is a guess.
      footer={
        present && superseded.length > 0 ? (
          <>
            <span>
              {t(supersededLine(showSuperseded, superseded.length), {
                count: String(superseded.length),
              })}
            </span>
            <button
              type="button"
              className="co-rowlink rec-foot-action"
              aria-pressed={showSuperseded}
              onClick={() => setShowSuperseded(!showSuperseded)}
            >
              {t(
                showSuperseded
                  ? "docs.superseded.hide"
                  : "docs.superseded.show",
              )}
            </button>
          </>
        ) : undefined
      }
    >
      {present && (
        <PanelBody className="docs-filters">
          <FilterChip
            label={t("docs.category.all")}
            count={live.length}
            pressed={category === ""}
            onPress={() => setCategory("")}
          />
          {FILTER_CATEGORIES.map((key) => (
            <FilterChip
              key={key}
              label={t(CATEGORY_LABELS[key])}
              count={live.filter((doc) => doc.category === key).length}
              pressed={category === key}
              onPress={() => setCategory(category === key ? "" : key)}
            />
          ))}
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

// One category, with how many documents are behind it. The count is what makes
// the chip a decision rather than a gamble: a reader presses "Legal" knowing
// whether there is one file there or none, and a kind with nothing in it says
// so instead of leading to an empty list.
function FilterChip({
  label,
  count,
  pressed,
  onPress,
}: Readonly<{
  label: string;
  count: number;
  pressed: boolean;
  onPress: () => void;
}>) {
  return (
    <Button small aria-pressed={pressed} onClick={onPress}>
      {label}
      <span className="rec-chip-count">{count}</span>
    </Button>
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
      <PanelRow className="rec-row">
        <span className="rec-main">
          {/* No pinned badge. `pinned` is a real field — the endpoint sorts on
              it and can filter by it — but nothing in this product SETS it, so
              the badge could only ever appear for a document pinned through the
              API by hand. A state a reader can see and cannot reach reads as a
              feature that is broken rather than one that is absent. It comes
              back with the control that pins. */}
          {/* The NAME is the download. A reader who wants a document clicks its
              title — a separate action word at the far end of the row is a
              second thing to find for the only thing this row does. The title
              if somebody gave it one, else the filename: a display name is what
              a reader looks for; the filename is what arrived, and it is what
              the saved file is called. */}
          <a
            className="co-rowlink rec-title"
            href={`/v1/attachments/${doc.id}`}
            download={doc.filename}
          >
            {doc.title || doc.filename}
          </a>
          {/* What qualifies the file, on one quiet line under its name: the
              filename it will save as when a human gave it a different title,
              where it came from, and when it arrived. The filename is absent
              when the two names are the same string, because a row that says
              one thing twice says it once. */}
          <span className="rec-meta">
            {doc.title && doc.title !== doc.filename && (
              <span>{doc.filename}</span>
            )}
            <span>{doc.source}</span>
            <span>{formatDateTime(doc.created_at, locale, RECORD_ZONE)}</span>
          </span>
        </span>
        <span className="rec-end">
          {doc.category && <Badge>{t(CATEGORY_LABELS[doc.category])}</Badge>}
          {doc.doc_state && (
            <Badge tone={STATE_TONE[doc.doc_state]}>
              {t(STATE_LABELS[doc.doc_state])}
            </Badge>
          )}
          {offersReading && (
            <Button
              small
              aria-expanded={reading}
              onClick={() => setReading(!reading)}
            >
              {t(reading ? "docs.reading.hide" : "docs.reading.show")}
            </Button>
          )}
        </span>
      </PanelRow>
      {/* The staged reading sits UNDER its own row rather than inside it: what
          it offers is about the document above it, and a panel wedged into a
          list row would push the filename and the download out of line. */}
      {offersReading && reading && (
        <PanelRow className="rec-reading">
          <DocumentExtractionPanel
            attachmentId={doc.id}
            canAccept={canWriteDeals}
          />
        </PanelRow>
      )}
    </Fragment>
  );
}
