// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import {
  type QueryKey,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { api, FIRST_PAGE } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { formatUploadLimit, useMaxUploadBytes } from "../app/uploadlimit";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ChoiceList } from "../design-system/choicelist";
import { FileDropzone } from "../design-system/filedropzone";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";

// Adding a document to the record the dialog was opened from — an account's
// document library, or a contact's.
//
// WHY THE PARENT IS A QUESTION AND NOT A DEFAULT. A document filed against the
// company is a document about the company; one filed against a deal is evidence
// in that deal, and it is the only kind the extraction panel will offer to read
// for deal fields, because a deal is the only record the accept can write to.
// Filing everything against the company would be the tidier form and would
// quietly make that feature unreachable, which is the state this screen was in
// before: the upload existed, hardcoded to the organization, and the reading it
// fed had no way to happen.
//
// WHY IT TAKES TWO REQUESTS. The upload endpoint carries the bytes and the
// parent and nothing else — category and title live behind
// `PATCH /attachments/{id}/metadata`. So the second call can fail on its own
// with the file already stored, and this dialog says exactly that rather than
// reporting a failure the reader would answer by uploading the same file twice.
//
// WHY THE QUESTION IS ONLY ASKED ON AN ACCOUNT. Deals hang off a company, so
// the account's library can offer its own deals as filing targets and a
// contact's cannot: nothing on a contact's page names a deal, and a contact is
// seated on deals at companies they may not even work for. The contact's
// library therefore files against the contact, with no choice to make, rather
// than growing a control whose one option is the record you are already on.

type Attachment = components["schemas"]["Attachment"];
type Category = NonNullable<Attachment["category"]>;
type DealPage = components["schemas"]["DealListResponse"];

const CATEGORY_KEYS: Record<Category, MessageKey> = {
  contract: "docs.category.contract",
  offer: "docs.category.offer",
  legal: "docs.category.legal",
  email_attachment: "docs.category.email",
  message_attachment: "docs.category.message",
  other: "docs.category.other",
};

// WHAT A HUMAN MAY CHOOSE, which is not the whole vocabulary. The two
// `*_attachment` values record PROVENANCE — capture derives them from the
// transport a file arrived on — and a file being uploaded from this dialog
// arrived on none. Offering them would let the picker mint a claim about where a
// file came from that is false the moment it is chosen, and the document library
// reads that column as an answer to exactly that question.
//
// Derived by subtraction from CATEGORY_KEYS rather than listed, so a value added
// to the contract lands in the picker by default and only a deliberate edit here
// keeps it out. The alternative — a second hand-kept list — is how the two come
// to disagree about a category nobody remembers adding.
const CAPTURED_ONLY: readonly Category[] = [
  "email_attachment",
  "message_attachment",
];

const UPLOADABLE_CATEGORIES = (Object.keys(CATEGORY_KEYS) as Category[]).filter(
  (key) => !CAPTURED_ONLY.includes(key),
);

/**
 * The record whose document library this dialog was opened from.
 *
 * `record` is also the wire's own `entity_type`, which is why it is one string
 * rather than two shapes: the upload takes the parent as an (entity_type,
 * entity_id) pair, and the RBAC object the server checks IS that same string
 * (`auth.Require(ctx, entityType, update)`), so a second vocabulary here would
 * be a mapping table with nothing to map.
 */
export type DocumentAnchor = Readonly<{
  record: "organization" | "person" | "deal";
  id: string;
}>;

type Parent = Readonly<{
  entityType: DocumentAnchor["record"];
  entityId: string;
}>;

// Which record the file hangs off: the one this dialog was opened from, or one
// of that account's deals. Two named answers rather than a Select carrying a
// sentinel value beside a list of deal ids — filing against the account is a
// different KIND of decision from picking one deal out of hundreds, and the two
// spent a release smuggled into one dropdown where the account read as the
// zeroth deal.
type Filing = "anchor" | "deal";

/**
 * The parent the bytes will be filed against, or null when the reader has
 * chosen "a deal" and not yet picked one — which is a refusal to state, not a
 * parent to guess.
 */
function parentOf(
  anchor: DocumentAnchor,
  filing: Filing,
  deal: RecordPickerCandidate | null,
): Parent | null {
  if (filing === "deal") {
    return deal ? { entityType: "deal", entityId: deal.id } : null;
  }
  return { entityType: anchor.record, entityId: anchor.id };
}

type Submission = {
  parent: Parent;
  category: Category;
  title: string;
  file: File;
};

// The bytes go up as multipart because the endpoint takes a file part and the
// generated client only serializes JSON. Everything else about the request —
// the cookie, the problem-document shape of a refusal — is unchanged.
//
// Returns the stored document's id, or undefined when the server accepted the
// bytes and the response body could not be read. Those are different facts and
// the caller needs both: the second one is a stored document whose id we do not
// know, so its metadata cannot be written — but reporting it as a failed upload
// would be a lie about a file that is on the record.
async function uploadFile(submitted: Submission): Promise<string | undefined> {
  const body = new FormData();
  body.append("entity_type", submitted.parent.entityType);
  body.append("entity_id", submitted.parent.entityId);
  body.append("file", submitted.file);
  const response = await fetch("/v1/attachments", {
    method: "POST",
    body,
    credentials: "include",
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => undefined);
    throwProblem(payload);
  }
  // Not thrown. Past the status check the bytes are stored, and nothing a
  // failed parse tells us changes that.
  const stored: Attachment | undefined = await response
    .json()
    .catch(() => undefined);
  return stored?.id;
}

// Only what the reader actually chose is sent. A PATCH that also wrote the
// defaults back would overwrite a category the server may have derived for
// itself, and would put this dialog's assumptions into a record it did not read.
function metadataFor(submitted: Submission) {
  const title = submitted.title.trim();
  const patch: { category?: Category; title?: string } = {};
  if (submitted.category !== "other") {
    patch.category = submitted.category;
  }
  if (title !== "") {
    patch.title = title;
  }
  return patch;
}

// HOW THE DEAL SEARCH WORKS, AND WHERE IT STOPS.
//
// `GET /deals` is cursor-paginated and takes no text query — the contract
// offers a cursor, a limit, a sort and a set of id filters, and nothing
// textual. So the words the reader types are matched HERE, over pages this
// dialog walks, and a client-side match has to stop somewhere or one settled
// keystroke walks every deal an old account ever had.
//
// The bound is pages, not results: DEAL_SEARCH_PAGES pages of the contract's
// maximum page size, in the list endpoint's own default order, which is
// newest-created first. What the search therefore covers is this account's
// DEAL_SEARCH_REACH newest deals, and what it cannot reach is anything older —
// which the picker STATES, under the field, before the reader goes looking. An
// unfound deal and a deal that does not exist read identically otherwise, and
// that silence is the whole of what issue 1536 was about.
const DEAL_PAGE_SIZE = 200;
const DEAL_SEARCH_PAGES = 10;
const DEAL_SEARCH_REACH = DEAL_PAGE_SIZE * DEAL_SEARCH_PAGES;

// How many matches are worth offering at once. Past this the walk stops: a
// list of a hundred pickable buttons is not a pick, and the reader has a
// cheaper way to shorten it, which is one more word.
const DEAL_MATCH_LIMIT = 25;

// How long a walked page is reused. The reader re-runs the whole walk every
// time they change a word, so the pages are cached under their own cursor;
// a minute outlasts a dialog and is far shorter than the age of the deals a
// walk this deep is reaching.
const DEAL_PAGE_FRESH_MS = 60_000;

/**
 * Walk the account's deals, newest first, keeping the ones whose name contains
 * what the reader typed.
 *
 * `fetchPage` is injected rather than called directly so the walk reads pages
 * through the caller's cache: the second search over one account re-reads what
 * the first already fetched instead of spending the whole page budget again.
 */
async function walkAccountDeals(
  fetchPage: (cursor: string | null) => Promise<DealPage>,
  needle: string,
): Promise<RecordPickerCandidate[]> {
  const matches: RecordPickerCandidate[] = [];
  let cursor = FIRST_PAGE;
  for (let page = 0; page < DEAL_SEARCH_PAGES; page += 1) {
    const answered = await fetchPage(cursor);
    for (const deal of answered.data) {
      if (deal.name.toLocaleLowerCase().includes(needle)) {
        matches.push({ id: deal.id, name: deal.name });
      }
    }
    if (matches.length >= DEAL_MATCH_LIMIT) {
      return matches.slice(0, DEAL_MATCH_LIMIT);
    }
    // The CURSOR is what the walk can continue with, and `has_more` without one
    // is a cut list nothing can read the rest of — so both ends of the walk end
    // it here rather than looping on a cursor that will not move.
    cursor = answered.page.next_cursor ?? null;
    if (!cursor) {
      return matches;
    }
  }
  return matches;
}

export function AddDocumentDialog({
  anchor,
  open,
  onClose,
}: Readonly<{
  anchor: DocumentAnchor;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  const queryClient = useQueryClient();

  const [filing, setFiling] = useState<Filing>("anchor");
  const [deal, setDeal] = useState<RecordPickerCandidate | null>(null);
  const [category, setCategory] = useState<Category>("other");
  const [title, setTitle] = useState("");
  const [file, setFile] = useState<File | undefined>();
  // Set when the bytes landed and the metadata did not. It is not an error
  // state: the upload succeeded, and the row exists.
  const [partial, setPartial] = useState(false);

  // Read at the moment the request lands, not captured when it was sent. The
  // mutation's callbacks close over the render that pressed the button, where
  // the dialog was open by definition — so a plain `open` here would always be
  // true and the guard below would never fire.
  //
  // Written in an EFFECT, not during render: React may discard a render, and a
  // ref assigned in one publishes a value that was never committed.
  const openNow = useRef(open);
  useEffect(() => {
    openNow.current = open;
  }, [open]);

  // What this installation accepts, so an oversize file is refused here rather
  // than after every byte of it has crossed the wire.
  const maxUploadBytes = useMaxUploadBytes();
  const limitLabel = maxUploadBytes ? formatUploadLimit(maxUploadBytes) : "";

  // One page of the account's deals, read through the query cache so a second
  // search re-uses what the first walked. Keyed by the CURSOR, because that is
  // what identifies a page of a keyset walk.
  //
  // Built on every anchor, called only from the deal picker — which only the
  // account branch below renders. A hook cannot be conditional, and an
  // uncalled callback asks nothing.
  const fetchDealPage = useCallback(
    (cursor: string | null) =>
      queryClient.fetchQuery({
        queryKey: ["dealsForOrg", anchor.id, cursor],
        staleTime: DEAL_PAGE_FRESH_MS,
        queryFn: async (): Promise<DealPage> => {
          const { data, error } = await api.GET("/deals", {
            params: {
              query: {
                organization_id: anchor.id,
                limit: DEAL_PAGE_SIZE,
                ...(cursor ? { cursor } : {}),
              },
            },
          });
          if (error) {
            throwProblem(error);
          }
          return data;
        },
      }),
    [anchor.id, queryClient],
  );

  // Kept on `fetchDealPage` alone. RecordPicker reads a new `searchTargets`
  // identity as a new search space and empties the candidates it is showing, so
  // a callback rebuilt on anything that changes while the reader types would
  // clear the list under them.
  const searchDeals = useCallback(
    (query: string) =>
      walkAccountDeals(fetchDealPage, query.trim().toLocaleLowerCase()),
    [fetchDealPage],
  );

  const parent = parentOf(anchor, filing, deal);
  // All three asked unconditionally: the number of hooks a render performs must
  // not depend on which record the reader is filing against.
  const canWriteOrg = useCanWrite("organization", "update");
  const canWriteDeal = useCanWrite("deal", "update");
  const canWritePerson = useCanWrite("person", "update");
  // The upload's RBAC object IS the parent's entity type, so the grant this
  // dialog checks follows the CHOICE rather than the record it was opened from.
  // With "a deal" chosen and none picked yet there is no parent, and the grant
  // that governs the press is still the deal one.
  const writable: Readonly<Record<Parent["entityType"], boolean>> = {
    organization: canWriteOrg,
    person: canWritePerson,
    deal: canWriteDeal,
  };
  const permitted = writable[parent?.entityType ?? "deal"];

  // Emptying the form is separate from closing it, because the two happen at
  // different moments: every close empties, and the partial-failure path
  // empties the file without closing.
  const clearDraft = () => {
    setFiling("anchor");
    setDeal(null);
    setCategory("other");
    setTitle("");
    setFile(undefined);
  };

  // Everything the request needs arrives as a variable. A mutationFn closing
  // over `file` or `parent` would submit whatever the previous render held.
  const upload = useMutation({
    mutationFn: async (submitted: Submission) => {
      const id = await uploadFile(submitted);
      const patch = metadataFor(submitted);
      if (Object.keys(patch).length === 0) {
        return { filed: true };
      }
      if (id === undefined) {
        // Stored, but we never learned which row — so there is nothing to
        // address the metadata request to. Partial, for the same reason a
        // refused PATCH is partial: the document exists either way.
        return { filed: false };
      }
      // EVERY way the second call can fail is a partial success, not a
      // failure — a refusal, a dropped connection, a parse error alike. Once
      // the bytes are stored, the only wrong answer is the one that tells the
      // reader nothing was. Catching is what covers the thrown half: an
      // openapi-fetch rejection never reaches `error`, and left uncaught it
      // would surface as "Nothing was stored" over a document that is.
      try {
        const { error } = await api.PATCH("/attachments/{id}/metadata", {
          params: { path: { id } },
          body: patch,
        });
        return { filed: !error };
      } catch {
        return { filed: false };
      }
    },
    onSuccess: async (result) => {
      for (const queryKey of staleAfterUpload(anchor)) {
        await queryClient.invalidateQueries({ queryKey });
      }
      if (result.filed) {
        closeAndClear();
        return;
      }
      // Guarded on the dialog still being open. React Query runs a mutation to
      // completion whoever started it, so a reader who closed mid-flight would
      // otherwise be met on their NEXT visit by a warning about an upload from
      // the previous one, with nothing on screen it refers to.
      if (!openNow.current) {
        return;
      }
      // Kept open, with the draft cleared: the document is on the record, so
      // offering the same upload again would file it twice.
      clearDraft();
      setPartial(true);
    },
  });

  // Closing empties the form. The dialog is MOUNTED for the life of the card —
  // Modal renders nothing while shut, it does not unmount its children — so
  // anything left here is what the next opening starts with. That is a hazard
  // rather than untidiness: a file still in the field is one a later, unrelated
  // upload would silently send a second copy of.
  function closeAndClear() {
    clearDraft();
    setPartial(false);
    upload.reset();
    onClose();
  }

  const refusal = uploadRefusal({
    file,
    parent,
    permitted,
    pending: upload.isPending,
    maxBytes: maxUploadBytes,
  });

  return (
    <Modal open={open} onClose={closeAndClear} labelledBy={titleId}>
      <h2 id={titleId}>{t("docs.add.title")}</h2>

      {partial && (
        <Callout tone="warn" live="alert" title={t("docs.add.partialTitle")}>
          {t("docs.add.partial")}
        </Callout>
      )}
      {upload.isError && (
        <Callout tone="danger" live="alert" title={t("docs.add.failedTitle")}>
          {/* The SERVER's own sentence when it gave one. An oversize file and a
              permission denial are different problems with different next
              moves, and one fixed "try again" is wrong advice for the second
              and hides the size limit in the first. */}
          {problemMessageOf(upload.error, t, t("docs.add.failed"))}
        </Callout>
      )}

      {/* Asked only on an account, and asked as a QUESTION WITH TWO ANSWERS
          rather than a dropdown: both answers have to be readable at rest,
          because choosing between them is the decision this dialog exists to
          put in front of the reader, and a menu covering two options makes
          somebody open it to find out what the alternative was
          (design-system/choicelist.tsx). A contact's library has no second
          answer, so it asks nothing. */}
      {anchor.record === "organization" && (
        <>
          <ChoiceList
            legend={t("docs.add.about")}
            value={filing}
            onChange={setFiling}
            choices={[
              { value: "anchor", label: t("docs.add.thisCompany") },
              {
                value: "deal",
                label: t("docs.add.aDeal"),
                description: t("docs.add.aboutHint"),
              },
            ]}
          />
          {filing === "deal" && (
            <div className="field">
              <RecordPicker
                label={t("docs.add.dealSearch")}
                searchTargets={searchDeals}
                selected={deal}
                onPick={setDeal}
                disabled={upload.isPending}
              />
              {/* The reach, stated up front rather than after the reader has
                  failed to find something. It is the same sentence whatever
                  the account's size, which is what makes it trustworthy: a
                  caption that only appeared once a walk ran out would be a
                  claim about the last search rather than about the control,
                  and RecordPicker hands its caller no way to know which search
                  an answer belonged to. */}
              <p className="t-caption">
                {t("docs.add.dealSearchReach", {
                  deals: String(DEAL_SEARCH_REACH),
                  matches: String(DEAL_MATCH_LIMIT),
                })}
              </p>
            </div>
          )}
        </>
      )}

      <Field label={t("docs.add.category")}>
        {(control) => (
          <Select
            {...control}
            value={category}
            onChange={(picked) => setCategory(picked as Category)}
            options={UPLOADABLE_CATEGORIES.map((key) => ({
              value: key,
              label: t(CATEGORY_KEYS[key]),
            }))}
          />
        )}
      </Field>

      <Field label={t("docs.add.name")} hint={t("docs.add.nameHint")}>
        {(control) => (
          <TextInput
            {...control}
            value={title}
            onChange={(event) => setTitle(event.target.value)}
          />
        )}
      </Field>

      {/* The limit is the SERVER's, read from the installation rather than
          written here: it is set per deployment, so a number in this copy would
          be right only for whoever shipped the default. Until the answer
          arrives the hint says nothing at all — silence is honest, a guess is
          not, and the wait is one request long. */}
      <FileDropzone
        label={t("docs.add.file")}
        hint={
          limitLabel ? t("docs.add.fileHint", { size: limitLabel }) : undefined
        }
        emptyLabel={t("docs.add.fileEmpty")}
        file={file}
        onPick={setFile}
      />

      <div className="actions">
        <Button onClick={closeAndClear}>{t("docs.add.cancel")}</Button>
        <Button
          variant="primary"
          reason={refusal ? t(refusal, { size: limitLabel }) : undefined}
          onClick={() => {
            if (file && parent) {
              upload.mutate({ parent, category, title, file });
            }
          }}
        >
          {t(upload.isPending ? "docs.add.uploading" : "docs.add.submit")}
        </Button>
      </div>
    </Modal>
  );
}

/**
 * What a landed upload makes stale, per anchor.
 *
 * The two libraries are read by different surfaces and so by different keys:
 * the account's is one unpaginated read plus the 360 that counts it, the
 * contact's is its own cursor-paginated tab and nothing else — the person 360
 * carries no attachments section, so invalidating it would refetch a composite
 * that says nothing about the file just filed.
 */
function staleAfterUpload(anchor: DocumentAnchor): readonly QueryKey[] {
  if (anchor.record === "person") {
    return [["attachments", "person", anchor.id]];
  }
  if (anchor.record === "deal") {
    return [
      ["deal-documents", anchor.id],
      ["deal-attachments", anchor.id],
    ];
  }
  return [
    ["orgDocuments", anchor.id],
    ["organization360", anchor.id],
  ];
}

// Why the upload cannot be offered, in the order the reader can act on: a
// missing file is theirs to fix, a missing grant is not, and an upload already
// in flight is neither — it is the same press arriving twice.
//
// The in-flight case is a REFUSAL and not merely a label change, because the
// request it would repeat has already left: a second press lands a second copy
// of the document on the record, and nothing downstream can tell that from two
// deliberate uploads of the same file.
function uploadRefusal({
  file,
  parent,
  permitted,
  pending,
  maxBytes,
}: Readonly<{
  file: File | undefined;
  parent: Parent | null;
  permitted: boolean;
  pending: boolean;
  maxBytes: number | undefined;
}>): MessageKey | null {
  if (!permitted) {
    return "docs.add.errRefused";
  }
  if (!file) {
    return "docs.add.errNoFile";
  }
  // "A deal" chosen and none picked. Named as its own refusal rather than left
  // to fall back on the account: a document filed somewhere the reader did not
  // choose is worse than one not filed at all, and only a deal's documents can
  // be read for deal fields.
  if (!parent) {
    return "docs.add.errNoDeal";
  }
  if (pending) {
    return "docs.add.errInFlight";
  }
  // Only when the server has SAID what it accepts. An unanswered installation
  // read leaves the upload to be refused where it always was — sending a file
  // that turns out to be too large costs a wasted request, while guessing a
  // limit refuses one the installation would have taken.
  //
  // Compared against the file alone, though the ceiling bounds the whole
  // request. The few hundred bytes of part framing mean a file within that
  // distance of the limit still reaches the server's own refusal, which names
  // the same number this message does.
  if (maxBytes !== undefined && file.size > maxBytes) {
    return "docs.add.errTooLarge";
  }
  return null;
}
