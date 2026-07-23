import { Bot, Check, CheckCircle2, Circle, ShieldCheck } from "lucide-react";
import type { components } from "../api/schema";
import { TextInput } from "../design-system/atoms";
import {
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { useT } from "../i18n";
import { coldFieldLabel } from "./common";
import { confidenceLevel } from "./inbox";
import {
  type CompanyDraft,
  type CompanyFieldName,
  CUSTOMER_FIELDS,
  groundingOf,
  isRequired,
  LEGAL_IDENTITY_FIELDS,
  MAX_SELECTED_FACTS,
  OFFER_FIELDS,
  SALES_FIELDS,
} from "./onboarding";

// The reviewable company form: the field groups at the house form rhythm,
// grounded values carrying their evidence and provenance, plus the
// legal-entity choice and the fact selection. The conversational shell
// hosts it as the "edit fields directly" escape hatch.

type ColdField = components["schemas"]["ColdStartField"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type CompanySiteReadLegalEntity =
  components["schemas"]["CompanySiteReadLegalEntity"];

export function CompanyStep({
  draft,
  setField,
  read,
  saved,
  saveError,
  missingRequired,
  selectedFactKeys,
  setSelectedFactKeys,
  onPickEntity,
  onFieldBlur,
  embedded = false,
}: Readonly<{
  draft: CompanyDraft;
  setField: (field: CompanyFieldName, value: string) => void;
  onPickEntity: (entity: CompanySiteReadLegalEntity) => void;
  read: CompanySiteRead | null;
  saved: boolean;
  saveError: string | null;
  missingRequired: readonly CompanyFieldName[];
  selectedFactKeys: readonly string[];
  setSelectedFactKeys: (keys: string[]) => void;
  onFieldBlur: () => void;
  embedded?: boolean;
}>) {
  const t = useT();

  return (
    <section className={embedded ? "ob-company-review" : "ob-panel"}>
      {!embedded && (
        <>
          <div className="kick">{t("ob.s1.kick")}</div>
          <h1 className="ttl">{t("ob.s1.title")}</h1>
          <p className="ob-sub">{t("ob.s1.sub")}</p>
        </>
      )}

      <div className="confirm-origin">
        <ShieldCheck aria-hidden />
        <span>
          {read
            ? t("ob.confirmWebsite", {
                count: read.pages_read ?? read.pages.length,
              })
            : t("ob.confirmManual")}
        </span>
      </div>

      {saved && (
        <p className="ob-sub" style={{ margin: "14px 0 0" }}>
          <CheckCircle2
            aria-hidden
            style={{ width: 14, height: 14, verticalAlign: "-2px" }}
          />{" "}
          {t("ob.s1.savedNote")}
        </p>
      )}

      {saveError && (
        <div className="readfail warn" style={{ marginTop: "var(--space-3)" }}>
          <span className="rfi">
            <Circle aria-hidden />
          </span>
          <div>
            <div className="rft">{t("ob.s1.saveFailed")}</div>
            <p className="rfp">{saveError}</p>
          </div>
        </div>
      )}

      {missingRequired.length > 0 && (
        <div className="urlnote err" style={{ marginTop: "var(--space-3)" }}>
          <Circle aria-hidden />{" "}
          {t("ob.s1.requiredMissing", {
            fields: missingRequired
              .map((field) => coldFieldLabel(field, t))
              .join(", "),
          })}
        </div>
      )}

      {/* One .form-stack carries the whole form at the house 8/12 rhythm; the
          two groups are separated by labeled dividers (the create-form
          pattern), not by per-field margins. */}
      <div className="form-stack ob-companyform">
        <p className="form-divider t-label">{t("ob.s1.identityLabel")}</p>
        <LegalEntityChoice read={read} draft={draft} onPick={onPickEntity} />
        <CompanyFieldList
          fields={LEGAL_IDENTITY_FIELDS}
          draft={draft}
          missingRequired={missingRequired}
          setField={setField}
          onBlur={onFieldBlur}
        />

        <p className="form-divider t-label">{t("ob.s1.offerLabel")}</p>
        <CompanyFieldList
          fields={OFFER_FIELDS}
          draft={draft}
          missingRequired={missingRequired}
          setField={setField}
          onBlur={onFieldBlur}
        />

        <p className="form-divider t-label">{t("ob.s1.customerLabel")}</p>
        <CompanyFieldList
          fields={CUSTOMER_FIELDS}
          draft={draft}
          missingRequired={missingRequired}
          setField={setField}
          onBlur={onFieldBlur}
        />

        <p className="form-divider t-label">{t("ob.s1.salesLabel")}</p>
        <CompanyFieldList
          fields={SALES_FIELDS}
          draft={draft}
          missingRequired={missingRequired}
          setField={setField}
          onBlur={onFieldBlur}
        />
      </div>

      {read && read.facts.length > 0 && (
        <details className="confirm-facts">
          {/* Collapsed by default: a hundred evidence cards between the form
              and the Continue button turns a review into a scroll. The
              summary states what is selected, which is the only thing a
              human needs unless they want to change it. */}
          <summary>
            <span className="seclabel">{t("ob.factsTitle")}</span>
            <span className="facts-count">
              {t("ob.factsSelected", {
                selected: selectedFactKeys.length,
                total: read.facts.length,
              })}
            </span>
          </summary>
          <p className="ob-sub">{t("ob.factsSub")}</p>
          <div className="fact-grid">
            {read.facts.map((fact) => {
              const selected = selectedFactKeys.includes(fact.value_key);
              const selectionFull =
                !selected && selectedFactKeys.length >= MAX_SELECTED_FACTS;
              return (
                <button
                  key={`${fact.field}:${fact.value_key}`}
                  type="button"
                  className={`fact-card ${selected ? "selected" : ""}`}
                  aria-pressed={selected}
                  disabled={selectionFull}
                  onClick={() =>
                    setSelectedFactKeys(
                      selected
                        ? selectedFactKeys.filter(
                            (key) => key !== fact.value_key,
                          )
                        : [...selectedFactKeys, fact.value_key],
                    )
                  }
                >
                  <span className="fact-check">
                    {selected ? <Check aria-hidden /> : <Circle aria-hidden />}
                  </span>
                  <span>
                    <b>{coldFieldLabel(fact.field, t)}</b>
                    <span>{fact.value}</span>
                    <small>{fact.evidence_snippet}</small>
                  </span>
                </button>
              );
            })}
          </div>
        </details>
      )}
    </section>
  );
}

function CompanyFieldList({
  fields,
  draft,
  missingRequired,
  setField,
  onBlur,
}: Readonly<{
  fields: readonly Exclude<CompanyFieldName, "website">[];
  draft: CompanyDraft;
  missingRequired: readonly CompanyFieldName[];
  setField: (field: CompanyFieldName, value: string) => void;
  onBlur: () => void;
}>) {
  const t = useT();
  return fields.map((field) => (
    <CompanyFormField
      key={field}
      field={field}
      value={draft.values[field]}
      grounded={groundingOf(draft, field)}
      edited={draft.edited.has(field)}
      required={isRequired(field)}
      error={missingRequired.includes(field) ? t("ob.s1.fieldRequired") : null}
      multiline={isMultilineField(field)}
      onChange={(value) => setField(field, value)}
      onBlur={onBlur}
    />
  ));
}

function isMultilineField(field: CompanyFieldName): boolean {
  return !(
    field === "display_name" ||
    field === "legal_name" ||
    field === "register_vat" ||
    field === "industry" ||
    field === "website"
  );
}

// The legal-entity choice. A group's imprint states one block per company
// — registered name, address, register number — and the read refuses to
// guess which of them the installation belongs to, because picking wrong
// writes another company's legal identity into this one's CRM. So it
// offers what it read and the human answers in one click, instead of
// retyping five lines the page already printed.
//
// One entity needs no question: the read already filled the fields.
function LegalEntityChoice({
  read,
  draft,
  onPick,
}: Readonly<{
  read: CompanySiteRead | null;
  draft: CompanyDraft;
  onPick: (entity: CompanySiteReadLegalEntity) => void;
}>) {
  const t = useT();
  const entities = read?.legal_entities ?? [];
  if (entities.length < 2) {
    return null;
  }
  const chosen = draft.values.legal_name.trim();
  return (
    <div className="legal-choice">
      <div className="l">{t("ob.legalTitle")}</div>
      <p className="ob-sub">{t("ob.legalSub")}</p>
      <div className="legal-grid">
        {entities.map((entity) => {
          const selected = chosen !== "" && chosen === entity.name;
          return (
            <button
              key={`${entity.name}-${entity.source_url}`}
              type="button"
              className={`legal-card ${selected ? "selected" : ""}`}
              aria-pressed={selected}
              onClick={() => onPick(entity)}
            >
              <span className="fact-check">
                {selected ? <Check aria-hidden /> : <Circle aria-hidden />}
              </span>
              <span>
                <b>{entity.name}</b>
                {entity.registered_address ? (
                  <span>{entity.registered_address}</span>
                ) : null}
                {entity.register_number ? (
                  <small>{entity.register_number}</small>
                ) : null}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

function CompanyFormField({
  field,
  value,
  grounded,
  edited,
  required,
  error,
  multiline,
  onChange,
  onBlur,
}: Readonly<{
  field: CompanyFieldName;
  value: string;
  grounded: ColdField | null;
  edited: boolean;
  required: boolean;
  error: string | null;
  multiline?: boolean;
  onChange: (v: string) => void;
  onBlur: () => void;
}>) {
  const t = useT();
  const id = `co-${field}`;
  const level = grounded ? confidenceLevel(grounded.confidence) : null;
  // The design-system field shape (create.tsx RecordFormBody is the reference):
  // .field + .t-label + .input/.textarea. The trust adornments (confidence,
  // read-from-site, typed-by-you) ride the label; the evidence chip sits under
  // the control. Onboarding gets no bespoke input styling — the form must read
  // as the same product as every other screen.
  return (
    <div className="field">
      <label className="t-label" htmlFor={id}>
        {coldFieldLabel(field, t)}
        {required ? " *" : ""} {level && <ConfidenceMeter level={level} />}
        {grounded && (
          <span className="rfprov">
            <Bot aria-hidden /> {t("ob.readFromSite")}
          </span>
        )}
        {edited && <ProvenanceTag provenance={{ kind: "human" }} />}
      </label>
      {multiline ? (
        <textarea
          id={id}
          className="textarea"
          value={value}
          required={required}
          aria-invalid={error ? true : undefined}
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur}
        />
      ) : (
        <TextInput
          id={id}
          value={value}
          required={required}
          aria-invalid={error ? true : undefined}
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur}
        />
      )}
      {grounded && (
        <EvidenceChip
          evidence={{
            snippet: grounded.evidence_snippet,
            // source_url is carried only by url-sourced evidence; text and
            // self-description evidence names its origin instead of linking.
            source: grounded.source_url ?? t("ob.readFromSite"),
          }}
        />
      )}
      {error && (
        <div className="urlnote err">
          <Circle aria-hidden /> {error}
        </div>
      )}
    </div>
  );
}
