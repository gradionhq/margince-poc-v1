import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type DragEvent, useEffect, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { Select } from "../design-system/select";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { useT } from "../i18n";
import { ProblemError, throwProblem } from "./common";
// The drop zone needs BOTH sheets, and neither was imported here.
//
// `.dropzone` — the dashed box, its padding and its dragover state — lives in
// onboarding.css, where the first drop zone was built; `.dropzone-input` and
// `.dropzone-label`, which hide the real file input and draw the text over it,
// live in company360.css. This form loaded neither and looked right only
// because the company page pulls both in elsewhere in the bundle. Rendered on
// its own — a story, or a route that opens the modal directly — it showed the
// browser's raw "Choose File" control and no box at all.
//
// Split across two screen sheets is not where this belongs; it is a
// design-system control with two homes. Filed rather than moved here, because
// moving it is a design-system change with its own review surface.
import "./company360.css";
import "./onboarding.css";

// Recording an agreement.
//
// THE SIGNED DATE IS NEVER PREFILLED. A deal's close timestamp records when
// somebody moved a stage, which is not evidence that anything was signed, and a
// date the form supplied would be indistinguishable from one a human asserted
// the moment it was saved. The field starts empty and says why.
//
// The status is absent from this form on purpose: an agreement is born a draft
// and leaves that state through its own transition, so a correction to a term
// can never silently activate a contract.

type Contract = components["schemas"]["Contract"];
type ValueBasis = NonNullable<
  components["schemas"]["CreateContractRequest"]["value_basis"]
>;

// The draft the form edits. Money travels as a pair, so the two fields live
// together and are submitted together or not at all.
type ContractDraft = {
  title: string;
  contractNumber: string;
  valueMinor: number;
  currency: string;
  valueBasis: ValueBasis;
  startsOn: string;
  endsOn: string;
  renewalOn: string;
  noticePeriodDays: string;
  signedOn: string;
};

const EMPTY_DRAFT: ContractDraft = {
  title: "",
  contractNumber: "",
  valueMinor: 0,
  currency: "EUR",
  valueBasis: "total",
  startsOn: "",
  endsOn: "",
  renewalOn: "",
  noticePeriodDays: "",
  signedOn: "",
};

export function ContractForm({
  orgId,
  contract,
  open,
  onClose,
}: Readonly<{
  orgId: string;
  contract?: Contract;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<ContractDraft>(draftOf(contract));
  const [file, setFile] = useState<File | undefined>();

  // Re-seed when the modal opens on a DIFFERENT agreement. Without this the
  // form keeps the previous row's values, and a reader correcting the second
  // contract they clicked would be editing the first one's numbers.
  useEffect(() => {
    if (open) {
      setDraft(draftOf(contract));
      setFile(undefined);
    }
  }, [open, contract]);

  // The draft is a VARIABLE, never a closure over render state: a click that
  // lands before React re-arms the mutation's options would otherwise submit
  // the previous render's form — choices nobody made.
  const save = useMutation({
    mutationFn: async (submitted: { draft: ContractDraft; file?: File }) => {
      const id = contract
        ? await patchContract(contract, submitted.draft)
        : await createContract(orgId, submitted.draft);
      if (submitted.file) {
        // A SECOND request, which can fail on its own. The agreement is saved
        // by then, so a failure here says the FILE did not attach rather than
        // implying the whole thing was lost.
        await uploadSignedFile(orgId, id, submitted.file);
      }
      return id;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["orgContracts", orgId] });
      queryClient.invalidateQueries({ queryKey: ["org360", orgId] });
      queryClient.invalidateQueries({ queryKey: ["orgDocuments", orgId] });
      // The paper list this form and the contract row BOTH read. Without it an
      // upload lands on the server and neither surface shows it: the row keeps
      // the pre-upload list, and reopening the form serves the same stale cache
      // while it refetches behind.
      queryClient.invalidateQueries({ queryKey: ["contractPaper", orgId] });
      onClose();
    },
  });

  const invalid = draftProblem(draft);

  return (
    <Modal open={open} onClose={onClose} labelledBy={titleId}>
      <h2 id={titleId}>
        {t(contract ? "contracts.form.editTitle" : "contracts.form.title")}
      </h2>

      <Field label={t("contracts.form.name")} required>
        {(props) => (
          <TextInput
            {...props}
            value={draft.title}
            onChange={(e) => setDraft({ ...draft, title: e.target.value })}
          />
        )}
      </Field>

      <Field label={t("contracts.form.number")}>
        {(props) => (
          <TextInput
            {...props}
            value={draft.contractNumber}
            onChange={(e) =>
              setDraft({ ...draft, contractNumber: e.target.value })
            }
          />
        )}
      </Field>

      <Field label={t("contracts.form.value")}>
        {(props) => (
          <TextInput
            {...props}
            type="number"
            min={0}
            step="0.01"
            value={draft.valueMinor === 0 ? "" : draft.valueMinor / 100}
            onChange={(e) =>
              setDraft({
                ...draft,
                valueMinor: Math.round(Number(e.target.value || 0) * 100),
              })
            }
          />
        )}
      </Field>

      {/* The basis is asked HERE, next to the amount, because it changes what
          the amount means. An open-ended agreement has no finite total, so it
          records twelve months and says so — and a figure whose basis was
          picked on another screen is a figure nobody checked. */}
      <Field label={t("contracts.form.basis")} required>
        {(props) => (
          <Select
            {...props}
            value={draft.valueBasis}
            onChange={(value) =>
              setDraft({ ...draft, valueBasis: value as ValueBasis })
            }
            options={[
              { value: "total", label: t("contracts.basis.total") },
              { value: "annualized_12m", label: t("contracts.basis.annual") },
            ]}
          />
        )}
      </Field>

      <Field label={t("contracts.form.startsOn")}>
        {(props) => (
          <TextInput
            {...props}
            type="date"
            value={draft.startsOn}
            onChange={(e) => setDraft({ ...draft, startsOn: e.target.value })}
          />
        )}
      </Field>

      {/* Empty means open-ended, which is a real shape rather than a missing
          answer — and it is exactly the case the annualized basis exists for. */}
      <Field
        label={t("contracts.form.endsOn")}
        hint={t("contracts.form.endsOnHint")}
      >
        {(props) => (
          <TextInput
            {...props}
            type="date"
            value={draft.endsOn}
            onChange={(e) => setDraft({ ...draft, endsOn: e.target.value })}
          />
        )}
      </Field>

      <Field label={t("contracts.form.renewalOn")}>
        {(props) => (
          <TextInput
            {...props}
            type="date"
            value={draft.renewalOn}
            onChange={(e) => setDraft({ ...draft, renewalOn: e.target.value })}
          />
        )}
      </Field>

      <Field
        label={t("contracts.form.noticeDays")}
        hint={t("contracts.form.noticeDaysHint")}
      >
        {(props) => (
          <TextInput
            {...props}
            type="number"
            min={0}
            value={draft.noticePeriodDays}
            onChange={(e) =>
              setDraft({ ...draft, noticePeriodDays: e.target.value })
            }
          />
        )}
      </Field>

      <Field
        label={t("contracts.form.signedOn")}
        hint={t("contracts.form.signedOnHint")}
      >
        {(props) => (
          <TextInput
            {...props}
            type="date"
            value={draft.signedOn}
            onChange={(e) => setDraft({ ...draft, signedOn: e.target.value })}
          />
        )}
      </Field>

      <SignedFileField
        orgId={orgId}
        contractID={contract?.id}
        file={file}
        onPick={setFile}
      />

      {save.error && (
        <p className="t-caption" role="alert">
          {save.error.message}
        </p>
      )}

      <div className="modal-actions">
        <Button onClick={onClose}>{t("create.cancel")}</Button>
        {/* The refusal travels WITH the control: a disabled button whose
            reason lives in a paragraph somewhere above it is announced to
            nobody using a screen reader, and cannot be focused to find out. */}
        <Button
          variant="primary"
          reason={invalid ? t(invalid) : undefined}
          disabled={save.isPending || invalid !== null}
          onClick={() => save.mutate({ draft, file })}
        >
          {t(contract ? "contracts.form.saveEdit" : "contracts.form.save")}
        </Button>
      </div>
    </Modal>
  );
}

// draftOf reads an existing agreement back into the form's shape, so correcting
// one starts from what is recorded rather than from a blank the reader has to
// retype — and might get wrong a second time.
function draftOf(contract: Contract | undefined): ContractDraft {
  if (!contract) {
    return EMPTY_DRAFT;
  }
  return {
    title: contract.title,
    contractNumber: contract.contract_number ?? "",
    valueMinor: contract.value_minor ?? 0,
    currency: contract.currency ?? "EUR",
    valueBasis: contract.value_basis as ValueBasis,
    startsOn: contract.starts_on ?? "",
    endsOn: contract.ends_on ?? "",
    renewalOn: contract.renewal_on ?? "",
    noticePeriodDays:
      contract.notice_period_days == null
        ? ""
        : String(contract.notice_period_days),
    signedOn: contract.signed_on ?? "",
  };
}

/**
 * paperState is what the field KNOWS about an agreement's documents.
 *
 * The four not-ready cases are kept apart because each is a different sentence
 * to the reader, and collapsing them into an empty list makes the field claim
 * "no paper on file" three times over when it has no idea. A contract being
 * created is the one honest empty: it cannot have documents yet.
 *
 * A 403 is WITHHELD, not failed. Reading documents carries its own grant, so a
 * reader without it must be told the answer is being kept from them rather than
 * offered a retry that will refuse again exactly the same way.
 */
export function paperState(
  hasContract: boolean,
  query: { isPending: boolean; isError: boolean; error: unknown },
  count: number,
): SectionState {
  if (!hasContract) {
    return "empty";
  }
  if (query.isPending) {
    return "loading";
  }
  if (query.isError) {
    return problemStatus(query.error) === 403 ? "withheld" : "failed";
  }
  return count === 0 ? "empty" : "ready";
}

// The HTTP status out of a thrown RFC-7807 body, or 0 when the failure carried
// none (a dropped connection throws no problem document at all).
function problemStatus(err: unknown): number {
  // `typeof null === "object"`, so the null check is not redundant: without it
  // a ProblemError carrying a null body throws while deciding how to report a
  // failure, turning a handled error into an unhandled one.
  if (!(err instanceof ProblemError) || !err.problem) {
    return 0;
  }
  if (typeof err.problem !== "object") {
    return 0;
  }
  const body = err.problem as Record<string, unknown>;
  return typeof body.status === "number" ? body.status : 0;
}

/**
 * SignedFileField shows the paper already on file and takes a new one by
 * drag-and-drop or by clicking.
 *
 * IT LISTS WHAT IS ALREADY THERE, because a form that only offers an upload
 * says, to anyone reading it, that there is nothing yet. Somebody opening an
 * agreement to check its terms wants the signed PDF, and the edit form is where
 * they land when they click the row — so a filed document that can only be
 * reached from somewhere else is a document they will conclude does not exist.
 *
 * AND IT NEVER SAYS THAT WHEN IT DOES NOT KNOW. A read still in flight, one
 * that failed, and one a grant refused are each a state where the answer is
 * unknown — and rendering a bare drop zone in any of them makes the same false
 * claim this field exists to stop, just later and more convincingly. Reading
 * documents needs its own grant, so a reader who cannot have them is told they
 * are withheld rather than shown an empty field about a contract that has
 * paper.
 *
 * The picker takes BOTH gestures, not one: dropping is what a reader reaches
 * for with a PDF already in front of them, and clicking is what works from a
 * keyboard and on a phone. A drop zone with no real input behind it is
 * unreachable for anyone not using a mouse, which is why the input is present
 * and merely made invisible.
 */
export function SignedFileField({
  orgId,
  contractID,
  file,
  onPick,
}: Readonly<{
  orgId: string;
  contractID?: string;
  file?: File;
  onPick: (file: File) => void;
}>) {
  const t = useT();
  const [over, setOver] = useState(false);
  // A contract being CREATED has no id and therefore no paper — asking would be
  // a request for the documents of an agreement that does not exist yet.
  const filed = useQuery({
    queryKey: ["contractPaper", orgId, contractID],
    enabled: Boolean(contractID),
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/documents", {
        params: {
          path: { id: orgId },
          query: { contract_id: contractID },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data?.data ?? [];
    },
  });

  const take = (dropped: FileList | null) => {
    const first = dropped?.[0];
    if (first) {
      onPick(first);
    }
  };

  const onFile = filed.data ?? [];
  const state = paperState(Boolean(contractID), filed, onFile.length);
  // A drop zone always shows: uploading does not depend on being able to READ
  // what is already filed, and withholding the only way to attach paper would
  // punish the reader for a grant they do not have. What changes is whether
  // anything above it claims to be the full picture.
  const known = state === "ready" || state === "empty";

  return (
    <Field label={t("contracts.form.file")} hint={t("contracts.form.fileHint")}>
      {(props) => (
        <>
          {/* Each filed document, downloadable by name — and when the answer
              is not known, the reason instead. `empty` renders nothing here
              because the drop zone below already says the field is waiting for
              a file; two sentences saying the same absence is noise. */}
          {known ? (
            onFile.map((doc) => (
              // `.link-button`, not the row link: a row title is a link
              // because of where it sits, but in a form a plain-coloured line
              // reads as a value somebody typed. This one has to look like the
              // download it is.
              <a
                key={doc.id}
                className="link-button"
                href={`/v1/attachments/${doc.id}`}
                download={doc.filename}
              >
                {doc.title || doc.filename}
              </a>
            ))
          ) : (
            <SurfaceState
              state={state}
              emptyLabel=""
              detail={
                state === "failed"
                  ? { onRetry: () => void filed.refetch() }
                  : {}
              }
            >
              {null}
            </SurfaceState>
          )}
          {/* A LABEL, not a div: it owns the real file input, so a click or a
              keypress anywhere in the zone opens the picker without a handler
              faking it, and a screen reader announces one control rather than
              an interactive box of unknown purpose. Dropping is the mouse
              affordance layered on top of that, not a replacement for it. */}
          <label
            className={over ? "dropzone dragover" : "dropzone"}
            onDragOver={(e: DragEvent<HTMLLabelElement>) => {
              e.preventDefault();
              setOver(true);
            }}
            onDragLeave={() => setOver(false)}
            onDrop={(e: DragEvent<HTMLLabelElement>) => {
              e.preventDefault();
              setOver(false);
              take(e.dataTransfer.files);
            }}
          >
            <input
              {...props}
              type="file"
              className="dropzone-input"
              onChange={(e) => take(e.target.files)}
            />
            <span className="dropzone-label">
              {/* The label says ADD whenever this field is not asserting that
                  nothing is filed — either because paper IS filed (an
                  agreement can carry an amendment beside its original) or
                  because the read did not come back, where "drop a file here"
                  would quietly restate the absence the panel above just
                  declined to claim. */}
              {file
                ? file.name
                : t(
                    onFile.length > 0 || !known
                      ? "contracts.form.fileAdd"
                      : "contracts.form.fileEmpty",
                  )}
            </span>
          </label>
        </>
      )}
    </Field>
  );
}

async function createContract(
  orgId: string,
  draft: ContractDraft,
): Promise<string> {
  const { data, error } = await api.POST("/contracts", {
    body: contractBody(orgId, draft),
  });
  if (error) {
    throwProblem(error);
  }
  return data?.id ?? "";
}

// A correction sends nulls for the fields a human cleared: once somebody has
// removed a value, "I typed this by mistake" and "we never agreed one" are the
// same answer, and leaving the old value in place would keep asserting the
// mistake.
async function patchContract(
  contract: Contract,
  draft: ContractDraft,
): Promise<string> {
  const { error } = await api.PATCH("/contracts/{id}", {
    params: { path: { id: contract.id } },
    body: {
      title: draft.title.trim(),
      contract_number: draft.contractNumber.trim() || null,
      value_minor: draft.valueMinor > 0 ? draft.valueMinor : null,
      currency: draft.valueMinor > 0 ? draft.currency : null,
      value_basis: draft.valueBasis,
      starts_on: draft.startsOn || null,
      ends_on: draft.endsOn || null,
      renewal_on: draft.renewalOn || null,
      notice_period_days: draft.noticePeriodDays
        ? Number(draft.noticePeriodDays)
        : null,
      signed_on: draft.signedOn || null,
    },
  });
  if (error) {
    throwProblem(error);
  }
  return contract.id;
}

// Sent as multipart by hand: the generated client serializes JSON bodies, and
// this endpoint takes a file part. The document is filed against the agreement
// AND against the company, so it also appears in the account's own library.
async function uploadSignedFile(orgId: string, contractID: string, file: File) {
  const body = new FormData();
  body.append("entity_type", "organization");
  body.append("entity_id", orgId);
  body.append("contract_id", contractID);
  body.append("file", file);
  const response = await fetch("/v1/attachments", {
    method: "POST",
    body,
    credentials: "include",
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => undefined);
    throwProblem(payload);
  }
}

// What the form refuses before the server has to.
//
// These mirror the database's own constraints rather than adding rules of their
// own: the server is still the authority, and a form that refused MORE than the
// server would make a legal agreement unrecordable.
export function draftProblem(
  draft: ContractDraft,
): "contracts.form.errNoName" | "contracts.form.errTermOrder" | null {
  if (draft.title.trim() === "") {
    return "contracts.form.errNoName";
  }
  if (
    draft.startsOn !== "" &&
    draft.endsOn !== "" &&
    draft.endsOn < draft.startsOn
  ) {
    return "contracts.form.errTermOrder";
  }
  return null;
}

// The wire body, with every empty field left OUT rather than sent as an empty
// string. An omitted date is "not recorded"; an empty string is a value the
// server would have to reject, and the difference is what keeps a half-filled
// form from reading as a half-known agreement.
export function contractBody(
  orgId: string,
  draft: ContractDraft,
): components["schemas"]["CreateContractRequest"] {
  const body: components["schemas"]["CreateContractRequest"] = {
    organization_id: orgId,
    title: draft.title.trim(),
    value_basis: draft.valueBasis,
    // Stated rather than defaulted: whether an agreement renews itself is a
    // fact about the paper, and a field the form quietly omitted would be a
    // guess the record could not distinguish from an answer.
    auto_renew: false,
  };
  if (draft.contractNumber.trim() !== "") {
    body.contract_number = draft.contractNumber.trim();
  }
  // Money is a PAIR: an amount with no currency cannot be converted and the
  // server refuses half of one, so the form sends both or neither.
  if (draft.valueMinor > 0) {
    body.value_minor = draft.valueMinor;
    body.currency = draft.currency;
  }
  if (draft.startsOn !== "") {
    body.starts_on = draft.startsOn;
  }
  if (draft.endsOn !== "") {
    body.ends_on = draft.endsOn;
  }
  if (draft.renewalOn !== "") {
    body.renewal_on = draft.renewalOn;
  }
  if (draft.noticePeriodDays !== "") {
    body.notice_period_days = Number(draft.noticePeriodDays);
  }
  if (draft.signedOn !== "") {
    body.signed_on = draft.signedOn;
  }
  return body;
}
