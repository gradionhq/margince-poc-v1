import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
// The installation read every form shares: one query, one key, so the currency
// this form writes is the same fact the settings screen shows.
import { useInstallationSettings } from "../app/uploadlimit";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { FileDropzoneControl } from "../design-system/filedropzone";
import { MoneyInput } from "../design-system/moneyinput";
import { Select } from "../design-system/select";
import { SurfaceState } from "../design-system/surfacestate";
import { useT } from "../i18n";
import { throwProblem } from "./common";
import { paperState, useContractPaper } from "./contractpaper";

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

// The draft the form edits. Money travels as a pair, so the amount and its
// currency live together — and that currency is only ever one the record or the
// installation stated, never a default this file picked on their behalf.
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
  // No currency of its own. There is no currency control on this form, so
  // whatever stands here is written to the record unseen — and a literal would
  // label every installation's agreements in one country's money. The unit
  // comes from the installation itself, resolved at the moment of the save.
  currency: "",
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
  // The installation's own declared currency — the only one this form can
  // supply, since it offers the reader no currency control. Undefined while the
  // read is in flight and if it never answers, and undefined stays undefined:
  // guessing a unit is the failure this whole pairing exists to prevent.
  const baseCurrency = useInstallationSettings().data?.base_currency;

  // The scale the amount field reads and writes in. A recorded agreement keeps
  // its OWN currency (draftOf preserves it); a new one takes the installation's
  // declared code once that read lands. The empty-string fallback is not a
  // guessed currency — it reaches only minorUnitDigits, whose unusable-code
  // answer is ISO's own default of two, which is what this field assumed
  // unconditionally before.
  const contractCurrency = draft.currency || baseCurrency || "";

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
      queryClient.invalidateQueries({ queryKey: ["organization360", orgId] });
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
          // MoneyInput rather than a TextInput this file scales itself, and the
          // reasons are the two defects the hand-rolled version had.
          //
          // It keeps the typed text as its OWN state, so a fractional amount is
          // not reformatted between keystrokes — typing "12.345" into a
          // three-decimal currency lost its tail when every keystroke was
          // scaled and echoed back.
          //
          // And it re-seeds when the CURRENCY changes, which this form needs
          // more than any other: the installation read that supplies the code
          // may land after the reader has already typed. Scaling on each
          // keystroke against a currency still in flight recorded the amount at
          // the two-digit fallback and then reinterpreted the same integer at
          // the real scale, with nothing on screen to say it had moved.
          <MoneyInput
            {...props}
            min={0}
            currency={contractCurrency}
            valueMinor={draft.valueMinor}
            // An agreement on record may carry no value at all — the two money
            // columns are paired and both NULL until somebody prices it — so an
            // unpriced one shows an empty field, not a nought nobody typed.
            blankWhenZero
            onChangeMinor={(valueMinor) => setDraft({ ...draft, valueMinor })}
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
          onClick={() =>
            save.mutate({ draft: pricedIn(draft, baseCurrency), file })
          }
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
    // A recorded agreement keeps its OWN currency. The two money columns are
    // paired by the database, so an agreement carrying none carries no amount
    // either: it is being priced here for the first time, exactly like a new
    // one, and takes the installation's unit at the save.
    currency: contract.currency ?? "",
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
 * The draft as it will be SAVED: an amount the reader typed, in the currency the
 * agreement already records, or else the installation's own.
 *
 * Resolved at the save rather than seeded into the draft because the modal opens
 * the instant a reader clicks a row, which can be before the installation read
 * has answered — a draft seeded with the blank would keep the blank after the
 * answer arrived, and re-seeding on arrival would throw away what the reader had
 * typed by then.
 *
 * With no answer at all the currency stays blank, and `contractBody` sends the
 * half-pair the form actually holds. That is the one honest option left: the
 * server refuses it where the reader can see the refusal, whereas dropping the
 * amount would report a saved agreement whose value quietly went nowhere.
 */
export function pricedIn(
  draft: ContractDraft,
  baseCurrency: string | undefined,
): ContractDraft {
  if (draft.currency !== "") {
    return draft;
  }
  return { ...draft, currency: baseCurrency ?? "" };
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
 * AND IT NEVER PRESENTS A PAGE AS A LIST. The documents endpoint paginates, so
 * an agreement with more filed paper than one page holds is `partial` and says
 * how much is missing — never a truncated list under a field that reads as
 * complete.
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
  const filed = useContractPaper(orgId, contractID);

  const paper = filed.data;
  const onFile = paper?.documents ?? [];
  const state = paperState(Boolean(contractID), filed, paper);
  // A drop zone always shows: uploading does not depend on being able to READ
  // what is already filed, and withholding the only way to attach paper would
  // punish the reader for a grant they do not have. What changes is whether
  // anything above it claims to be the full picture — `empty` is the one state
  // that does, and the only one whose picker says "drop a file here".

  return (
    <Field label={t("contracts.form.file")} hint={t("contracts.form.fileHint")}>
      {(props) => (
        <>
          {/* Each filed document, downloadable by name — and when the answer
              is not known, the reason instead. `empty` renders nothing here
              because the drop zone below already says the field is waiting for
              a file; two sentences saying the same absence is noise.

              `ready` and `partial` draw the same links; `partial` adds what is
              missing UNDER them, which is where a count about a list belongs. */}
          {state === "empty" ? null : (
            <SurfaceState
              state={state}
              emptyLabel=""
              detail={{
                onRetry: () => void filed.refetch(),
                remaining: paper?.remaining,
              }}
            >
              {onFile.map((doc) => (
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
              ))}
            </SurfaceState>
          )}
          {/* The zone is the design-system control, and this field owns the
              `Field` around it so the filed paper above can sit under the same
              label. `fileAdd` whenever this field is not asserting that nothing
              is filed — either because paper IS filed (an agreement can carry
              an amendment beside its original) or because the read did not come
              back, where "drop a file here" would quietly restate the absence
              the panel above just declined to claim. */}
          <FileDropzoneControl
            control={props}
            file={file}
            onPick={onPick}
            emptyLabel={t(
              state !== "empty"
                ? "contracts.form.fileAdd"
                : "contracts.form.fileEmpty",
            )}
          />
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
      // Same pairing as a create, and the same refusal to complete it with a
      // guess: an amount whose currency the form does not hold goes out as the
      // half it is, for the server to refuse in the open.
      currency:
        draft.valueMinor > 0 && draft.currency !== "" ? draft.currency : null,
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
  // Money is a PAIR: an amount with no currency cannot be converted, and the
  // record's own CHECK refuses half of one. So the amount travels with the
  // currency the form HOLDS — never with one it made up, and never without the
  // amount the reader typed. Holding no currency, the form sends the half-pair
  // and takes the refusal: an invented unit would be believed for the life of
  // the record, and dropping the amount would report a saved agreement whose
  // value quietly went nowhere.
  if (draft.valueMinor > 0) {
    body.value_minor = draft.valueMinor;
    if (draft.currency !== "") {
      body.currency = draft.currency;
    }
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
