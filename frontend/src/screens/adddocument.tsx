// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { FileDropzone } from "../design-system/filedropzone";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";

// Adding a document to an account, from the account.
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

type Attachment = components["schemas"]["Attachment"];
type Category = NonNullable<Attachment["category"]>;

const CATEGORY_KEYS: Record<Category, MessageKey> = {
  contract: "docs.category.contract",
  offer: "docs.category.offer",
  legal: "docs.category.legal",
  email_attachment: "docs.category.email",
  other: "docs.category.other",
};

// The parent the file hangs off, as one string the Select can carry. "org" is
// the account itself; anything else is a deal id behind this prefix.
const DEAL_PREFIX = "deal:";
const THIS_COMPANY = "org";

type Parent = { entityType: "organization" | "deal"; entityId: string };

function parseParent(choice: string, orgId: string): Parent {
  return choice.startsWith(DEAL_PREFIX)
    ? { entityType: "deal", entityId: choice.slice(DEAL_PREFIX.length) }
    : { entityType: "organization", entityId: orgId };
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

// How many deals the picker offers. A Select is a list, not a search, and one
// carrying every deal an old account ever had is not a control anybody can use.
// The cap is therefore deliberate — and it is a real limit: an account past it
// cannot file a document against its oldest deals from here. Issue 1536 tracks
// giving this a searchable picker, which is the shape that removes the cap
// rather than raising it.
const DEAL_CHOICES = 50;

/** The deals this document could be filed against, in the API's own order. */
function useAccountDeals(orgId: string, open: boolean) {
  return useQuery({
    queryKey: ["dealsForOrg", orgId],
    // A closed dialog asks nothing: the parent card renders on every company
    // page, and this list is only a question once somebody opens the form.
    enabled: open,
    queryFn: async () => {
      const { data, error } = await api.GET("/deals", {
        params: { query: { organization_id: orgId, limit: DEAL_CHOICES } },
      });
      if (error) {
        throwProblem(error);
      }
      return data?.data ?? [];
    },
  });
}

export function AddDocumentDialog({
  orgId,
  open,
  onClose,
}: Readonly<{ orgId: string; open: boolean; onClose: () => void }>) {
  const t = useT();
  const titleId = useId();
  const queryClient = useQueryClient();

  const [choice, setChoice] = useState(THIS_COMPANY);
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
  const openNow = useRef(open);
  openNow.current = open;

  const deals = useAccountDeals(orgId, open);
  const parent = parseParent(choice, orgId);
  const canWriteOrg = useCanWrite("organization", "update");
  const canWriteDeal = useCanWrite("deal", "update");
  const permitted = parent.entityType === "deal" ? canWriteDeal : canWriteOrg;

  // Emptying the form is separate from closing it, because the two happen at
  // different moments: every close empties, and the partial-failure path
  // empties the file without closing.
  const clearDraft = () => {
    setChoice(THIS_COMPANY);
    setCategory("other");
    setTitle("");
    setFile(undefined);
  };

  // Everything the request needs arrives as a variable. A mutationFn closing
  // over `file` or `choice` would submit whatever the previous render held.
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
      await queryClient.invalidateQueries({
        queryKey: ["orgDocuments", orgId],
      });
      await queryClient.invalidateQueries({
        queryKey: ["organization360", orgId],
      });
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

  const refusal = uploadRefusal(file, permitted, upload.isPending);

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
      {deals.isError && (
        <Callout tone="warn" live="status">
          {t("docs.add.dealsFailed")}
        </Callout>
      )}

      <Field label={t("docs.add.about")} hint={t("docs.add.aboutHint")}>
        {(control) => (
          <Select
            {...control}
            value={choice}
            onChange={setChoice}
            options={[
              { value: THIS_COMPANY, label: t("docs.add.thisCompany") },
              ...(deals.data ?? []).map((deal) => ({
                value: `${DEAL_PREFIX}${deal.id}`,
                label: deal.name,
              })),
            ]}
          />
        )}
      </Field>

      <Field label={t("docs.add.category")}>
        {(control) => (
          <Select
            {...control}
            value={category}
            onChange={(picked) => setCategory(picked as Category)}
            options={(Object.keys(CATEGORY_KEYS) as Category[]).map((key) => ({
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

      {/* 25 MB is what the server enforces, checked against a running one
          rather than read off a constant: the chassis bounded every body at
          1 MiB until this change, which made the upload handler's own 25 MiB
          cap dead and this hint a promise the product could not keep (issue
          1542). A number in copy is a claim about behaviour. */}
      <FileDropzone
        label={t("docs.add.file")}
        hint={t("docs.add.fileHint")}
        emptyLabel={t("docs.add.fileEmpty")}
        file={file}
        onPick={setFile}
      />

      <div className="actions">
        <Button onClick={closeAndClear}>{t("docs.add.cancel")}</Button>
        <Button
          variant="primary"
          reason={refusal ? t(refusal) : undefined}
          onClick={() =>
            file && upload.mutate({ parent, category, title, file })
          }
        >
          {t(upload.isPending ? "docs.add.uploading" : "docs.add.submit")}
        </Button>
      </div>
    </Modal>
  );
}

// Why the upload cannot be offered, in the order the reader can act on: a
// missing file is theirs to fix, a missing grant is not, and an upload already
// in flight is neither — it is the same press arriving twice.
//
// The in-flight case is a REFUSAL and not merely a label change, because the
// request it would repeat has already left: a second press lands a second copy
// of the document on the record, and nothing downstream can tell that from two
// deliberate uploads of the same file.
function uploadRefusal(
  file: File | undefined,
  permitted: boolean,
  pending: boolean,
): MessageKey | null {
  if (!permitted) {
    return "docs.add.errRefused";
  }
  if (!file) {
    return "docs.add.errNoFile";
  }
  if (pending) {
    return "docs.add.errInFlight";
  }
  return null;
}
