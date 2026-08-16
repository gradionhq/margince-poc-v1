import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { MoneyInput } from "../design-system/moneyinput";
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
  open,
  onClose,
}: Readonly<{ orgId: string; open: boolean; onClose: () => void }>) {
  const t = useT();
  const titleId = useId();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<ContractDraft>(EMPTY_DRAFT);

  // The draft is a VARIABLE, never a closure over render state: a click that
  // lands before React re-arms the mutation's options would otherwise submit
  // the previous render's form — choices nobody made.
  const create = useMutation({
    mutationFn: async (submitted: ContractDraft) => {
      const { data, error } = await api.POST("/contracts", {
        body: contractBody(orgId, submitted),
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      setDraft(EMPTY_DRAFT);
      queryClient.invalidateQueries({ queryKey: ["orgContracts", orgId] });
      queryClient.invalidateQueries({ queryKey: ["org360", orgId] });
      onClose();
    },
  });

  const invalid = draftProblem(draft);

  return (
    <Modal open={open} onClose={onClose} labelledBy={titleId}>
      <h2 id={titleId}>{t("contracts.form.title")}</h2>

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
          <MoneyInput
            {...props}
            valueMinor={draft.valueMinor}
            onChangeMinor={(minor) => setDraft({ ...draft, valueMinor: minor })}
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

      {create.error && (
        <p className="t-caption" role="alert">
          {create.error.message}
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
          disabled={create.isPending || invalid !== null}
          onClick={() => create.mutate(draft)}
        >
          {t("contracts.form.save")}
        </Button>
      </div>
    </Modal>
  );
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
