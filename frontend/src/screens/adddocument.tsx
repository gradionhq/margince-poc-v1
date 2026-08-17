import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { FileDropzone } from "../design-system/filedropzone";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem } from "./common";

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
async function uploadFile(submitted: Submission): Promise<string> {
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
  const stored: Attachment = await response.json();
  return stored.id;
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

/** The deals this document could be filed against, newest first. */
function useAccountDeals(orgId: string, open: boolean) {
  return useQuery({
    queryKey: ["dealsForOrg", orgId],
    // A closed dialog asks nothing: the parent card renders on every company
    // page, and this list is only a question once somebody opens the form.
    enabled: open,
    queryFn: async () => {
      const { data, error } = await api.GET("/deals", {
        params: { query: { organization_id: orgId, limit: 50 } },
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

  const deals = useAccountDeals(orgId, open);
  const parent = parseParent(choice, orgId);
  const canWriteOrg = useCanWrite("organization", "update");
  const canWriteDeal = useCanWrite("deal", "update");
  const permitted = parent.entityType === "deal" ? canWriteDeal : canWriteOrg;

  const close = () => {
    setPartial(false);
    onClose();
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
      const { error } = await api.PATCH("/attachments/{id}/metadata", {
        params: { path: { id } },
        body: patch,
      });
      // Deliberately not thrown: the file is stored either way, and the caller
      // needs to tell those two outcomes apart.
      return { filed: !error };
    },
    onSuccess: async (result) => {
      await queryClient.invalidateQueries({
        queryKey: ["orgDocuments", orgId],
      });
      await queryClient.invalidateQueries({
        queryKey: ["organization360", orgId],
      });
      if (result.filed) {
        close();
        return;
      }
      // Kept open, with the file cleared: the document is on the record, so
      // offering the same upload again would file it twice.
      setFile(undefined);
      setPartial(true);
    },
  });

  const refusal = uploadRefusal(file, permitted);

  return (
    <Modal open={open} onClose={close} labelledBy={titleId}>
      <h2 id={titleId}>{t("docs.add.title")}</h2>

      {partial && (
        <Callout tone="warn" live="alert" title={t("docs.add.partialTitle")}>
          {t("docs.add.partial")}
        </Callout>
      )}
      {upload.isError && (
        <Callout tone="danger" live="alert" title={t("docs.add.failedTitle")}>
          {t("docs.add.failed")}
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

      <FileDropzone
        label={t("docs.add.file")}
        hint={t("docs.add.fileHint")}
        emptyLabel={t("docs.add.fileEmpty")}
        file={file}
        onPick={setFile}
      />

      <div className="actions">
        <Button onClick={close}>{t("docs.add.cancel")}</Button>
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
// missing file is theirs to fix, a missing grant is not.
function uploadRefusal(
  file: File | undefined,
  permitted: boolean,
): MessageKey | null {
  if (!permitted) {
    return "docs.add.errRefused";
  }
  if (!file) {
    return "docs.add.errNoFile";
  }
  return null;
}
