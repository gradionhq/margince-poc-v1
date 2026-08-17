import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { FileDropzone } from "../design-system/filedropzone";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import { throwProblem } from "./common";

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
      queryClient.invalidateQueries({ queryKey: ["organization360", orgId] });
      queryClient.invalidateQueries({ queryKey: ["orgDocuments", orgId] });
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

      <FileDropzone
        label={t("contracts.form.file")}
        hint={t("contracts.form.fileHint")}
        emptyLabel={t("contracts.form.fileEmpty")}
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
