import { useId, useState } from "react";
import { Button, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import {
  apiKey,
  CF_TYPES,
  type CfObject,
  type CfType,
  ddlPreview,
  looksStructural,
} from "./customfields.logic";
import "./customfields.css";

// The add-field builder (AC-custom-fields-3..5/8): a governed form that turns a
// human's plain label into one typed scalar column on an existing object. The
// immutable cf_-prefixed API key and the pending DDL are shown before Confirm so
// the schema change is legible, a structural-sounding label is refused up front,
// and the 🟡 gate states that Confirm writes a live column + an audit row. This
// is NOT the ApprovalGate (Accept/Edit/Dismiss triad) — it is a local .cf-gate
// preview block owned by this screen.

export type NewFieldDraft = {
  object: CfObject;
  label: string;
  type: CfType;
  currency: string;
  options: string[];
};

export function FieldBuilder({
  object,
  pending,
  onSubmit,
  onToast,
}: Readonly<{
  object: CfObject;
  pending: boolean;
  onSubmit: (draft: NewFieldDraft) => void;
  onToast: (msg: string) => void;
}>) {
  const t = useT();
  const ids = { label: useId(), key: useId(), currency: useId() };
  const [label, setLabel] = useState("");
  const [type, setType] = useState<CfType>("text");
  const [currency, setCurrency] = useState("EUR");
  const [options, setOptions] = useState<string[]>([""]);
  const structural = looksStructural(label);
  const canConfirm = !pending && label.trim().length > 0 && !structural;

  const setOptionAt = (idx: number, value: string) => {
    setOptions((current) => current.map((opt, i) => (i === idx ? value : opt)));
  };

  const removeOption = (idx: number) => {
    // A picklist without an option is not a picklist — the last row is a floor,
    // not a delete target, so the intent is surfaced as a toast, not swallowed.
    if (options.length <= 1) {
      onToast(t("cf.lastOptionBlocked"));
      return;
    }
    setOptions((current) => current.filter((_, i) => i !== idx));
  };

  const confirm = () => {
    if (!canConfirm) {
      return;
    }
    onSubmit({ object, label: label.trim(), type, currency, options });
  };

  return (
    <section
      className="card cf-builder"
      aria-label={t("cf.builder.addTo", { object })}
    >
      <header className="cf-builder-head">
        <h3 className="section-header">{t("cf.builder.addTo", { object })}</h3>
        <span className="badge">{t("cf.builder.noCode")}</span>
      </header>
      <p className="cf-hint">{t("cf.builder.intro")}</p>

      <div className="cf-grid">
        <div className="field">
          <label className="t-label" htmlFor={ids.label}>
            {t("cf.label")}
          </label>
          <TextInput
            id={ids.label}
            value={label}
            onChange={(event) => setLabel(event.target.value)}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={ids.key}>
            {t("cf.apiKey")}
          </label>
          <TextInput
            id={ids.key}
            className="t-mono"
            value={apiKey(object, label)}
            disabled
            readOnly
          />
          <span className="cf-hint">{t("cf.apiKeyHint")}</span>
        </div>
      </div>

      <div className="field">
        <span className="t-label">{t("cf.typeLabel")}</span>
        <div className="cf-typegrid">
          {CF_TYPES.map((candidate) => (
            <button
              key={candidate}
              type="button"
              aria-pressed={candidate === type}
              className={
                candidate === type ? "cf-typebtn active" : "cf-typebtn"
              }
              onClick={() => setType(candidate)}
            >
              {t(`cf.type.${candidate}`)}
            </button>
          ))}
        </div>
      </div>

      {type === "currency" && (
        <div className="field">
          <label className="t-label" htmlFor={ids.currency}>
            {t("cf.currencyCode")}
          </label>
          <TextInput
            id={ids.currency}
            className="t-mono"
            value={currency}
            maxLength={3}
            onChange={(event) => setCurrency(event.target.value.toUpperCase())}
          />
          <span className="cf-hint">{t("cf.currencyHint")}</span>
        </div>
      )}

      {type === "picklist" && (
        <div className="field">
          <span className="t-label">{t("cf.options")}</span>
          <div className="cf-options">
            {options.map((option, idx) => (
              // Option rows have no stable id (they are user-typed values that
              // may repeat), so the row index is the only honest key here.
              // biome-ignore lint/suspicious/noArrayIndexKey: option rows are positional, not identity-keyed
              <div className="cf-option-row" key={idx}>
                <TextInput
                  aria-label={t("cf.optionPlaceholder")}
                  placeholder={t("cf.optionPlaceholder")}
                  value={option}
                  onChange={(event) => setOptionAt(idx, event.target.value)}
                />
                <Button
                  small
                  aria-label={t("cf.removeOption")}
                  onClick={() => removeOption(idx)}
                >
                  {"×"}
                </Button>
              </div>
            ))}
          </div>
          <Button
            small
            onClick={() => setOptions((current) => [...current, ""])}
          >
            {t("cf.addOption")}
          </Button>
        </div>
      )}

      {structural && (
        <div className="cf-refuse" role="alert">
          <strong>{t("cf.refuse.title")}</strong>
          <p>{t("cf.refuse.body")}</p>
          <p>{t("cf.refuse.route")}</p>
        </div>
      )}

      <div className="cf-gate">
        <strong>{t("cf.gate.title")}</strong>
        <p>{t("cf.gate.body", { object })}</p>
        <code className="cf-ddl">
          {ddlPreview(object, label, type, currency)}
        </code>
      </div>

      <div className="cf-actions">
        <Button variant="primary" disabled={!canConfirm} onClick={confirm}>
          {t("cf.confirm")}
        </Button>
        <Button
          onClick={() => {
            setLabel("");
            setType("text");
            setCurrency("EUR");
            setOptions([""]);
          }}
        >
          {t("cf.reset")}
        </Button>
      </div>
    </section>
  );
}
